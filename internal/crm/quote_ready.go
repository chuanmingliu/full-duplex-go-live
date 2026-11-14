package crm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/tools"
)

const (
	quoteReadyMaxResults = 32
	quoteReadyMaxBytes   = 256 << 10
)

var quoteReadyCall = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// Written by a cache entry's owner before closing done, then immutable. In
// particular, snapshots never read a partially filled quote or audit record.
type readyQuote struct {
	call       string
	pricing    readyPricing
	completed  time.Time
	equivalent string
	value      tools.ReadyQuote
}

type readyPricing struct {
	fingerprint  string
	api          bool
	rulesVersion int
	rulesSHA     string
}

// A conservative pricing identity includes intake plans and both pricing
// sources. Config changes invalidate old hints, even when a source is inactive.
// Reading config files also detects edits that bypass versioned configuration.
func (s *Service) currentReadyPricing() readyPricing {
	var versions [3]*ConfigVersion
	for i, name := range []string{QuotePlansName, QuoteAPIName, QuoteRulesName} {
		version, err := s.configs.Get(name, 0)
		if err != nil {
			return readyPricing{}
		}
		versions[i] = version
	}
	var api QuoteAPI
	if json.Unmarshal(versions[1].Body, &api) != nil {
		return readyPricing{}
	}
	if s.cfg.QuoteAPIBase != "" {
		api.BaseURL = s.cfg.QuoteAPIBase
	}
	// Ensure the separately read files are still the current version set.
	s.configs.mu.Lock()
	defer s.configs.mu.Unlock()
	for _, version := range versions {
		current := s.configs.versions[version.Name]
		if len(current) == 0 || current[len(current)-1].Version != version.Version || current[len(current)-1].SHA != version.SHA {
			return readyPricing{}
		}
	}
	sum := sha256.New()
	for _, version := range versions {
		fmt.Fprintf(sum, "%s:%d:%s\n", version.Name, version.Version, version.SHA)
	}
	fmt.Fprint(sum, s.cfg.QuoteAPIBase)
	return readyPricing{fingerprint: hex.EncodeToString(sum.Sum(nil)), api: strings.TrimSpace(api.BaseURL) != "", rulesVersion: versions[2].Version, rulesSHA: versions[2].SHA}
}

func (s *Service) completedReadyQuote(call string, started time.Time, pricing readyPricing, result any) *readyQuote {
	if !quoteReadyCall.MatchString(call) || pricing.fingerprint == "" || pricing != s.currentReadyPricing() {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > quoteReadyMaxBytes {
		return nil
	}
	var fields struct {
		Status       string `json:"status"`
		Source       string `json:"source"`
		RulesVersion int    `json:"rules_version"`
		RulesSHA     string `json:"rules_sha"`
		ValidForS    int64  `json:"valid_for_s"`
	}
	if json.Unmarshal(raw, &fields) != nil || fields.Status != "quote" {
		return nil
	}
	if pricing.api {
		if fields.Source != "quote-api" {
			return nil
		}
	} else if fields.RulesVersion != pricing.rulesVersion || fields.RulesSHA != pricing.rulesSHA {
		return nil
	}
	valid := prequoteValidity
	if fields.ValidForS > 0 {
		// Reject durations that cannot be represented without wrapping.
		if fields.ValidForS > int64((1<<63-1)/time.Second) {
			return nil
		}
		valid = time.Duration(fields.ValidForS) * time.Second
	}
	// Match the existing cache's conservative expiry, measured from its claim.
	expires := started.Add(valid)
	if !time.Now().Before(expires) {
		return nil
	}
	// Presentation/cache markers do not create a second identity for the same
	// completed default that also lives in the ordinary selection cache. Public
	// IDs still include every result field, so changing presentation cannot reuse
	// an ID already observed by a consumer before the default completed.
	var identity map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&identity) != nil {
		return nil
	}
	for _, key := range []string{"default", "prefetched_ms", "cached", "prequoted", "prequoted_ms"} {
		delete(identity, key)
	}
	canonical, _ := json.Marshal(identity)
	equivalent := sha256.Sum256(append(append([]byte(call), 0), canonical...))
	id := sha256.Sum256(append(append([]byte(call), 0), raw...))
	return &readyQuote{call: call, pricing: pricing, completed: time.Now(), equivalent: hex.EncodeToString(equivalent[:]), value: tools.ReadyQuote{ID: hex.EncodeToString(id[:]), ExpiresAt: expires, Result: raw}}
}

// readyQuotes only observes completed cache entries. Closing done publishes all
// their immutable metadata; no worker, timer, pricing request or wait is added.
func (s *Service) readyQuotes(call string) []*readyQuote {
	s.mu.Lock()
	defer s.mu.Unlock()
	var defaults []*readyQuote
	if p := s.prefetches[call]; p != nil {
		select {
		case <-p.done:
			if p.ready != nil {
				defaults = append(defaults, p.ready)
			}
		default:
		}
	}
	var normal []*readyQuote
	for _, p := range s.prequotes {
		select {
		case <-p.done:
			if p.ready != nil && p.ready.call == call {
				normal = append(normal, p.ready)
			}
		default:
		}
	}
	sort.Slice(normal, func(i, j int) bool {
		if normal[i].completed.Equal(normal[j].completed) {
			return normal[i].value.ID < normal[j].value.ID
		}
		return normal[i].completed.Before(normal[j].completed)
	})
	return append(defaults, normal...)
}

func (s *Service) handleQuoteReady(w http.ResponseWriter, r *http.Request) error {
	query, err := quoteReadyCallQuery(r)
	if err != nil {
		return err
	}
	pricing := s.currentReadyPricing()
	snapshot := tools.QuoteReadySnapshot{Call: query, Quotes: []tools.ReadyQuote{}}
	seen := map[string]bool{}
	now := time.Now()
	if pricing.fingerprint != "" {
		for _, entry := range s.readyQuotes(query) {
			if entry.call != query || entry.pricing != pricing || !now.Before(entry.value.ExpiresAt) || seen[entry.equivalent] {
				continue
			}
			snapshot.Quotes = append(snapshot.Quotes, entry.value)
			body, _ := json.Marshal(snapshot)
			if len(body)+1 > quoteReadyMaxBytes {
				snapshot.Quotes = snapshot.Quotes[:len(snapshot.Quotes)-1]
				continue
			}
			seen[entry.equivalent] = true
			if len(snapshot.Quotes) == quoteReadyMaxResults {
				break
			}
		}
	}
	// If configuration changed while assembling, return no stale results. This
	// final observation is the snapshot's pricing boundary, not a pricing lock.
	if pricing != s.currentReadyPricing() {
		snapshot.Quotes = []tools.ReadyQuote{}
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(append(body, '\n'))
	return err
}

func quoteReadyCallQuery(r *http.Request) (string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	calls := query["call"]
	if err != nil || len(query) != 1 || len(calls) != 1 || !quoteReadyCall.MatchString(calls[0]) {
		return "", fail(http.StatusBadRequest, "one exact call identifier is required")
	}
	return calls[0], nil
}
