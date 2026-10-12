package journal

import (
	"embed"
	"encoding/json"
	"errors"
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
)

//go:embed ui
var uiFiles embed.FS

// Config says where the journal reads from and where it keeps what is its own.
type Config struct {
	// LogPath is golive's log. StoreDir is where golive keeps recorded calls
	// (store_dir); empty when calls are not recorded.
	LogPath  string
	StoreDir string
	// DataDir is the journal's own: each call as last read (so a call outlives
	// the log it was read from) and the owner's notes.
	DataDir string
	// NotesDir holds what is written about the calls rather than measured:
	// retros/<call id>.md, issues.json, decisions.json. It ships with golive's
	// bundle, so it is versioned with the code it describes.
	NotesDir string
	// RepoURL turns a commit hash into a link.
	RepoURL string
	// RetentionHours is how long golive keeps a recorded call, for the page to
	// say; the journal does not delete anything.
	RetentionHours int
	// UIDir serves the page from a directory instead of the copy built into
	// the binary, for working on it.
	UIDir string
	Log   *slog.Logger
}

// Server serves the journal.
type Server struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	calls    []*Call
	byID     map[string]*Call
	logSize  int64
	logMod   time.Time
	checked  time.Time
	logBuild string
}

// NewServer builds a journal over cfg. Nothing is read until it is asked for.
func NewServer(cfg Config) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Server{cfg: cfg, log: cfg.Log, byID: map[string]*Call{}}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// refresh re-reads the log when it has grown. The whole log is read each time:
// it is tens of megabytes at most, a call ends a few times an hour, and a
// reader that keeps no position has no position to get wrong.
func (s *Server) refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.checked) < 2*time.Second {
		return
	}
	s.checked = time.Now()

	if s.calls == nil {
		s.loadKept()
	}
	info, err := os.Stat(s.cfg.LogPath)
	if err != nil {
		return
	}
	if info.Size() == s.logSize && info.ModTime().Equal(s.logMod) {
		return
	}
	f, err := os.Open(s.cfg.LogPath)
	if err != nil {
		return
	}
	defer f.Close()
	ix := NewIndexer()
	if err := ix.ReadLog(f); err != nil {
		s.log.Warn("journal: the log could not be read to its end", "err", err)
	}
	s.logSize, s.logMod, s.logBuild = info.Size(), info.ModTime(), ix.build

	seen := map[string]bool{}
	merged := ix.Calls()
	for _, c := range merged {
		seen[c.ID] = true
		s.keep(c)
	}
	// Calls the log no longer holds — it was rotated, or lost — stay as they
	// were last read.
	for _, c := range s.calls {
		if !seen[c.ID] {
			merged = append(merged, c)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].StartedAt.After(merged[j].StartedAt) })
	s.calls = merged
	s.byID = make(map[string]*Call, len(merged))
	for _, c := range merged {
		s.byID[c.ID] = c
	}
}

func (s *Server) keptDir() string { return filepath.Join(s.cfg.DataDir, "calls") }

func (s *Server) loadKept() {
	s.calls = []*Call{}
	entries, _ := os.ReadDir(s.keptDir())
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.keptDir(), e.Name()))
		if err != nil {
			continue
		}
		var c Call
		if json.Unmarshal(data, &c) == nil && c.ID != "" {
			s.calls = append(s.calls, &c)
			s.byID[c.ID] = &c
		}
	}
}

// keep writes a finished call down, once it has ended and when it has changed.
func (s *Server) keep(c *Call) {
	if s.cfg.DataDir == "" || c.EndedAt.IsZero() || !idPattern.MatchString(c.ID) {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	path := filepath.Join(s.keptDir(), c.ID+".json")
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return
	}
	if err := os.MkdirAll(s.keptDir(), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// --- what is written about a call ---

// Note is a piece of writing with its date.
type Note struct {
	Text      string    `json:"text"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Server) retro(id string) *Note {
	if s.cfg.NotesDir == "" {
		return nil
	}
	path := filepath.Join(s.cfg.NotesDir, "retros", id+".md")
	data, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	n := &Note{Text: string(data)}
	if info, err := os.Stat(path); err == nil {
		n.UpdatedAt = info.ModTime()
	}
	return n
}

// Feedback is the owner's own account of a call: the one measurement no log
// has. Whether an answer sounded right is not in any of the numbers here.
type Feedback struct {
	Rating    int       `json:"rating"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Server) feedbackPath(id string) string {
	return filepath.Join(s.cfg.DataDir, "feedback", id+".json")
}

func (s *Server) feedback(id string) *Feedback {
	data, err := os.ReadFile(s.feedbackPath(id))
	if err != nil {
		return nil
	}
	var f Feedback
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return &f
}

func (s *Server) audioOf(c *Call) (available bool, seconds float64, rate int) {
	rec, err := OpenRecording(s.cfg.StoreDir, c.Session)
	if err != nil {
		return false, 0, 0
	}
	defer rec.Close()
	return rec.Frames > 0, rec.DurationS(), rec.Rate
}

// --- HTTP ---

// Handler is the journal's page and its API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/calls/", s.handleCall)
	mux.HandleFunc("/api/issues", s.handleFile("issues.json", `{"issues":[]}`))
	mux.HandleFunc("/api/decisions", s.handleFile("decisions.json", `{"decisions":[]}`))
	if s.cfg.UIDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.cfg.UIDir)))
	} else {
		sub, _ := fs.Sub(uiFiles, "ui")
		mux.Handle("/", http.FileServer(http.FS(sub)))
	}
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleFile serves one of the written files as it stands.
func (s *Server) handleFile(name, empty string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		data, err := os.ReadFile(filepath.Join(s.cfg.NotesDir, name))
		if err != nil || s.cfg.NotesDir == "" {
			_, _ = w.Write([]byte(empty))
			return
		}
		_, _ = w.Write(data)
	}
}

// CallRow is a call as the list shows it.
type CallRow struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	DurationS float64   `json:"duration_s"`
	Build     string    `json:"build"`
	Summary   Stats     `json:"summary"`
	LLMCopies int       `json:"llm_copies,omitempty"`
	HasAudio  bool      `json:"has_audio"`
	HasRetro  bool      `json:"has_retro"`
	Rating    int       `json:"rating,omitempty"`
	// Worst is what the slowest turn over target was laid to, so that the list
	// says something about a bad call without opening it.
	Worst string `json:"worst,omitempty"`
}

// BuildRow pools the calls made on one build, which is the only pooling the
// figures allow: a turn on another build was a turn through other code.
type BuildRow struct {
	Build   string    `json:"build"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	Calls   int       `json:"calls"`
	Summary Stats     `json:"summary"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	s.refresh()
	s.mu.Lock()
	calls := append([]*Call(nil), s.calls...)
	build := s.logBuild
	s.mu.Unlock()

	rows := make([]CallRow, 0, len(calls))
	pooled := map[string]*BuildRow{}
	turnsBy := map[string][]Turn{}
	var order []string
	var recent []Turn
	for _, c := range calls {
		row := CallRow{ID: c.ID, StartedAt: c.StartedAt, DurationS: c.DurationS, Build: c.Build,
			Summary: c.Summary, LLMCopies: c.LLMCopies}
		row.HasAudio, _, _ = s.audioOf(c)
		row.HasRetro = s.retro(c.ID) != nil
		if f := s.feedback(c.ID); f != nil {
			row.Rating = f.Rating
		}
		worst := 0
		for _, t := range c.Turns {
			if t.Over && t.TotalMS > worst {
				worst, row.Worst = t.TotalMS, t.Cause
			}
		}
		rows = append(rows, row)

		b := pooled[c.Build]
		if b == nil {
			b = &BuildRow{Build: c.Build, First: c.StartedAt, Last: c.StartedAt}
			pooled[c.Build] = b
			order = append(order, c.Build)
		}
		b.Calls++
		if c.StartedAt.Before(b.First) {
			b.First = c.StartedAt
		}
		if c.StartedAt.After(b.Last) {
			b.Last = c.StartedAt
		}
		turnsBy[c.Build] = append(turnsBy[c.Build], c.Turns...)
		if len(recent) < 100 {
			recent = append(recent, c.Turns...)
		}
	}
	builds := make([]BuildRow, 0, len(order))
	for _, name := range order {
		b := pooled[name]
		b.Summary = Summarise(turnsBy[name])
		builds = append(builds, *b)
	}
	if len(recent) > 100 {
		recent = recent[:100]
	}
	_, storeErr := os.Stat(s.cfg.StoreDir)
	writeJSON(w, http.StatusOK, map[string]any{
		"target_ms":       TargetMS,
		"generated_at":    time.Now(),
		"build":           build,
		"repo_url":        s.cfg.RepoURL,
		"recording":       s.cfg.StoreDir != "" && storeErr == nil,
		"retention_hours": s.cfg.RetentionHours,
		"min_turns":       MinTurnsForP95,
		"calls":           rows,
		"builds":          builds,
		"recent":          Summarise(recent),
	})
}

func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/calls/")
	id, what, _ := strings.Cut(rest, "/")
	if !idPattern.MatchString(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	s.refresh()
	s.mu.Lock()
	c := s.byID[id]
	s.mu.Unlock()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}

	switch what {
	case "":
		available, seconds, rate := s.audioOf(c)
		writeJSON(w, http.StatusOK, map[string]any{
			"call":          c,
			"target_ms":     TargetMS,
			"repo_url":      s.cfg.RepoURL,
			"audio":         map[string]any{"available": available, "duration_s": seconds, "rate": rate},
			"min_turns":     MinTurnsForP95,
			"retrospective": s.retro(c.ID),
			"feedback":      s.feedback(c.ID),
		})
	case "audio.wav":
		rec, err := OpenRecording(s.cfg.StoreDir, c.Session)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this call has no recording"})
			return
		}
		defer rec.Close()
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeContent(w, r, "", c.StartedAt, rec.WAV())
	case "peaks":
		rec, err := OpenRecording(s.cfg.StoreDir, c.Session)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this call has no recording"})
			return
		}
		defer rec.Close()
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n == 0 {
			n = 1200
		}
		writeJSON(w, http.StatusOK, rec.Peaks(n))
	case "feedback":
		s.handleFeedback(w, r, c)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such thing"})
	}
}

// handleFeedback is the one thing the page can write: the owner's rating and
// note for a call. Nothing else about the journal can be changed from a
// browser, which is what lets it be served to a home network without a login.
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request, c *Call) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST a rating and a note"})
		return
	}
	var in struct {
		Rating int    `json:"rating"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that is not JSON"})
		return
	}
	if in.Rating < 0 || in.Rating > 5 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a rating is 1 to 5, or 0 for none"})
		return
	}
	if len([]rune(in.Note)) > 4000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a note is at most 4000 characters"})
		return
	}
	if s.cfg.DataDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the journal has nowhere to keep notes"})
		return
	}
	f := Feedback{Rating: in.Rating, Note: strings.TrimSpace(in.Note), UpdatedAt: time.Now()}
	path := s.feedbackPath(c.ID)
	data, _ := json.Marshal(f)
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		var pe *os.PathError
		msg := "the note could not be saved"
		if errors.As(err, &pe) {
			msg += ": " + pe.Err.Error()
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": msg})
		return
	}
	writeJSON(w, http.StatusOK, f)
}
