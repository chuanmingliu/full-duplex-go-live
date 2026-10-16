// Package desk is where the two coding agents working on golive — Claude and
// Codex — leave each other messages, tasks and results, with the owner
// watching and able to stop them.
//
// It is deliberately small: an append-only, checksummed log of notes; one
// token per participant, so a note's author is known and not claimed; a
// control switch only the owner can throw (paused: the agents may read but
// not write); and a page over all of it that updates as notes arrive. The
// agents reach it with curl from wherever they run; the owner reads the
// page. Nothing here runs an agent or places a call: when the two are later
// made to take turns on a schedule, this is the record they will take turns
// in, and the pause is the owner's hand on it.
package desk

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Participants. The owner is a person; the other two are the agents.
const (
	Owner  = "owner"
	Claude = "claude"
	Codex  = "codex"
	All    = "all"
)

var participants = []string{Owner, Claude, Codex}

// Kinds of note.
const (
	Message  = "message"  // something said
	Task     = "task"     // something asked of the addressee; stays open until done
	Question = "question" // something asked that needs an answer; open until done
	Result   = "result"   // what came of a task
	Status   = "status"   // where the author stands (a build, a call, a deploy)
)

var kinds = map[string]bool{Message: true, Task: true, Question: true, Result: true, Status: true}

// Note is one entry in the log. Seq and Chain make the log tamper-evident
// (see Log); the rest is what was said.
type Note struct {
	Seq     int64     `json:"seq"`
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Kind    string    `json:"kind"`
	Type    string    `json:"type"` // note | done | control
	Thread  string    `json:"thread,omitempty"`
	ReplyTo string    `json:"reply_to,omitempty"`
	Subject string    `json:"subject,omitempty"`
	Body    string    `json:"body,omitempty"`
	Refs    []string  `json:"refs,omitempty"`
	// For Type done: the note it closes. For Type control: the switch.
	Of     string `json:"of,omitempty"`
	Paused *bool  `json:"paused,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Chain  string `json:"chain"`
}

// View is a note as the API shows it: the note, and whether it is still open.
type View struct {
	Note
	Open   bool       `json:"open"`
	DoneBy string     `json:"done_by,omitempty"`
	DoneAt *time.Time `json:"done_at,omitempty"`
}

// Control is the owner's switches. Paused refuses the agents' writes. Mode is
// written down for the scheduler that does not exist yet: manual means the
// agents act when told; auto will mean they take turns by themselves. Nothing
// reads Mode today but the page.
type Control struct {
	Paused bool      `json:"paused"`
	Mode   string    `json:"mode"`
	By     string    `json:"by,omitempty"`
	At     time.Time `json:"at,omitempty"`
}

// Config is what the service needs to run.
type Config struct {
	Dir string // the log and the tokens; the owner's alone
	Log *slog.Logger
	Now func() time.Time
}

// Service is the desk.
type Service struct {
	cfg    Config
	log    *slog.Logger
	tokens map[string]string // participant → token
	events *Log

	mu      sync.RWMutex
	notes   []Note
	byID    map[string]int
	done    map[string]Note // note id → the done entry
	control Control
	seen    map[string]time.Time
	subs    map[chan Note]struct{}
}

// Open reads the log and the tokens, making tokens that are missing.
func Open(cfg Config) (*Service, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("desk: %w", err)
	}
	s := &Service{cfg: cfg, log: cfg.Log, tokens: map[string]string{}, byID: map[string]int{}, done: map[string]Note{},
		control: Control{Mode: "manual"}, seen: map[string]time.Time{}, subs: map[chan Note]struct{}{}}
	for _, p := range participants {
		t, err := loadToken(filepath.Join(cfg.Dir, p+".token"))
		if err != nil {
			return nil, err
		}
		s.tokens[p] = t
	}
	events, torn, err := OpenLog(cfg.Dir, cfg.Now, s.replay)
	if err != nil {
		return nil, err
	}
	s.events = events
	if torn > 0 {
		s.log.Warn("desk: cut a half-written line off the log", "bytes", torn)
	}
	return s, nil
}

// Close releases the log.
func (s *Service) Close() error { return s.events.Close() }

func (s *Service) replay(n Note) error {
	switch n.Type {
	case "note":
		s.byID[n.ID] = len(s.notes)
		s.notes = append(s.notes, n)
	case "done":
		s.done[n.Of] = n
	case "control":
		if n.Paused != nil {
			s.control.Paused = *n.Paused
		}
		if n.Mode != "" {
			s.control.Mode = n.Mode
		}
		s.control.By, s.control.At = n.From, n.At
	default:
		return fmt.Errorf("an entry of a type this does not know: %q", n.Type)
	}
	return nil
}

// loadToken reads a participant's token, making one the first time. It is a
// secret: the file is the owner's alone and is never printed.
func loadToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(data)); len(t) >= 32 {
			return t, nil
		}
		return "", fmt.Errorf("desk: %s holds something too short to be a token; remove it and one will be made", path)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	t := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("desk: %w", err)
	}
	return t, nil
}

// whoIs names the holder of a token, or "".
func (s *Service) whoIs(token string) string {
	for _, p := range participants {
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.tokens[p])) == 1 {
			return p
		}
	}
	return ""
}

// Post writes a note from one participant. The agents cannot post while the
// desk is paused; the owner always can.
func (s *Service) Post(from string, in Note) (View, error) {
	in.From = from
	in.Type = "note"
	if in.To == "" {
		in.To = All
	}
	if in.To != All && !isParticipant(in.To) {
		return View{}, fail(http.StatusBadRequest, "to must be one of owner, claude, codex or all")
	}
	if in.To == from {
		return View{}, fail(http.StatusBadRequest, "a note to oneself")
	}
	if in.Kind == "" {
		in.Kind = Message
	}
	if !kinds[in.Kind] {
		return View{}, fail(http.StatusBadRequest, "kind must be message, task, question, result or status")
	}
	in.Subject = strings.TrimSpace(in.Subject)
	in.Body = strings.TrimRight(in.Body, "\n")
	if in.Subject == "" && in.Body == "" {
		return View{}, fail(http.StatusBadRequest, "an empty note")
	}
	if len(in.Subject) > 200 || len(in.Body) > 32<<10 || len(in.Refs) > 20 {
		return View{}, fail(http.StatusBadRequest, "too long: subject 200 characters, body 32 KB, 20 refs")
	}
	for i, r := range in.Refs {
		in.Refs[i] = strings.TrimSpace(r)
		if len(in.Refs[i]) > 300 {
			return View{}, fail(http.StatusBadRequest, "a ref over 300 characters")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.control.Paused && from != Owner {
		return View{}, fail(http.StatusLocked, "the desk is paused by the owner; read, do not write")
	}
	if in.ReplyTo != "" {
		i, ok := s.byID[in.ReplyTo]
		if !ok {
			return View{}, fail(http.StatusBadRequest, "reply_to names a note that is not here")
		}
		in.Thread = s.notes[i].Thread
	}
	n, err := s.events.Append(in, func(seq int64) string { return "n" + strconv.FormatInt(seq, 10) })
	if err != nil {
		return View{}, err
	}
	if n.Thread == "" {
		// A thread is named after its first note; that cannot be known
		// before the sequence number is, so it is filled in on reading.
		n.Thread = n.ID
	}
	s.byID[n.ID] = len(s.notes)
	s.notes = append(s.notes, n)
	s.publish(n)
	return s.view(n), nil
}

// Done closes a task or a question. The addressee, the author or the owner
// may; anyone else may not.
func (s *Service) Done(by, id string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.byID[id]
	if !ok {
		return View{}, fail(http.StatusNotFound, "no such note")
	}
	n := s.notes[i]
	if _, closed := s.done[id]; closed {
		return s.view(n), nil
	}
	if by != Owner && by != n.From && n.To != by && n.To != All {
		return View{}, fail(http.StatusForbidden, "not yours to close")
	}
	if s.control.Paused && by != Owner {
		return View{}, fail(http.StatusLocked, "the desk is paused by the owner; read, do not write")
	}
	d, err := s.events.Append(Note{Type: "done", From: by, Of: id, To: n.From}, func(seq int64) string { return "d" + strconv.FormatInt(seq, 10) })
	if err != nil {
		return View{}, err
	}
	s.done[id] = d
	s.publish(d)
	return s.view(n), nil
}

// SetControl is the owner's. Mode is kept, not acted on.
func (s *Service) SetControl(by string, paused *bool, mode string) (Control, error) {
	if by != Owner {
		return Control{}, fail(http.StatusForbidden, "only the owner throws the switches")
	}
	if mode != "" && mode != "manual" && mode != "auto" {
		return Control{}, fail(http.StatusBadRequest, "mode is manual or auto")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.events.Append(Note{Type: "control", From: by, To: All, Paused: paused, Mode: mode}, func(seq int64) string { return "c" + strconv.FormatInt(seq, 10) })
	if err != nil {
		return Control{}, err
	}
	_ = s.replay(c)
	s.publish(c)
	return s.control, nil
}

// Control is the switches as they stand.
func (s *Service) Control() Control {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.control
}

// Query selects notes.
type Query struct {
	After  int64 // notes with seq > After
	Thread string
	To     string // notes addressed to this participant or to all
	From   string
	Kind   string
	Open   bool // only tasks and questions still open
	Limit  int
}

// Notes lists notes, oldest first.
func (s *Service) Notes(q Query) []View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []View
	for _, n := range s.notes {
		if n.Seq <= q.After || (q.Thread != "" && s.threadOf(n) != q.Thread) || (q.From != "" && n.From != q.From) ||
			(q.Kind != "" && n.Kind != q.Kind) || (q.To != "" && n.To != q.To && n.To != All) {
			continue
		}
		v := s.view(n)
		if q.Open && !v.Open {
			continue
		}
		out = append(out, v)
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return out
}

func (s *Service) threadOf(n Note) string {
	if n.Thread != "" {
		return n.Thread
	}
	return n.ID
}

func (s *Service) view(n Note) View {
	n.Thread = s.threadOf(n)
	v := View{Note: n}
	if n.Kind == Task || n.Kind == Question {
		if d, ok := s.done[n.ID]; ok {
			at := d.At
			v.DoneBy, v.DoneAt = d.From, &at
		} else {
			v.Open = true
		}
	}
	return v
}

// State is what the page shows at the top: who was last heard from and
// when, what is open for whom, and the switches.
type State struct {
	Control Control              `json:"control"`
	Seen    map[string]time.Time `json:"seen"`
	Open    map[string]int       `json:"open"`
	Notes   int64                `json:"notes"`
	Seq     int64                `json:"seq"`
}

func (s *Service) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := State{Control: s.control, Seen: map[string]time.Time{}, Open: map[string]int{}, Notes: int64(len(s.notes)), Seq: s.events.Seq()}
	for p, t := range s.seen {
		st.Seen[p] = t
	}
	for _, n := range s.notes {
		if v := s.view(n); v.Open {
			st.Open[n.To]++
		}
	}
	return st
}

func (s *Service) touch(p string) {
	s.mu.Lock()
	s.seen[p] = s.cfg.Now()
	s.mu.Unlock()
}

func (s *Service) publish(n Note) {
	for ch := range s.subs {
		select {
		case ch <- n:
		default:
		}
	}
}

func (s *Service) subscribe() (chan Note, func()) {
	ch := make(chan Note, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

func isParticipant(p string) bool {
	for _, q := range participants {
		if p == q {
			return true
		}
	}
	return false
}

// ---- HTTP

//go:embed ui
var uiFiles embed.FS

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }
func fail(status int, format string, args ...any) *apiError {
	return &apiError{status: status, msg: fmt.Sprintf(format, args...)}
}

// Handler is the API and the page. Every request but the health check and
// the page's files carries a participant's token: Authorization: Bearer.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		st := s.State()
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "notes": st.Notes, "paused": st.Control.Paused, "mode": st.Control.Mode})
	})
	mux.HandleFunc("GET /api/me", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		writeJSON(w, http.StatusOK, map[string]any{"who": who, "control": s.Control()})
		return nil
	}))
	mux.HandleFunc("GET /api/state", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		writeJSON(w, http.StatusOK, s.State())
		return nil
	}))
	mux.HandleFunc("GET /api/notes", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		q := r.URL.Query()
		after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		to := q.Get("to")
		if to == "me" {
			to = who
		}
		writeJSON(w, http.StatusOK, map[string]any{"notes": s.Notes(Query{After: after, Thread: q.Get("thread"), To: to, From: q.Get("from"),
			Kind: q.Get("kind"), Open: q.Get("open") == "1", Limit: limit}), "seq": s.events.Seq()})
		return nil
	}))
	mux.HandleFunc("POST /api/notes", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		var in Note
		if err := readJSON(r, &in); err != nil {
			return err
		}
		v, err := s.Post(who, in)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, v)
		return nil
	}))
	mux.HandleFunc("POST /api/notes/{id}/done", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		v, err := s.Done(who, r.PathValue("id"))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, v)
		return nil
	}))
	mux.HandleFunc("PUT /api/control", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		var in struct {
			Paused *bool  `json:"paused"`
			Mode   string `json:"mode"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		c, err := s.SetControl(who, in.Paused, in.Mode)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, c)
		return nil
	}))
	// A stream of everything written from now on, as server-sent events:
	// the page listens; an agent may too.
	mux.HandleFunc("GET /api/events", s.authed(func(w http.ResponseWriter, r *http.Request, who string) error {
		fl, ok := w.(http.Flusher)
		if !ok {
			return fail(http.StatusNotImplemented, "streaming is not possible here")
		}
		ch, stop := s.subscribe()
		defer stop()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: hello\ndata: "+strconv.FormatInt(s.events.Seq(), 10)+"\n\n")
		fl.Flush()
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return nil
			case <-ping.C:
				_, _ = io.WriteString(w, ": ping\n\n")
				fl.Flush()
			case n := <-ch:
				var payload any = n
				if n.Type == "note" {
					s.mu.RLock()
					payload = s.view(n)
					s.mu.RUnlock()
				}
				b, _ := json.Marshal(payload)
				_, _ = io.WriteString(w, "event: "+n.Type+"\ndata: "+string(b)+"\n\n")
				fl.Flush()
			}
		}
	}))
	if sub, err := fs.Sub(uiFiles, "ui"); err == nil {
		mux.Handle("GET /", http.FileServer(http.FS(sub)))
	}
	return s.logged(mux)
}

func (s *Service) authed(h func(http.ResponseWriter, *http.Request, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		who := ""
		if ok {
			who = s.whoIs(strings.TrimSpace(got))
		}
		if who == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "send your token: Authorization: Bearer <token>"})
			return
		}
		s.touch(who)
		if err := h(w, r, who); err != nil {
			var ae *apiError
			if errors.As(err, &ae) {
				writeJSON(w, ae.status, map[string]string{"error": ae.msg})
				return
			}
			s.log.Error("desk: request failed", "path", r.URL.Path, "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the desk could not do that; see its log"})
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logged writes one line a request: what was asked and how it went, never
// what was in it.
func (s *Service) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		began := time.Now()
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" && r.URL.Path != "/api/events" {
			s.log.Info("desk: request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(began).Milliseconds(), "from", r.RemoteAddr)
		}
	})
}

func readJSON(r *http.Request, into any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(into); err != nil {
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

// ---- The log: one entry a line, each line's checksum covering every line
// before it, so a line changed by hand breaks the chain from there on.

const logFile = "notes.jsonl"

// Log is the append-only record.
type Log struct {
	path  string
	mu    sync.Mutex
	f     *os.File
	seq   int64
	chain string
	now   func() time.Time
}

// OpenLog opens, checks and replays the log; torn is the length of a
// half-written last line it cut off.
func OpenLog(dir string, now func() time.Time, replay func(Note) error) (*Log, int, error) {
	path := filepath.Join(dir, logFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, 0, fmt.Errorf("desk: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("desk: %s is in use by another process", path)
	}
	l := &Log{path: path, f: f, now: now}
	good, torn, err := l.read(replay)
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if torn > 0 {
		if err := f.Truncate(good); err != nil {
			f.Close()
			return nil, 0, fmt.Errorf("desk: %w", err)
		}
	}
	if _, err := f.Seek(good, 0); err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("desk: %w", err)
	}
	return l, torn, nil
}

func (l *Log) read(replay func(Note) error) (good int64, torn int, err error) {
	if _, err := l.f.Seek(0, 0); err != nil {
		return 0, 0, err
	}
	info, err := l.f.Stat()
	if err != nil {
		return 0, 0, err
	}
	r := bufio.NewReaderSize(l.f, 1<<20)
	for n := 1; ; n++ {
		line, rerr := r.ReadBytes('\n')
		if len(line) == 0 && rerr != nil {
			break
		}
		last := good+int64(len(line)) >= info.Size()
		ev, sum, perr := checkLine(line, l.chain)
		if perr != nil || rerr != nil || ev.Seq != l.seq+1 {
			if last && rerr != nil {
				return good, len(line), nil
			}
			if perr == nil {
				perr = fmt.Errorf("it is numbered %d and should be %d", ev.Seq, l.seq+1)
			}
			return 0, 0, fmt.Errorf("desk: %s line %d does not follow from the lines before it (%v): the log has been changed or damaged", l.path, n, perr)
		}
		if replay != nil {
			if err := replay(ev); err != nil {
				return 0, 0, fmt.Errorf("desk: %s line %d: %w", l.path, n, err)
			}
		}
		l.seq, l.chain = ev.Seq, sum
		good += int64(len(line))
	}
	return good, 0, nil
}

func checkLine(line []byte, prev string) (Note, string, error) {
	var n Note
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return n, "", errors.New("it is not a whole line")
	}
	if err := json.Unmarshal(line, &n); err != nil {
		return n, "", errors.New("it is not an entry")
	}
	claimed := n.Chain
	n.Chain = ""
	body, err := json.Marshal(n)
	if err != nil {
		return n, "", err
	}
	if sum := chainSum(prev, body); sum != claimed {
		return n, "", errors.New("its checksum is wrong")
	}
	n.Chain = claimed
	return n, claimed, nil
}

func chainSum(prev string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// Append numbers, names, stamps, checksums and writes an entry.
func (l *Log) Append(n Note, name func(seq int64) string) (Note, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return n, errors.New("desk: the log is closed")
	}
	n.Seq = l.seq + 1
	n.ID = name(n.Seq)
	n.At = l.now().Round(time.Millisecond)
	n.Chain = ""
	body, err := json.Marshal(n)
	if err != nil {
		return n, err
	}
	var back Note
	if err := json.Unmarshal(body, &back); err != nil {
		return n, err
	}
	if body, err = json.Marshal(back); err != nil {
		return n, err
	}
	n = back
	n.Chain = chainSum(l.chain, body)
	line, err := json.Marshal(n)
	if err != nil {
		return n, err
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return n, fmt.Errorf("desk: writing the log: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return n, fmt.Errorf("desk: writing the log: %w", err)
	}
	l.seq, l.chain = n.Seq, n.Chain
	return n, nil
}

// Seq is the number of the last entry.
func (l *Log) Seq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// Close releases the file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Verify reads a log without opening it for writing.
func Verify(dir string) (int64, error) {
	f, err := os.Open(filepath.Join(dir, logFile))
	if err != nil {
		return 0, fmt.Errorf("desk: %w", err)
	}
	defer f.Close()
	l := &Log{path: f.Name(), f: f}
	_, torn, err := l.read(nil)
	if err != nil {
		return l.seq, err
	}
	if torn > 0 {
		return l.seq, fmt.Errorf("desk: %s ends in %d bytes of a line that was never finished", l.path, torn)
	}
	return l.seq, nil
}

// Serve runs the handler on each address until ctx ends.
func Serve(ctx context.Context, s *Service, addrs []string) error {
	errc := make(chan error, len(addrs))
	var servers []*http.Server
	for _, a := range addrs {
		srv := &http.Server{Addr: a, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv)
		go func() { errc <- srv.ListenAndServe() }()
	}
	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shut)
	}
	return nil
}

// Participants lists who may hold a token, sorted.
func Participants() []string {
	out := append([]string(nil), participants...)
	sort.Strings(out)
	return out
}
