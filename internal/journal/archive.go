package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The archive is where a call goes to stay.
//
// Everything else that holds a call is working storage with a clock on it:
// golive's store deletes a recording after its retention, the log is one file
// that will one day be rotated or lost, and the journal's own copy of a call
// is whatever its parser made of that log on the day. The archive has no
// clock. Nothing here deletes from it, and nothing rewrites audio once it is
// in.
//
// One directory per call, named so that a listing is in order and a person
// can find a call without a tool:
//
//	calls/2026/10/2026-10-12_124921_out-mv4rvtp9-d3fw/
//	    audio.wav        the recording: stereo, caller left, assistant right
//	    transcript.txt   the conversation, to read
//	    call.json        everything measured: turns, stages, events, transcript
//	    session.json     golive's own record of the session, as it wrote it
//	    golive.log       this call's lines of golive's log — what the rest was derived from
//	    manifest.json    what is here, how big, and each file's SHA-256
//	prompts/<sha>.txt    the instructions a call ran on, once per distinct text
//	index.jsonl          one line per call, for finding things; rebuilt from the manifests
//
// The raw material is kept beside what was made from it on purpose. call.json
// is a reading of the log by today's parser; golive.log lets a better parser
// read it again in a year.
//
// A call is archived when it has ended. A call with no recording (recording
// was off) is archived as text; if its recording turns up later it is added.
// Text files are replaced when the journal's reading of the call changes;
// audio.wav, session.json and golive.log are written once.
type Archive struct {
	dir string

	mu      sync.Mutex
	entries map[string]*ArchiveEntry // by call id
}

// ArchiveVersion is the layout above. It goes in every manifest.
const ArchiveVersion = 1

// ArchiveEntry is one call's manifest.
type ArchiveEntry struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	Session    string    `json:"session"`
	StartedAt  time.Time `json:"started_at"`
	DurationS  float64   `json:"duration_s"`
	Build      string    `json:"build"`
	Turns      int       `json:"turns"`
	P50        int       `json:"p50_ms"`
	P95        int       `json:"p95_ms"`
	Max        int       `json:"max_ms"`
	Over       int       `json:"over"`
	Lines      int       `json:"transcript_lines"`
	PromptSHA  string    `json:"prompt_sha,omitempty"`
	Audio      bool      `json:"audio"`
	AudioS     float64   `json:"audio_s,omitempty"`
	Bytes      int64     `json:"bytes"`
	Dir        string    `json:"dir"` // relative to the archive
	ArchivedAt time.Time `json:"archived_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Files is each file's size and SHA-256, for Verify.
	Files map[string]ArchiveFile `json:"files"`
}

// ArchiveFile is one file of a call.
type ArchiveFile struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

const (
	archAudio      = "audio.wav"
	archTranscript = "transcript.txt"
	archCall       = "call.json"
	archSession    = "session.json"
	archLog        = "golive.log"
	archManifest   = "manifest.json"
)

// OpenArchive opens the archive in dir, creating it if need be, and reads
// what it holds.
func OpenArchive(dir string) (*Archive, error) {
	if dir == "" {
		return nil, errors.New("journal: no archive directory")
	}
	if err := os.MkdirAll(filepath.Join(dir, "calls"), 0o700); err != nil {
		return nil, err
	}
	a := &Archive{dir: dir, entries: map[string]*ArchiveEntry{}}
	manifests, _ := filepath.Glob(filepath.Join(dir, "calls", "*", "*", "*", archManifest))
	for _, path := range manifests {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var e ArchiveEntry
		if json.Unmarshal(data, &e) != nil || e.ID == "" {
			continue
		}
		// Where it is, is where it was found: an archive that was copied or
		// moved is still an archive.
		if rel, err := filepath.Rel(dir, filepath.Dir(path)); err == nil {
			e.Dir = rel
		}
		a.entries[e.ID] = &e
	}
	return a, nil
}

// Dir is where the archive is.
func (a *Archive) Dir() string { return a.dir }

// Entry returns a call's manifest, or nil if the call is not archived.
func (a *Archive) Entry(id string) *ArchiveEntry {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.entries[id]; e != nil {
		c := *e
		return &c
	}
	return nil
}

// Entries returns every manifest, newest call first.
func (a *Archive) Entries() []ArchiveEntry {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ArchiveEntry, 0, len(a.entries))
	for _, e := range a.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// ArchiveTotals is the archive in four numbers.
type ArchiveTotals struct {
	Dir       string  `json:"dir"`
	Calls     int     `json:"calls"`
	WithAudio int     `json:"with_audio"`
	AudioS    float64 `json:"audio_s"`
	Bytes     int64   `json:"bytes"`
}

// Totals sums the archive.
func (a *Archive) Totals() ArchiveTotals {
	t := ArchiveTotals{}
	if a == nil {
		return t
	}
	t.Dir = a.dir
	for _, e := range a.Entries() {
		t.Calls++
		t.Bytes += e.Bytes
		if e.Audio {
			t.WithAudio++
			t.AudioS += e.AudioS
		}
	}
	return t
}

// Path is where one of a call's files is, or "" if the call is not archived.
func (a *Archive) Path(id, name string) string {
	e := a.Entry(id)
	if e == nil {
		return ""
	}
	return filepath.Join(a.dir, e.Dir, name)
}

// Call reads an archived call back.
func (a *Archive) Call(id string) (*Call, error) {
	path := a.Path(id, archCall)
	if path == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Call
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Sources is where a call's raw material is at the moment it is archived.
// Any of it may be missing; what is there is taken.
type Sources struct {
	// StoreDir is golive's store, holding the recording while it lasts.
	StoreDir string
	// LogPath and the byte range LogFrom–LogTo are the call's lines in
	// golive's log.
	LogPath        string
	LogFrom, LogTo int64
	// Prompt is the instructions whose hash is PromptSHA, when they are known.
	Prompt, PromptSHA string
}

// Put archives a finished call, or brings what is archived up to date: text
// that reads differently now is replaced, and audio that was not there before
// is added. It reports whether anything was written.
func (a *Archive) Put(c *Call, src Sources) (changed bool, err error) {
	if a == nil || c == nil || c.EndedAt.IsZero() || !idPattern.MatchString(c.ID) {
		return false, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	e := a.entries[c.ID]
	if e == nil {
		e = &ArchiveEntry{
			Version:    ArchiveVersion,
			ID:         c.ID,
			Dir:        filepath.Join("calls", c.StartedAt.Format("2006"), c.StartedAt.Format("01"), c.StartedAt.Format("2006-01-02_150405")+"_"+c.ID),
			ArchivedAt: now,
			Files:      map[string]ArchiveFile{},
		}
	} else {
		cp := *e
		cp.Files = make(map[string]ArchiveFile, len(e.Files))
		for k, v := range e.Files {
			cp.Files[k] = v
		}
		e = &cp
	}
	dir := filepath.Join(a.dir, e.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}

	// Text: written whenever it reads differently from what is there.
	callJSON, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return false, err
	}
	for name, data := range map[string][]byte{
		archCall:       append(callJSON, '\n'),
		archTranscript: []byte(TranscriptText(c)),
	} {
		sum := sha256Hex(data)
		if e.Files[name].SHA256 == sum {
			continue
		}
		if err := writeFileAtomic(filepath.Join(dir, name), data); err != nil {
			return changed, err
		}
		e.Files[name] = ArchiveFile{Bytes: int64(len(data)), SHA256: sum}
		changed = true
	}

	// The call's lines of the log: once.
	if _, have := e.Files[archLog]; !have && src.LogPath != "" && src.LogTo > src.LogFrom {
		if f, err := os.Open(src.LogPath); err == nil {
			file, werr := copyAtomic(filepath.Join(dir, archLog), io.NewSectionReader(f, src.LogFrom, src.LogTo-src.LogFrom))
			f.Close()
			if werr != nil {
				return changed, werr
			}
			e.Files[archLog] = file
			changed = true
		}
	}

	// The recording and golive's own record of the session: once, and only
	// from a session golive has finished writing.
	if _, have := e.Files[archAudio]; !have && src.StoreDir != "" && c.Session != "" {
		sdir := filepath.Join(src.StoreDir, c.Session)
		if closedSession(filepath.Join(sdir, recordFile)) {
			if rec, err := OpenRecording(src.StoreDir, c.Session); err == nil {
				if rec.Frames > 0 {
					file, werr := copyAtomic(filepath.Join(dir, archAudio), rec.WAV())
					if werr != nil {
						rec.Close()
						return changed, werr
					}
					e.Files[archAudio] = file
					e.Audio, e.AudioS = true, rec.DurationS()
					changed = true
				}
				rec.Close()
			}
			if _, have := e.Files[archSession]; !have {
				if f, err := os.Open(filepath.Join(sdir, recordFile)); err == nil {
					file, werr := copyAtomic(filepath.Join(dir, archSession), f)
					f.Close()
					if werr != nil {
						return changed, werr
					}
					e.Files[archSession] = file
					changed = true
				}
			}
		}
	}

	// The instructions the call ran on, once per distinct text.
	if src.Prompt != "" && src.PromptSHA != "" && src.PromptSHA == c.PromptSHA && idPattern.MatchString(src.PromptSHA) {
		path := filepath.Join(a.dir, "prompts", src.PromptSHA+".txt")
		if _, err := os.Stat(path); err != nil {
			if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
				_ = writeFileAtomic(path, []byte(src.Prompt))
			}
		}
	}

	if !changed {
		return false, nil
	}
	e.Session, e.StartedAt, e.DurationS, e.Build = c.Session, c.StartedAt, c.DurationS, c.Build
	e.Turns, e.P50, e.P95, e.Max, e.Over = c.Summary.Turns, c.Summary.P50, c.Summary.P95, c.Summary.Max, c.Summary.Over
	e.Lines, e.PromptSHA, e.UpdatedAt = len(c.Transcript), c.PromptSHA, now
	e.Bytes = 0
	for _, f := range e.Files {
		e.Bytes += f.Bytes
	}
	manifest, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return changed, err
	}
	// The manifest goes last: a directory without one is a call that was
	// being written when something stopped, and is written again.
	if err := writeFileAtomic(filepath.Join(dir, archManifest), append(manifest, '\n')); err != nil {
		return changed, err
	}
	a.entries[c.ID] = e
	a.writeIndexLocked()
	return true, nil
}

// closedSession reports whether golive has finished with a stored session:
// its record says when it closed.
func closedSession(recordPath string) bool {
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return false
	}
	var rec struct {
		ClosedAt *time.Time `json:"closed_at"`
	}
	return json.Unmarshal(data, &rec) == nil && rec.ClosedAt != nil && !rec.ClosedAt.IsZero()
}

// writeIndexLocked rewrites index.jsonl: one line per call, newest first,
// without the per-file detail. It is a convenience; the manifests are the
// record.
func (a *Archive) writeIndexLocked() {
	entries := make([]*ArchiveEntry, 0, len(a.entries))
	for _, e := range a.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].StartedAt.After(entries[j].StartedAt) })
	var b strings.Builder
	for _, e := range entries {
		line := *e
		line.Files = nil
		data, err := json.Marshal(line)
		if err != nil {
			continue
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	_ = writeFileAtomic(filepath.Join(a.dir, "index.jsonl"), []byte(b.String()))
}

// Problem is something Verify found.
type Problem struct {
	ID   string `json:"id"`
	File string `json:"file"`
	What string `json:"what"`
}

// Verify reads every archived file again and compares it with its manifest.
func (a *Archive) Verify() (files int, bytes int64, problems []Problem) {
	for _, e := range a.Entries() {
		names := make([]string, 0, len(e.Files))
		for name := range e.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			want := e.Files[name]
			f, err := os.Open(filepath.Join(a.dir, e.Dir, name))
			if err != nil {
				problems = append(problems, Problem{e.ID, name, "missing"})
				continue
			}
			h := sha256.New()
			n, err := io.Copy(h, f)
			f.Close()
			files++
			bytes += n
			switch {
			case err != nil:
				problems = append(problems, Problem{e.ID, name, "unreadable: " + err.Error()})
			case n != want.Bytes:
				problems = append(problems, Problem{e.ID, name, fmt.Sprintf("%d bytes, the manifest says %d", n, want.Bytes)})
			case hex.EncodeToString(h.Sum(nil)) != want.SHA256:
				problems = append(problems, Problem{e.ID, name, "its contents have changed since it was archived"})
			}
		}
	}
	return files, bytes, problems
}

// TranscriptText is a call's conversation as a page to read.
func TranscriptText(c *Call) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Call %s\n", c.ID)
	fmt.Fprintf(&b, "%s, %s, build %s\n", c.StartedAt.Format("2006-01-02 15:04:05 -07:00"), clock(c.DurationS), c.Build)
	if st := c.Summary; st.Turns > 0 {
		fmt.Fprintf(&b, "%d answered turns; last sound to first audio: p50 %d ms, worst %d ms, %d over %d\n",
			st.Turns, st.P50, st.Max, st.Over, TargetMS)
	}
	b.WriteByte('\n')
	for _, line := range c.Transcript {
		who := "caller   "
		if line.Who == "assistant" {
			who = "assistant"
		}
		note := ""
		switch {
		case line.Kind == "greeting":
			note = " (opening line)"
		case line.Who == "assistant" && line.Turn > 0:
			note = fmt.Sprintf(" (turn %d, after %d ms)", line.Turn, line.TotalMS)
			if line.Interrupted {
				note = fmt.Sprintf(" (turn %d, after %d ms, cut short by the caller)", line.Turn, line.TotalMS)
			}
		case line.Who == "caller" && line.Turn == 0:
			note = " (not answered)"
		}
		// Stamped where the line begins: the caller's where they began to
		// speak, when the log says.
		at := line.TS
		if line.Who == "caller" && line.StartS > 0 && line.StartS < line.TS {
			at = line.StartS
		}
		fmt.Fprintf(&b, "[%s] %s%s  %s\n", clock(at), who, note, line.Text)
	}
	return b.String()
}

func clock(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	whole := int(seconds)
	return fmt.Sprintf("%02d:%02d.%d", whole/60, whole%60, int((seconds-float64(whole))*10))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// copyAtomic writes r to path through a temporary file, synced before it is
// given its name, and returns its size and hash.
func copyAtomic(path string, r io.Reader) (ArchiveFile, error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return ArchiveFile{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return ArchiveFile{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return ArchiveFile{}, err
	}
	return ArchiveFile{Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
