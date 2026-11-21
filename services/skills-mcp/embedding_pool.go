package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"
	"time"
)

const embeddingRetryDelay = 5 * time.Second

var embeddingIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

type embeddingSettings struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Model          string `json:"model"`
	Revision       string `json:"revision"`
	TokenFile      string `json:"token_file,omitempty"`
	QueryPrefix    string `json:"query_prefix,omitempty"`
	DocumentPrefix string `json:"document_prefix,omitempty"`
}

func embeddingPoolFromFile(path string) (*embeddingClient, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open embedding pool: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("invalid embedding pool file size")
	}
	var config struct {
		Members []embeddingSettings `json:"members"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("invalid embedding pool: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || len(config.Members) < 2 || len(config.Members) > 8 {
		return nil, errors.New("embedding pool requires 2 to 8 members and one JSON object")
	}
	pool := &embeddingClient{}
	names := map[string]bool{}
	for _, settings := range config.Members {
		if !embeddingIdentifier.MatchString(settings.Name) || len(settings.Name) > 64 || names[settings.Name] ||
			!embeddingIdentifier.MatchString(settings.Revision) || len(settings.Revision) > 128 {
			return nil, errors.New("embedding members require unique names (1-64 ASCII identifier characters) and pinned revisions (1-128 ASCII identifier characters)")
		}
		client, err := newEmbeddingClient(settings)
		if err != nil {
			return nil, fmt.Errorf("embedding member %q: %w", settings.Name, err)
		}
		names[settings.Name] = true
		pool.members = append(pool.members, client)
	}
	return pool, nil
}

// Health is shared across catalog revisions. A single request probes an expired
// circuit while other requests continue to the next member.
type embeddingHealth struct {
	mu         sync.Mutex
	retryAfter time.Time
	probing    bool
	generation uint64
}

func (h *embeddingHealth) acquire() (uint64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.retryAfter.IsZero() {
		if time.Now().Before(h.retryAfter) || h.probing {
			return 0, false
		}
		h.probing = true
	}
	return h.generation, true
}

func (h *embeddingHealth) finish(generation uint64, outcome error, canceled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Completions admitted before a circuit transition cannot undo it.
	if generation != h.generation {
		return
	}
	h.probing = false
	if canceled {
		return
	}
	if outcome != nil {
		h.retryAfter = time.Now().Add(embeddingRetryDelay)
		h.generation++
	} else if !h.retryAfter.IsZero() {
		h.retryAfter = time.Time{}
		h.generation++
	}
}

type embeddingMember struct {
	client     *embeddingClient
	mu         sync.Mutex
	view       *skillIndex
	building   bool
	retryAfter time.Time
}

type semanticPool struct {
	catalog *skillIndex
	members []*embeddingMember
}

func (x *skillIndex) prepareEmbeddingPool(ctx context.Context, client *embeddingClient, previous *skillIndex) error {
	pool := &semanticPool{catalog: x}
	var work sync.WaitGroup
	for _, provider := range client.members {
		member := &embeddingMember{client: provider}
		pool.members = append(pool.members, member)
		var reusable *skillIndex
		if previous != nil && previous.pool != nil {
			for _, old := range previous.pool.members {
				if old.client == provider {
					old.mu.Lock()
					reusable = old.view
					old.mu.Unlock()
					break
				}
			}
		}
		work.Add(1)
		go func(member *embeddingMember, reusable *skillIndex) {
			defer work.Done()
			member.build(ctx, x, reusable)
		}(member, reusable)
	}
	work.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, member := range pool.members {
		if member.view != nil {
			x.pool = pool
			return nil
		}
	}
	return errors.New("no embedding pool member could index this catalog revision")
}

func (m *embeddingMember) build(ctx context.Context, catalog, previous *skillIndex) {
	// Each member owns a complete vector space for exactly this revision.
	view := *catalog
	view.pool, view.semantic = nil, nil
	view.provider = m.client.name
	view.providerRevision = m.client.revision
	err := view.prepareEmbeddings(ctx, m.client, previous)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.building = false
	if err != nil {
		m.retryAfter = time.Now().Add(embeddingRetryDelay)
		return
	}
	m.view = &view
	m.retryAfter = time.Time{}
}

func (m *embeddingMember) ready(catalog *skillIndex) *skillIndex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.view == nil && !m.building && !time.Now().Before(m.retryAfter) {
		m.building = true
		// Repair is single-flight and bounded. Caller cancellation must not
		// discard a complete repair needed by other searches of this revision.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			m.build(ctx, catalog, nil)
		}()
	}
	return m.view
}

func (p *semanticPool) search(ctx context.Context, in skillRequest, text string, limit, budget int) (skillReply, error) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	for i, member := range p.members {
		if err := ctx.Err(); err != nil {
			return skillReply{}, err
		}
		view := member.ready(p.catalog)
		if view == nil {
			continue
		}
		generation, admitted := member.client.health.acquire()
		if !admitted {
			continue
		}
		// Reserve a share of the remaining deadline for every later candidate.
		allowance := time.Until(deadline) / time.Duration(len(p.members)-i)
		attempt, stop := context.WithTimeout(ctx, allowance)
		out, err := view.semanticSearch(attempt, in, text, limit, budget)
		stop()
		inputError := errors.Is(err, errEmbeddingInput)
		member.client.health.finish(generation, err, ctx.Err() != nil || inputError)
		if inputError {
			return skillReply{}, err
		}
		if err == nil {
			return out, nil
		}
	}
	return skillReply{}, errors.New("embedding pool unavailable")
}
