package journal

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"github.com/chuanmingliu/golive/internal/benchrec"
	"io"
	"io/fs"
	"log/slog"
	"math"
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
	// ArchiveDir is where finished calls are kept for good: recording,
	// conversation, measurements and the log they came from (archive.go).
	// Empty archives nothing.
	ArchiveDir string
	// LastCallFile is where golive keeps the instructions of the last answered
	// call (last_call_file); the archive keeps a copy of each distinct text.
	LastCallFile string
	// UIDir serves the page from a directory instead of the copy built into
	// the binary, for working on it.
	UIDir string
	// SimDials is where `phone-brain.sh call sim` writes one line per call it
	// placed to the simulated customer ({"at","call","run"}): the only way to
	// tell such a call from a real one, since the line looks the same to
	// golive. Empty means every call is taken to be real.
	SimDials string
	// TalkerURL is a fixed local model endpoint used only for an on-demand
	// status check. TalkerPlayground is the owner's existing desk page.
	TalkerURL        string
	TalkerPlayground string
	Log              *slog.Logger
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

	archive *Archive
	// ranges is where each call's lines are in the log as last read, and
	// archiveDue whether there is anything the archive has not been given.
	ranges     map[string][2]int64
	archiveDue bool
	// simRuns is call id → simulator run id from SimDials, and simMod when
	// the file was last read.
	simRuns map[string]string
	simMod  time.Time
	// settled is each call as it stood when the archive last had all of it,
	// so that a pass costs nothing for the calls that have not changed.
	settled map[string]callSig

	// aligned is, for each recording whose caller's side had to be put back
	// on the clock, how: worked out once, on the first request for it. A nil
	// entry is a recording that needed nothing.
	alignMu sync.Mutex
	aligned map[string]*Alignment
}

// callSig is enough of a call to tell that it has not changed since it was
// archived, without writing it out again to compare.
type callSig struct {
	ended        time.Time
	turns, lines int
	summary      Stats
}

func sigOf(c *Call) callSig {
	return callSig{ended: c.EndedAt, turns: len(c.Turns), lines: len(c.Transcript), summary: c.Summary}
}

// NewServer builds a journal over cfg. Nothing is read until it is asked for.
func NewServer(cfg Config) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	s := &Server{cfg: cfg, log: cfg.Log, byID: map[string]*Call{}, ranges: map[string][2]int64{}, archiveDue: true, settled: map[string]callSig{}}
	if cfg.ArchiveDir != "" {
		a, err := OpenArchive(cfg.ArchiveDir)
		if err != nil {
			s.log.Error("journal: the archive could not be opened; calls are not being archived", "dir", cfg.ArchiveDir, "err", err)
		} else {
			s.archive = a
		}
	}
	return s
}

// Watch reads the log and feeds the archive on a clock, so that a call is
// archived whether or not anyone opens the page: the recording it is copied
// from has a retention, and a journal that archived only when looked at would
// lose the calls nobody looked at in time.
//
// It stays out of a call's way. Reading the log is the journal's one piece of
// real work, golive writes to it throughout a call, and the two share a
// machine whose whole purpose is that golive answers quickly — so the clock
// only reads a log that has been still for a moment, which is a log between
// calls. (Someone who opens the page during a call gets it read for them; that
// is theirs to choose.)
func (s *Server) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if info, err := os.Stat(s.cfg.LogPath); err != nil || time.Since(info.ModTime()) >= logQuiet {
			s.refresh()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// logQuiet is how long golive's log must have been still before the clock
// reads it.
const logQuiet = 8 * time.Second

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
	s.readDials()
	s.readLog()
	if s.archiveDue {
		s.archiveDue = s.archivePass()
	}
}

// readDials reads the record of calls placed to the simulated customer, when
// the file has changed. Held under s.mu.
func (s *Server) readDials() {
	if s.cfg.SimDials == "" {
		return
	}
	st, err := os.Stat(s.cfg.SimDials)
	if err != nil || st.ModTime().Equal(s.simMod) {
		return
	}
	data, err := os.ReadFile(s.cfg.SimDials)
	if err != nil {
		return
	}
	runs := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		var d struct {
			Call string `json:"call"`
			Run  string `json:"run"`
		}
		if json.Unmarshal([]byte(line), &d) == nil && d.Call != "" {
			runs[d.Call] = d.Run
		}
	}
	s.simRuns, s.simMod = runs, st.ModTime()
}

// simRunOf is the simulator run a call was placed to, or "" for a real call.
func (s *Server) simRunOf(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.simRuns[id]
}

// archivePass gives the archive every finished call, and reports whether any
// is still waiting on golive to finish writing its recording.
func (s *Server) archivePass() (waiting bool) {
	if s.archive == nil {
		return false
	}
	prompt, promptSHA := s.lastPrompt()
	for _, c := range s.calls {
		if c.EndedAt.IsZero() {
			// Still on the line — or a call whose end the log never recorded,
			// which is not worth waiting for after a few hours.
			if time.Since(c.StartedAt) < 6*time.Hour {
				waiting = true
			}
			continue
		}
		if sig, ok := s.settled[c.ID]; ok && sig == sigOf(c) {
			continue
		}
		src := Sources{StoreDir: s.cfg.StoreDir, Prompt: prompt, PromptSHA: promptSHA}
		if r, ok := s.ranges[c.ID]; ok {
			src.LogPath, src.LogFrom, src.LogTo = s.cfg.LogPath, r[0], r[1]
		}
		changed, err := s.archive.Put(c, src)
		if err != nil {
			s.log.Error("journal: a call could not be archived; it will be tried again", "call", c.ID, "err", err)
			waiting = true
			continue
		}
		e := s.archive.Entry(c.ID)
		if changed && e != nil {
			s.log.Info("journal: call archived", "call", c.ID, "audio", e.Audio, "bytes", e.Bytes, "dir", e.Dir)
		}
		// Recorded, and the recording is there but not yet closed: come back.
		if e != nil && !e.Audio && s.cfg.StoreDir != "" && c.Session != "" {
			if _, err := os.Stat(filepath.Join(s.cfg.StoreDir, c.Session, inputTrack)); err == nil {
				waiting = true
				continue
			}
		}
		s.settled[c.ID] = sigOf(c)
	}
	return waiting
}

// lastPrompt is the instructions golive last kept, and their hash.
func (s *Server) lastPrompt() (text, sha string) {
	if s.cfg.LastCallFile == "" {
		return "", ""
	}
	data, err := os.ReadFile(s.cfg.LastCallFile)
	if err != nil {
		return "", ""
	}
	var kept struct {
		Instructions string `json:"instructions"`
		SHA          string `json:"instructions_sha"`
	}
	if json.Unmarshal(data, &kept) != nil {
		return "", ""
	}
	return kept.Instructions, kept.SHA
}

// readLog re-reads the log when it has changed.
func (s *Server) readLog() {
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
	s.ranges = map[string][2]int64{}
	for _, c := range merged {
		seen[c.ID] = true
		s.keep(c)
		if from, to, ok := ix.LogRange(c.ID); ok {
			s.ranges[c.ID] = [2]int64{from, to}
		}
	}
	s.archiveDue = true
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
	// And what only the archive still has: the journal's own copies are a
	// convenience, the archive is the record.
	for _, e := range s.archive.Entries() {
		if s.byID[e.ID] != nil {
			continue
		}
		if c, err := s.archive.Call(e.ID); err == nil && c.ID != "" {
			s.calls = append(s.calls, c)
			s.byID[c.ID] = c
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

func (s *Server) audioOf(c *Call) map[string]any {
	rec, err := s.recording(c)
	if err != nil {
		return map[string]any{"available": false, "duration_s": 0.0, "rate": 0}
	}
	defer rec.Close()
	return map[string]any{"available": rec.Frames > 0, "duration_s": rec.DurationS(), "rate": rec.Rate,
		// How much of the caller's side is left out when it is played, to keep
		// it on the call's clock; zero for a recording that was made on it.
		"caller_trimmed_s": math.Round(rec.DroppedS()*10) / 10}
}

// recording opens a call's audio as it should be heard: the archive's copy,
// which stays, or golive's, which is all there is until the call has ended and
// been archived; and, for a recording made before the recorder kept the
// caller's side on the clock, with that side put back on it.
func (s *Server) recording(c *Call) (*Recording, error) {
	rec, record, err := s.rawRecording(c)
	if err != nil {
		return nil, err
	}
	if len(c.AudioIn) < 3 || onClock(record) {
		return rec, nil
	}
	s.alignMu.Lock()
	defer s.alignMu.Unlock()
	key := c.ID + "|" + strconv.FormatInt(rec.Frames, 10)
	if a, done := s.aligned[key]; done {
		rec.Use(a)
		return rec, nil
	}
	if s.aligned == nil {
		s.aligned = map[string]*Alignment{}
	}
	rec.Realign(c.AudioIn)
	// Kept only for a call that has ended: until then the recording grows.
	if !c.EndedAt.IsZero() {
		s.aligned[key] = rec.Aligned()
	}
	return rec, nil
}

// rawRecording opens a call's audio exactly as it was recorded, and says
// where its session record is.
func (s *Server) rawRecording(c *Call) (rec *Recording, record string, err error) {
	if e := s.archive.Entry(c.ID); e != nil && e.Audio {
		if rec, err := OpenArchivedWAV(s.archive.Path(c.ID, archAudio)); err == nil {
			return rec, s.archive.Path(c.ID, archSession), nil
		}
	}
	rec, err = OpenRecording(s.cfg.StoreDir, c.Session)
	return rec, filepath.Join(s.cfg.StoreDir, c.Session, recordFile), err
}

// gateTrace reads the gate samples from a call's session record, in the
// archive if it is there and otherwise in golive's store. nil when the
// record has none (a call before the record was kept, or a build without it).
func (s *Server) gateTrace(c *Call) []json.RawMessage {
	path := filepath.Join(s.cfg.StoreDir, c.Session, recordFile)
	if e := s.archive.Entry(c.ID); e != nil {
		if p := s.archive.Path(c.ID, archSession); fileExists(p) {
			path = p
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec struct {
		Gate []json.RawMessage `json:"gate"`
	}
	if json.Unmarshal(data, &rec) != nil || len(rec.Gate) == 0 {
		return nil
	}
	return rec.Gate
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// onClock reports whether a session's record says its caller's track was kept
// on the session clock as it was written.
func onClock(recordPath string) bool {
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return false
	}
	var rec struct {
		OnClock bool `json:"input_on_clock"`
	}
	return json.Unmarshal(data, &rec) == nil && rec.OnClock
}

// archiveOf is what the page is told about a call's place in the archive.
func (s *Server) archiveOf(c *Call) map[string]any {
	e := s.archive.Entry(c.ID)
	if e == nil {
		return map[string]any{"archived": false}
	}
	return map[string]any{"archived": true, "audio": e.Audio, "bytes": e.Bytes,
		"dir": filepath.Join(s.archive.Dir(), e.Dir), "archived_at": e.ArchivedAt}
}

// --- HTTP ---

// Handler is the journal's page and its API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/calls/", s.handleCall)
	mux.HandleFunc("GET /api/talker-status", s.handleTalkerStatus)
	mux.HandleFunc("/api/issues", s.handleFile("issues.json", `{"issues":[]}`))
	mux.HandleFunc("/api/decisions", s.handleFile("decisions.json", `{"decisions":[]}`))
	mux.HandleFunc("/api/benchmarks", func(w http.ResponseWriter, r *http.Request) {
		// The standing evaluation's records: journal/benchmarks/*.json,
		// newest first, with each record's verdicts against its thresholds.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		var recs []benchrec.Record
		if s.cfg.NotesDir != "" {
			recs, _ = benchrec.Read(filepath.Join(s.cfg.NotesDir, "benchmarks"))
		}
		type row struct {
			benchrec.Record
			Verdicts map[string]bool `json:"verdicts"`
		}
		rows := make([]row, 0, len(recs))
		for _, rec := range recs {
			rows = append(rows, row{Record: rec, Verdicts: rec.Verdicts()})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"benchmarks": rows})
	})
	var static http.Handler
	if s.cfg.UIDir != "" {
		static = http.FileServer(http.Dir(s.cfg.UIDir))
	} else {
		sub, _ := fs.Sub(uiFiles, "ui")
		static = http.FileServer(http.FS(sub))
	}
	// The page's script and style are revalidated on every load: a browser
	// that kept the old app.js showed a journal without its newest sections
	// twice (the Quotation section on 10-20, the Benchmarks tab on 10-21)
	// until a hard refresh.
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))
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
	Archived  bool      `json:"archived"`
	HasRetro  bool      `json:"has_retro"`
	Rating    int       `json:"rating,omitempty"`
	// Worst is what the slowest turn over target was laid to, so that the list
	// says something about a bad call without opening it.
	Worst string `json:"worst,omitempty"`
	// Kind is "simulated" for a call placed to the simulated customer (the
	// run id in RunID), and empty for a real one.
	Kind  string `json:"kind,omitempty"`
	RunID string `json:"run_id,omitempty"`
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
		// Whether there is a recording, without working out how to play it.
		if rec, _, err := s.rawRecording(c); err == nil {
			row.HasAudio = rec.Frames > 0
			rec.Close()
		}
		row.Archived = s.archive.Entry(c.ID) != nil
		row.HasRetro = s.retro(c.ID) != nil
		if run := s.simRunOf(c.ID); run != "" {
			row.Kind, row.RunID = "simulated", run
		}
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
		"archive":         s.archiveTotals(),
		"min_turns":       MinTurnsForP95,
		"calls":           rows,
		"builds":          builds,
		"recent":          Summarise(recent),
	})
}

// archiveTotals is the archive in a few numbers, or nil when there is none.
func (s *Server) archiveTotals() *ArchiveTotals {
	if s.archive == nil {
		return nil
	}
	t := s.archive.Totals()
	return &t
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
		writeJSON(w, http.StatusOK, map[string]any{
			"call":          s.withFillerEvidence(s.withLatencyEvidence(c)),
			"target_ms":     TargetMS,
			"repo_url":      s.cfg.RepoURL,
			"audio":         s.audioOf(c),
			"min_turns":     MinTurnsForP95,
			"archive":       s.archiveOf(c),
			"retrospective": s.retro(c.ID),
			"feedback":      s.feedback(c.ID),
			"sim_run":       s.simRunOf(c.ID),
		})
	case "audio.wav":
		rec, err := s.recording(c)
		if r.URL.Query().Get("raw") != "" {
			// Exactly as recorded, with nothing left out.
			if rec != nil {
				rec.Close()
			}
			rec, _, err = s.rawRecording(c)
		}
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this call has no recording"})
			return
		}
		defer rec.Close()
		// ?side=caller or ?side=assistant is that side alone, mono: the
		// samples golive stored for it, for a recognizer or an editor that
		// wants one voice. Without it, both sides in one stereo file.
		wav, suffix := rec.WAV(), ""
		switch side := r.URL.Query().Get("side"); side {
		case "":
		case "caller", "assistant":
			wav, suffix = rec.Side(side == "assistant"), "_"+side
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "side is caller or assistant"})
			return
		}
		if r.URL.Query().Get("raw") != "" {
			suffix += "_as-recorded"
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		if r.URL.Query().Get("download") != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+fileStem(c)+suffix+`.wav"`)
		}
		http.ServeContent(w, r, "", c.StartedAt, wav)
	case "transcript.txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Query().Get("download") != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+fileStem(c)+`.txt"`)
		}
		_, _ = io.WriteString(w, TranscriptText(s.withLatencyEvidence(c)))
	case "peaks":
		rec, err := s.recording(c)
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
	case "gate":
		// The energy gate's record of the call, from golive's session
		// record: the line's level against the floor and the bar, every
		// 100 ms, with the gate's openings from the log beside it.
		samples := s.gateTrace(c)
		if samples == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this call has no gate record"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"samples":    samples,
			"utterances": c.Utterances,
		})
	case "feedback":
		s.handleFeedback(w, r, c)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such thing"})
	}
}

// fileStem names a call's files for saving: when it was, and which call.
func fileStem(c *Call) string {
	return c.StartedAt.Format("2006-01-02_150405") + "_" + c.ID
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
