package crm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/jinja"
)

// Config is where the service keeps its data and how it reaches the things
// it works with.
type Config struct {
	// Dir holds everything: the event log, the templates, the rendered
	// prompts, the token. It is made, and kept, readable by its owner alone.
	Dir string
	// OriginatorURL is the call originator's address, and OriginatorEnv its
	// env file, which names the one number it will dial (ALLOWED_CALLEE).
	// That number is read each time a call is placed and never logged.
	OriginatorURL string
	OriginatorEnv string
	// GoliveURL is the phone line's golive: it is asked whether a call is in
	// progress before another is placed. Empty skips the question.
	GoliveURL string
	// QuoteAPIBase, when set, is the quotation service's address (up to and
	// including /intranet) and overrides the quote-api config's base_url: the
	// address is the deployment's, the rest of the config the business's.
	QuoteAPIBase string
	// Location is the time zone a render's nowDateTime is written in.
	Location *time.Location
	Log      *slog.Logger
	// HTTP is used for the originator and golive; nil is a client with a
	// timeout long enough for a dial to be accepted.
	HTTP *http.Client
}

// Service is the running thing.
type Service struct {
	cfg     Config
	log     *slog.Logger
	events  *Log
	prompts *Prompts
	configs *Configs
	token   string

	// quoteClient reaches the quotation service (nil: a default).
	quoteClient *http.Client

	mu       sync.Mutex
	calls    []CallRecord // newest last
	dialing  bool
	greeting string
}

const tokenFile = "api.token"

// greetingFile holds the default opening line: what a call opens with when
// the request gives neither greeting nor start.
const greetingFile = "greeting.txt"

// Greeting is the default opening line.
func (s *Service) Greeting() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.greeting
}

// SetGreeting changes it, for good.
func (s *Service) SetGreeting(line, by string) error {
	line = strings.TrimSpace(line)
	if line == "" || len([]rune(line)) > 300 {
		return fail(http.StatusUnprocessableEntity, "the opening line is one to three hundred characters")
	}
	if err := writeFileAtomic(filepath.Join(s.cfg.Dir, greetingFile), []byte(line+"\n")); err != nil {
		return err
	}
	if _, err := s.events.Append(Event{By: by, Type: "greeting.put", Note: fmt.Sprintf("%d characters", len([]rune(line)))}); err != nil {
		return err
	}
	s.mu.Lock()
	s.greeting = line
	s.mu.Unlock()
	s.log.Info("crm: default opening line set", "chars", len([]rune(line)), "by", by)
	return nil
}

func (s *Service) defaults() map[string]any {
	if g := s.Greeting(); g != "" {
		return map[string]any{"start": g}
	}
	return nil
}

// Open starts the service on its directory, replaying its log.
func Open(cfg Config) (*Service, error) {
	if cfg.Dir == "" {
		return nil, errors.New("crm: no data directory")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 45 * time.Second}
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("crm: %w", err)
	}
	if err := os.Chmod(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("crm: %w", err)
	}
	s := &Service{cfg: cfg, log: cfg.Log}
	s.prompts = &Prompts{dir: cfg.Dir, templates: map[string][]*Template{}, parsed: map[string]*jinja.Template{}}
	s.configs = &Configs{dir: cfg.Dir, versions: map[string][]*ConfigVersion{}}
	events, torn, err := OpenLog(cfg.Dir, func(ev Event) error {
		s.prompts.apply(ev)
		s.configs.apply(ev)
		if ev.Type == "call.placed" && ev.Call != nil {
			s.calls = append(s.calls, *ev.Call)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.events, s.prompts.log, s.configs.log = events, events, events
	// The quotation rules start as placeholders, so that a quote can be asked
	// for before the business has put its own tables in; the service config
	// starts without a base URL, so quotes go to the rules until it has one.
	if _, err := s.configs.Get(QuoteRulesName, 0); errors.Is(err, ErrNotFound) {
		if _, err := s.configs.Save(QuoteRulesName, []byte(DefaultQuoteRules), "placeholder rules: every number is made up", "crm"); err != nil {
			events.Close()
			return nil, err
		}
	}
	if _, err := s.configs.Get(QuoteAPIName, 0); errors.Is(err, ErrNotFound) {
		if _, err := s.configs.Save(QuoteAPIName, []byte(DefaultQuoteAPI), "no base_url yet: quotes go to the rules", "crm"); err != nil {
			events.Close()
			return nil, err
		}
	}
	if torn > 0 {
		s.log.Warn("crm: the log ended in a line that was never finished; it was cut off", "bytes", torn)
	}
	if data, err := os.ReadFile(filepath.Join(cfg.Dir, greetingFile)); err == nil {
		s.greeting = strings.TrimSpace(string(data))
	}
	if s.token, err = loadToken(filepath.Join(cfg.Dir, tokenFile)); err != nil {
		events.Close()
		return nil, err
	}
	s.log.Info("crm: open", "dir", cfg.Dir, "events", events.Seq(), "templates", len(s.prompts.templates), "calls", len(s.calls))
	return s, nil
}

// Quote prices a coverage selection under the quotation rules, and records
// that it did. The rules' simulate_delay_ms, when set, is waited out first:
// it stands in for a real rating service's time, so that the assistant's
// handling of a slow answer can be exercised.
func (s *Service) Quote(req QuoteRequest) (any, error) {
	if api, v := s.quoteAPI(); api != nil {
		return s.quoteViaAPI(api, v, req)
	}
	return s.quoteViaRules(req)
}

// quoteAPI is the service's config when it names a service to call.
func (s *Service) quoteAPI() (*QuoteAPI, *ConfigVersion) {
	v, err := s.configs.Get(QuoteAPIName, 0)
	if err != nil {
		return nil, nil
	}
	var api QuoteAPI
	if err := json.Unmarshal(v.Body, &api); err != nil {
		return nil, nil
	}
	if s.cfg.QuoteAPIBase != "" {
		api.BaseURL = s.cfg.QuoteAPIBase
	}
	if strings.TrimSpace(api.BaseURL) == "" {
		return nil, nil
	}
	return &api, v
}

// quoteViaAPI asks the business's quotation service and records the answer.
func (s *Service) quoteViaAPI(api *QuoteAPI, v *ConfigVersion, req QuoteRequest) (any, error) {
	began := time.Now()
	rec := &QuoteRecord{At: began, Call: req.Call, MemoryVersion: req.MemoryVersion, Source: "quote-api", APIVersion: v.Version}
	for name, choice := range req.Config {
		if selected(choice) {
			rec.Coverages = append(rec.Coverages, name)
		}
	}
	sort.Strings(rec.Coverages)
	q, _, err := api.Quote(context.Background(), s.quoteClient, req)
	rec.ComputeMS = time.Since(began).Milliseconds()
	if q != nil {
		rec.Total, rec.InsureFlowCode, rec.QuotePriceID = q.Total, q.InsureFlowCode, q.QuotePriceID
	}
	if err != nil {
		rec.Error = err.Error()
	}
	if _, lerr := s.events.Append(Event{By: "tool", Type: "quote.computed", Ref: req.Call, Quote: rec}); lerr != nil {
		s.log.Warn("crm: a quote was not recorded", "err", lerr)
	}
	s.log.Info("crm: quote via the service", "call", req.Call, "coverages", len(rec.Coverages), "total", rec.Total, "api", v.Version,
		"ms", rec.ComputeMS, "flow", rec.InsureFlowCode, "err", rec.Error)
	if err != nil {
		return nil, err
	}
	return q, nil
}

// quoteViaRules prices under the placeholder rules.
func (s *Service) quoteViaRules(req QuoteRequest) (*Quote, error) {
	began := time.Now()
	v, err := s.configs.Get(QuoteRulesName, req.RulesVersion)
	if err != nil {
		return nil, err
	}
	var rules QuoteRules
	if err := json.Unmarshal(v.Body, &rules); err != nil {
		return nil, fmt.Errorf("crm: the quote rules v%d are not readable: %w", v.Version, err)
	}
	if rules.SimulateDelayMS > 0 {
		time.Sleep(time.Duration(rules.SimulateDelayMS) * time.Millisecond)
	}
	q, err := rules.Compute(req)
	rec := &QuoteRecord{At: began, Call: req.Call, RulesVersion: v.Version, MemoryVersion: req.MemoryVersion, ComputeMS: time.Since(began).Milliseconds(), Source: "rules"}
	for name, choice := range req.Config {
		if b, ok := choice.(bool); ok && !b {
			continue
		}
		rec.Coverages = append(rec.Coverages, name)
	}
	sort.Strings(rec.Coverages)
	if err != nil {
		rec.Error = err.Error()
	} else {
		q.RulesVersion, q.RulesSHA, q.MemoryVersion = v.Version, v.SHA, req.MemoryVersion
		q.ComputedAt, q.ComputeMS = began, rec.ComputeMS
		rec.Total = q.Total
	}
	if _, lerr := s.events.Append(Event{By: "tool", Type: "quote.computed", Ref: req.Call, Quote: rec}); lerr != nil {
		s.log.Warn("crm: a quote was not recorded", "err", lerr)
	}
	s.log.Info("crm: quote", "call", req.Call, "coverages", len(rec.Coverages), "total", rec.Total, "rules", v.Version, "ms", rec.ComputeMS, "err", rec.Error)
	return q, err
}

// Configs is the service's versioned configs.
func (s *Service) Configs() *Configs { return s.configs }

// Close finishes with the log.
func (s *Service) Close() error { return s.events.Close() }

// Prompts is the service's templates.
func (s *Service) Prompts() *Prompts { return s.prompts }

// loadToken reads the API token, making one the first time. It is a secret
// kept in a file only its owner can read, like a provider's key.
func loadToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(data)); len(token) >= 32 {
			return token, nil
		}
		return "", fmt.Errorf("crm: %s holds something too short to be a token; remove it and one will be made", path)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("crm: %w", err)
	}
	token := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("crm: %w", err)
	}
	return token, nil
}

// --- placing a call ---

// CallRequest asks for a call on a rendered template.
type CallRequest struct {
	Template string          `json:"template"`
	Version  int             `json:"version"`
	Vars     json.RawMessage `json:"vars"`
	// Greeting is the opening line. Left empty it is the variable "start",
	// which is what such templates quote as the line the call opened with.
	Greeting string `json:"greeting"`
	Lead     string `json:"lead"`
	// DryRun renders and checks everything and stops short of dialling.
	DryRun bool `json:"dry_run"`
}

// CallResult is what came of it.
type CallResult struct {
	OK     bool   `json:"ok"`
	DryRun bool   `json:"dry_run,omitempty"`
	CallID string `json:"call_id,omitempty"`
	// Line says where the call went: the originator dials one locked number
	// whatever lead a call is for.
	Line     string    `json:"line"`
	Greeting string    `json:"greeting"`
	Render   *Rendered `json:"render"`
}

// apiError is a failure with the status it should be answered with.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func fail(status int, format string, args ...any) *apiError {
	return &apiError{status: status, msg: fmt.Sprintf(format, args...)}
}

var digits = regexp.MustCompile(`\+?\d{7,}`)

// hideNumbers takes anything that could be a phone number out of text that
// is about to be logged or returned.
func hideNumbers(s string) string { return digits.ReplaceAllString(s, "<number>") }

// Call renders the template and places the call.
func (s *Service) Call(ctx context.Context, req CallRequest, by string) (*CallResult, error) {
	vars, err := decodeVars(req.Vars)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	now := time.Now().In(s.cfg.Location)
	r, err := s.prompts.RenderWith(req.Template, req.Version, vars, now, s.defaults())
	if err != nil {
		return nil, renderFailure(err)
	}
	greeting := req.Greeting
	if greeting == "" {
		if start, ok := vars["start"].(string); ok {
			greeting = start
		} else {
			greeting = s.Greeting()
		}
	}
	if greeting == "" {
		return nil, fail(http.StatusUnprocessableEntity, "no opening line: give greeting or start, or set the default with PUT /api/greeting")
	}
	res := &CallResult{Line: "the originator's locked line", Greeting: greeting, Render: r, DryRun: req.DryRun}
	if strings.TrimSpace(r.Instructions) == "" {
		return nil, fail(http.StatusUnprocessableEntity, "the template rendered to nothing")
	}
	if req.DryRun {
		res.OK = true
		return res, nil
	}

	// One call at a time: there is one line.
	s.mu.Lock()
	if s.dialing {
		s.mu.Unlock()
		return nil, fail(http.StatusConflict, "another call is being placed")
	}
	s.dialing = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.dialing = false; s.mu.Unlock() }()

	if busy, err := s.lineBusy(ctx); err != nil {
		return nil, fail(http.StatusServiceUnavailable, "golive on the phone line is not answering: %v", err)
	} else if busy {
		return nil, fail(http.StatusConflict, "a call is in progress on the line")
	}
	if err := s.prompts.Keep(r); err != nil {
		return nil, err
	}
	rec := CallRecord{At: now, Template: r.Template, Version: r.Version, Render: r.SHA, Chars: r.Chars,
		Vars: r.Supplied, Undefined: r.Undefined, GreetingChars: len([]rune(greeting)), Lead: req.Lead}
	callID, dialErr := s.dial(ctx, r.Instructions, greeting)
	rec.CallID, rec.OK = callID, dialErr == nil
	if dialErr != nil {
		rec.Error = hideNumbers(dialErr.Error())
	}
	if _, err := s.events.Append(Event{At: now, By: by, Type: "call.placed", Ref: callID, Lead: req.Lead, Call: &rec}); err != nil {
		// The call may be ringing; say so rather than hide it behind the
		// failure to write it down.
		s.log.Error("crm: a call was placed and could not be recorded", "call", callID, "err", err)
		if dialErr == nil {
			return nil, fmt.Errorf("the call %s was placed, and recording it failed: %w", callID, err)
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, rec)
	s.mu.Unlock()
	if dialErr != nil {
		s.log.Warn("crm: the originator did not place the call", "template", r.Template, "version", r.Version, "render", r.SHA, "err", rec.Error)
		return nil, fail(http.StatusBadGateway, "the originator did not place the call: %s", rec.Error)
	}
	s.log.Info("crm: call placed", "call", callID, "template", r.Template, "version", r.Version, "render", r.SHA,
		"chars", r.Chars, "vars", len(r.Supplied), "undefined", len(r.Undefined), "by", by)
	res.OK, res.CallID = true, callID
	return res, nil
}

func renderFailure(err error) error {
	var je *jinja.Error
	switch {
	case errors.Is(err, ErrNotFound):
		return fail(http.StatusNotFound, "no such template or version")
	case errors.As(err, &je):
		return fail(http.StatusUnprocessableEntity, "%v", err)
	}
	return err
}

// lineBusy asks golive whether a call is in progress.
func (s *Service) lineBusy(ctx context.Context) (bool, error) {
	if s.cfg.GoliveURL == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.cfg.GoliveURL, "/")+"/healthz", nil)
	if err != nil {
		return false, err
	}
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var health struct {
		Sessions int    `json:"sessions"`
		Status   string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&health); err != nil || resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("its health check answered %d", resp.StatusCode)
	}
	return health.Sessions > 0, nil
}

// lockedNumber reads the one number the originator will dial from its env
// file. It is returned to be sent and for nothing else.
func (s *Service) lockedNumber() (string, error) {
	data, err := os.ReadFile(s.cfg.OriginatorEnv)
	if err != nil {
		return "", fmt.Errorf("cannot read the originator's env file: %v", errors.Unwrap(err))
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ALLOWED_CALLEE="); ok {
			if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
				return v, nil
			}
		}
	}
	return "", errors.New("the originator's env file names no number to dial (ALLOWED_CALLEE)")
}

// dial hands the prompt and the opening line to the originator.
func (s *Service) dial(ctx context.Context, instructions, greeting string) (string, error) {
	to, err := s.lockedNumber()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{"to": to, "instructions": instructions, "greeting": greeting})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.OriginatorURL, "/")+"/api/outbound/dial", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("the originator did not answer: %v", err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var answer struct {
		OK     bool   `json:"ok"`
		CallID string `json:"call_id"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(text, &answer)
	if resp.StatusCode != http.StatusOK || !answer.OK || answer.CallID == "" {
		msg := answer.Error
		if msg == "" {
			msg = strings.TrimSpace(string(text))
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("it answered %d: %s", resp.StatusCode, msg)
	}
	return answer.CallID, nil
}

// Calls is the calls placed, newest first, at most n of them.
func (s *Service) Calls(n int) []CallRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CallRecord, 0, n)
	for i := len(s.calls) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, s.calls[i])
	}
	return out
}

// --- HTTP ---

//go:embed ui
var uiFiles embed.FS

// Handler is the service's API. Every request but the health check must
// carry the token: Authorization: Bearer <token>.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "events": s.events.Seq(), "templates": len(s.prompts.List())})
	})
	mux.HandleFunc("GET /api/templates", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, http.StatusOK, map[string]any{"templates": s.prompts.List()})
		return nil
	}))
	mux.HandleFunc("GET /api/templates/{name}", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		version, _ := strconv.Atoi(r.URL.Query().Get("version"))
		t, err := s.prompts.Get(r.PathValue("name"), version)
		if err != nil {
			return renderFailure(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"template": t, "versions": s.prompts.Versions(t.Name)})
		return nil
	}))
	mux.HandleFunc("PUT /api/templates/{name}", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			return fail(http.StatusBadRequest, "the template could not be read")
		}
		body, note := string(raw), r.URL.Query().Get("note")
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var in struct {
				Body string `json:"body"`
				Note string `json:"note"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return fail(http.StatusBadRequest, "send the template as text, or as JSON with a body")
			}
			body, note = in.Body, in.Note
		}
		t, err := s.prompts.Save(r.PathValue("name"), body, note, who(r))
		if err != nil {
			var je *jinja.Error
			if errors.As(err, &je) || strings.HasPrefix(err.Error(), "crm: a template") || strings.HasPrefix(err.Error(), "crm: the template") {
				return fail(http.StatusUnprocessableEntity, "%v", err)
			}
			return err
		}
		s.log.Info("crm: template saved", "template", t.Name, "version", t.Version, "sha", t.SHA, "chars", t.Chars, "by", who(r))
		writeJSON(w, http.StatusOK, map[string]any{"template": t})
		return nil
	}))
	mux.HandleFunc("GET /api/configs", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, http.StatusOK, map[string]any{"configs": s.configs.List()})
		return nil
	}))
	mux.HandleFunc("GET /api/configs/{name}", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		version, _ := strconv.Atoi(r.URL.Query().Get("version"))
		v, err := s.configs.Get(r.PathValue("name"), version)
		if err != nil {
			return renderFailure(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": v})
		return nil
	}))
	mux.HandleFunc("PUT /api/configs/{name}", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			return fail(http.StatusBadRequest, "the config could not be read")
		}
		if r.PathValue("name") == QuoteRulesName {
			var rules QuoteRules
			if err := json.Unmarshal(raw, &rules); err != nil || len(rules.Coverages) == 0 {
				return fail(http.StatusUnprocessableEntity, "the quote rules are a JSON object with coverages; see internal/crm/quote.go")
			}
		}
		if r.PathValue("name") == QuoteAPIName {
			var api QuoteAPI
			if err := json.Unmarshal(raw, &api); err != nil || len(api.Coverages) == 0 {
				return fail(http.StatusUnprocessableEntity, "the quote-api config is a JSON object with base_url and coverages; see internal/crm/quoteapi.go")
			}
		}
		v, err := s.configs.Save(r.PathValue("name"), raw, r.URL.Query().Get("note"), who(r))
		if err != nil {
			if strings.HasPrefix(err.Error(), "crm: a config") || strings.HasPrefix(err.Error(), "crm: the config") {
				return fail(http.StatusUnprocessableEntity, "%v", err)
			}
			return err
		}
		s.log.Info("crm: config saved", "config", v.Name, "version", v.Version, "sha", v.SHA, "by", who(r))
		writeJSON(w, http.StatusOK, map[string]any{"config": v})
		return nil
	}))
	mux.HandleFunc("POST /api/tools/quote", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			return fail(http.StatusBadRequest, "the request could not be read")
		}
		req, err := quoteRequestFrom(raw)
		if err != nil {
			return fail(http.StatusBadRequest, "%v", err)
		}
		q, err := s.Quote(req)
		if err != nil {
			return fail(http.StatusUnprocessableEntity, "%v", err)
		}
		writeJSON(w, http.StatusOK, q)
		return nil
	}))
	mux.HandleFunc("POST /api/render", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		var req CallRequest
		if err := readJSON(r, &req); err != nil {
			return err
		}
		vars, err := decodeVars(req.Vars)
		if err != nil {
			return fail(http.StatusBadRequest, "%v", err)
		}
		out, err := s.prompts.RenderWith(req.Template, req.Version, vars, time.Now().In(s.cfg.Location), s.defaults())
		if err != nil {
			return renderFailure(err)
		}
		writeJSON(w, http.StatusOK, out)
		return nil
	}))
	mux.HandleFunc("POST /api/call", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		var req CallRequest
		if err := readJSON(r, &req); err != nil {
			return err
		}
		res, err := s.Call(r.Context(), req, who(r))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, res)
		return nil
	}))
	mux.HandleFunc("GET /api/greeting", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, http.StatusOK, map[string]any{"greeting": s.Greeting()})
		return nil
	}))
	mux.HandleFunc("PUT /api/greeting", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			return fail(http.StatusBadRequest, "the line could not be read")
		}
		line := string(raw)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var in struct {
				Greeting string `json:"greeting"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return fail(http.StatusBadRequest, "send the line as text, or as JSON with greeting")
			}
			line = in.Greeting
		}
		if err := s.SetGreeting(line, who(r)); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"greeting": s.Greeting()})
		return nil
	}))
	mux.HandleFunc("GET /api/calls", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 || n > 500 {
			n = 50
		}
		writeJSON(w, http.StatusOK, map[string]any{"calls": s.Calls(n)})
		return nil
	}))
	mux.HandleFunc("GET /api/renders/{sha}", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		text, err := s.prompts.Kept(r.PathValue("sha"))
		if err != nil {
			return fail(http.StatusNotFound, "no such rendered prompt")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, text)
		return nil
	}))
	// The panel: a page over this API, served from the copy built in. The
	// files are public; everything they ask for carries the token.
	if sub, err := fs.Sub(uiFiles, "ui"); err == nil {
		mux.Handle("GET /", http.FileServer(http.FS(sub)))
	}
	return s.logged(mux)
}

// who names the caller of a request for the audit trail: what it says it is,
// which is all there is to go on while there is one token.
func who(r *http.Request) string {
	if name := strings.TrimSpace(r.Header.Get("X-Operator")); name != "" && len(name) <= 64 {
		return name
	}
	return "api"
}

func (s *Service) authed(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "send the token: Authorization: Bearer <token>"})
			return
		}
		if err := h(w, r); err != nil {
			var ae *apiError
			if errors.As(err, &ae) {
				writeJSON(w, ae.status, map[string]string{"error": ae.msg})
				return
			}
			s.log.Error("crm: request failed", "path", r.URL.Path, "err", hideNumbers(err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": hideNumbers(err.Error())})
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// logged writes one line a request: what was asked and how it went, never
// what was in it.
func (s *Service) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		began := time.Now()
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" {
			s.log.Info("crm: request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(began).Milliseconds(), "from", r.RemoteAddr, "by", who(r))
		}
	})
}

func readJSON(r *http.Request, into any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(into); err != nil {
		return fail(http.StatusBadRequest, "the request is not the JSON this expects")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
