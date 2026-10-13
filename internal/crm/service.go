package crm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	token   string

	mu      sync.Mutex
	calls   []CallRecord // newest last
	dialing bool
}

const tokenFile = "api.token"

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
	events, torn, err := OpenLog(cfg.Dir, func(ev Event) error {
		s.prompts.apply(ev)
		if ev.Type == "call.placed" && ev.Call != nil {
			s.calls = append(s.calls, *ev.Call)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.events, s.prompts.log = events, events
	if torn > 0 {
		s.log.Warn("crm: the log ended in a line that was never finished; it was cut off", "bytes", torn)
	}
	if s.token, err = loadToken(filepath.Join(cfg.Dir, tokenFile)); err != nil {
		events.Close()
		return nil, err
	}
	s.log.Info("crm: open", "dir", cfg.Dir, "events", events.Seq(), "templates", len(s.prompts.templates), "calls", len(s.calls))
	return s, nil
}

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
	r, err := s.prompts.Render(req.Template, req.Version, vars, now)
	if err != nil {
		return nil, renderFailure(err)
	}
	greeting := req.Greeting
	if greeting == "" {
		if start, ok := vars["start"].(string); ok {
			greeting = start
		}
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
	mux.HandleFunc("POST /api/render", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		var req CallRequest
		if err := readJSON(r, &req); err != nil {
			return err
		}
		vars, err := decodeVars(req.Vars)
		if err != nil {
			return fail(http.StatusBadRequest, "%v", err)
		}
		out, err := s.prompts.Render(req.Template, req.Version, vars, time.Now().In(s.cfg.Location))
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
