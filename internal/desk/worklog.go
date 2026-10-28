package desk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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

// The work log: what an agent is doing, as it does it.
//
// The owner asked (17:20 on 10-28) to see in real time what Codex does on
// the local-talker training and where the model is trained. Notes on the
// desk are for outcomes; this is for the steps between them: a stream per
// task, append-only, one line per command or progress mark, posted by the
// participant doing the work (`scripts/desk.sh log <stream> -` pipes a
// command's output in) and tailed live on the desk's Work log page. The
// first line of a stream is expected to say where the work runs
// (`host=… device=…`), so "where is it trained" has one answer.
//
// Lines are kept under <dir>/logs/<stream>.log as `<unix ms>\t<who>\t<text>`.
// Runs of seven or more digits are masked before storage, the same rule as
// the runner's output: a phone number must not land here by accident. The
// streams are the participants' and the owner's; nothing here is executed.

// WorkLine is one line of a stream.
type WorkLine struct {
	N    int       `json:"n"`
	At   time.Time `json:"at"`
	Who  string    `json:"who"`
	Text string    `json:"text"`
}

// WorkStream summarizes one stream.
type WorkStream struct {
	Name   string    `json:"name"`
	Lines  int       `json:"lines"`
	LastAt time.Time `json:"last_at"`
	Last   string    `json:"last"`
	First  string    `json:"first"` // the opening line: host=…, device=…
	Who    string    `json:"who"`
}

type workLog struct {
	dir string
	mu  sync.Mutex
	// subs are tails waiting for new lines, by stream.
	subs map[string]map[chan WorkLine]struct{}
}

var (
	workStreamName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	longDigits     = regexp.MustCompile(`[0-9]{7,}`)
)

// workMaxLine bounds one line; longer ones are cut, not refused.
const workMaxLine = 2000

// workMaxBody bounds one post.
const workMaxBody = 1 << 20

func newWorkLog(dir string) *workLog {
	return &workLog{dir: filepath.Join(dir, "logs"), subs: map[string]map[chan WorkLine]struct{}{}}
}

func (w *workLog) path(stream string) string { return filepath.Join(w.dir, stream+".log") }

// append writes lines to a stream and wakes its tails.
func (w *workLog) append(stream, who string, texts []string, now time.Time) ([]WorkLine, error) {
	if !workStreamName.MatchString(stream) {
		return nil, fmt.Errorf("a stream is named with letters, digits, dots, dashes (%q)", stream)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(w.path(stream), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n := w.countLocked(stream)
	var out []WorkLine
	for _, t := range texts {
		t = strings.TrimRight(t, "\r\n")
		if strings.TrimSpace(t) == "" {
			continue
		}
		t = longDigits.ReplaceAllString(t, "<num>")
		if r := []rune(t); len(r) > workMaxLine {
			t = string(r[:workMaxLine]) + "…"
		}
		t = strings.ReplaceAll(strings.ReplaceAll(t, "\t", "    "), "\n", " ")
		n++
		line := WorkLine{N: n, At: now, Who: who, Text: t}
		if _, err := fmt.Fprintf(f, "%d\t%s\t%s\n", now.UnixMilli(), who, t); err != nil {
			return out, err
		}
		out = append(out, line)
	}
	for ch := range w.subs[stream] {
		for _, l := range out {
			select {
			case ch <- l:
			default:
			}
		}
	}
	return out, nil
}

// countLocked is how many lines a stream has.
func (w *workLog) countLocked(stream string) int {
	f, err := os.Open(w.path(stream))
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		n++
	}
	return n
}

// read returns the lines after index `after` (0 for all), at most `limit`.
func (w *workLog) read(stream string, after, limit int) ([]WorkLine, int, error) {
	if !workStreamName.MatchString(stream) {
		return nil, 0, fmt.Errorf("no such stream")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := os.Open(w.path(stream))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	var out []WorkLine
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		n++
		if n <= after {
			continue
		}
		if limit > 0 && len(out) >= limit {
			continue // keep counting for the total
		}
		out = append(out, parseWorkLine(n, sc.Text()))
	}
	return out, n, nil
}

func parseWorkLine(n int, raw string) WorkLine {
	parts := strings.SplitN(raw, "\t", 3)
	l := WorkLine{N: n}
	if len(parts) == 3 {
		if ms, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
			l.At = time.UnixMilli(ms)
		}
		l.Who, l.Text = parts[1], parts[2]
	} else {
		l.Text = raw
	}
	return l
}

// list summarizes every stream, most recent first.
func (w *workLog) list() ([]WorkStream, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []WorkStream
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".log")
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") || !workStreamName.MatchString(name) {
			continue
		}
		f, err := os.Open(filepath.Join(w.dir, e.Name()))
		if err != nil {
			continue
		}
		s := WorkStream{Name: name}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			s.Lines++
			l := parseWorkLine(s.Lines, sc.Text())
			if s.Lines == 1 {
				s.First, s.Who = l.Text, l.Who
			}
			s.LastAt, s.Last = l.At, l.Text
		}
		f.Close()
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out, nil
}

func (w *workLog) subscribe(stream string) (chan WorkLine, func()) {
	ch := make(chan WorkLine, 256)
	w.mu.Lock()
	if w.subs[stream] == nil {
		w.subs[stream] = map[chan WorkLine]struct{}{}
	}
	w.subs[stream][ch] = struct{}{}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		delete(w.subs[stream], ch)
		w.mu.Unlock()
	}
}

// mountWorkLog adds the work log's routes.
func (s *Service) mountWorkLog(mux *http.ServeMux) {
	w := s.work
	mux.HandleFunc("GET /api/logs", s.authed(func(rw http.ResponseWriter, r *http.Request, who string) error {
		streams, err := w.list()
		if err != nil {
			return err
		}
		if streams == nil {
			streams = []WorkStream{}
		}
		writeJSON(rw, http.StatusOK, map[string]any{"streams": streams})
		return nil
	}))
	mux.HandleFunc("GET /api/logs/{stream}", s.authed(func(rw http.ResponseWriter, r *http.Request, who string) error {
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 5000 {
			limit = 2000
		}
		lines, total, err := w.read(r.PathValue("stream"), after, limit)
		if err != nil {
			return fail(http.StatusNotFound, "%v", err)
		}
		if lines == nil {
			lines = []WorkLine{}
		}
		writeJSON(rw, http.StatusOK, map[string]any{"stream": r.PathValue("stream"), "lines": lines, "total": total})
		return nil
	}))
	// POST: the body is text, one line per line (or JSON {"lines": [...]}).
	mux.HandleFunc("POST /api/logs/{stream}", s.authed(func(rw http.ResponseWriter, r *http.Request, who string) error {
		raw, err := io.ReadAll(io.LimitReader(r.Body, workMaxBody))
		if err != nil {
			return fail(http.StatusBadRequest, "the lines could not be read")
		}
		var texts []string
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var body struct {
				Lines []string `json:"lines"`
				Line  string   `json:"line"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				return fail(http.StatusBadRequest, "not JSON: %v", err)
			}
			texts = append(body.Lines, body.Line)
		} else {
			texts = strings.Split(string(raw), "\n")
		}
		lines, err := w.append(r.PathValue("stream"), who, texts, s.cfg.Now())
		if err != nil {
			return fail(http.StatusBadRequest, "%v", err)
		}
		n := 0
		if len(lines) > 0 {
			n = lines[len(lines)-1].N
		}
		writeJSON(rw, http.StatusOK, map[string]any{"stream": r.PathValue("stream"), "added": len(lines), "total": n})
		return nil
	}))
	// A live tail: the lines after `after`, then each new one as it lands.
	mux.HandleFunc("GET /api/logs/{stream}/tail", s.authed(func(rw http.ResponseWriter, r *http.Request, who string) error {
		fl, ok := rw.(http.Flusher)
		if !ok {
			return fail(http.StatusNotImplemented, "streaming is not possible here")
		}
		stream := r.PathValue("stream")
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		ch, stop := w.subscribe(stream)
		defer stop()
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("Cache-Control", "no-store")
		rw.WriteHeader(http.StatusOK)
		lines, total, err := w.read(stream, after, 5000)
		if err != nil {
			return nil
		}
		last := 0
		for _, l := range lines {
			b, _ := json.Marshal(l)
			_, _ = io.WriteString(rw, "event: line\ndata: "+string(b)+"\n\n")
			last = l.N
		}
		if last == 0 {
			last = total
		}
		fl.Flush()
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return nil
			case <-ping.C:
				_, _ = io.WriteString(rw, ": ping\n\n")
				fl.Flush()
			case l := <-ch:
				if l.N <= last {
					continue
				}
				last = l.N
				b, _ := json.Marshal(l)
				_, _ = io.WriteString(rw, "event: line\ndata: "+string(b)+"\n\n")
				fl.Flush()
			}
		}
	}))
}
