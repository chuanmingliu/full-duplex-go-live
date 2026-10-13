// Package store keeps sessions that asked to be kept.
//
// gpt-live-1 lets a session opt in with store:true, and two things then become
// possible afterwards: the whole call can be downloaded as one stereo
// recording, caller on the left and assistant on the right, and a new session
// can be forked from where the old one stopped. Both need the session to have
// left something behind, and this is where it leaves it — a directory per
// session holding the two audio tracks and a small JSON record of the
// conversation.
//
// The tracks are written as they happen, as raw PCM at fixed offsets, so a
// call that ends with the process still has its audio. Silence is never
// written: a gap is a hole in the file, which reads back as zeros and on most
// filesystems costs nothing to keep.
package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// ErrNotFound means there is no stored session with that id.
var ErrNotFound = errors.New("store: no such session")

// idPattern is what a session id looks like. Ids arrive in URLs and become
// directory names, so anything else is refused before it touches the disk.
var idPattern = regexp.MustCompile(`^sess_[0-9a-f]{8,64}$`)

// ValidID reports whether id is a well-formed session id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

const (
	metaFile   = "session.json"
	inputFile  = "input.pcm"
	outputFile = "output.pcm"
	// maxTrackBytes bounds one track: two hours at 48 kHz. A session cannot
	// outlive session_max_seconds, but a recording must not be able to fill a
	// disk because a clock was wrong.
	maxTrackBytes = 48000 * 2 * 60 * 60 * 2
)

// Meta is what a stored session records about itself.
type Meta struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ClosedAt  time.Time `json:"closed_at,omitempty"`
	// Rate is the sample rate of both audio tracks, which are linear PCM16
	// whatever format the session used on the wire.
	Rate int `json:"rate"`
	// InputOnClock says the caller's track was kept on the session clock as it
	// was written: a recording without it may have a caller's side that runs
	// late against the assistant's (see WriteInput). InputDroppedMS is how
	// much repeated audio was left out to do it.
	InputOnClock   bool  `json:"input_on_clock,omitempty"`
	InputDroppedMS int64 `json:"input_dropped_ms,omitempty"`
	// Session is the configuration the session ran with.
	Session live.SessionConfig `json:"session"`
	// History is the conversation as the backend was shown it, which is what a
	// fork continues from.
	History []provider.Message `json:"history,omitempty"`
}

// Store is a directory of stored sessions.
type Store struct {
	dir string
	ttl time.Duration
}

// Open prepares dir, creating it if needed. A ttl of zero keeps sessions until
// something else removes them.
func Open(dir string, ttl time.Duration) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: no directory configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Store{dir: dir, ttl: ttl}, nil
}

func (s *Store) path(id string) (string, error) {
	if !ValidID(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.dir, id), nil
}

// Recording is one session being written.
type Recording struct {
	dir  string
	rate int

	mu       sync.Mutex
	in, out  *os.File
	inPos    int64
	outPos   int64
	started  time.Time
	closed   bool
	writeErr error

	// lastIn is the last caller chunk written, and inDropped the bytes of
	// repeated chunks left out to keep the track on the clock.
	lastIn    []byte
	inDropped int64
	// now is the clock; a test's own when it needs one.
	now func() time.Time
}

func (r *Recording) since() time.Duration {
	if r.now != nil {
		return r.now().Sub(r.started)
	}
	return time.Since(r.started)
}

// Create starts recording a session at the given PCM16 sample rate.
func (s *Store) Create(id string, rate int) (*Recording, error) {
	dir, err := s.path(id)
	if err != nil {
		return nil, fmt.Errorf("store: %q is not a session id", id)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	in, err := os.OpenFile(filepath.Join(dir, inputFile), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	out, err := os.OpenFile(filepath.Join(dir, outputFile), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		in.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Recording{dir: dir, rate: rate, in: in, out: out, started: time.Now()}, nil
}

// bytesAt converts a time on the session clock to a whole-sample byte offset.
func (r *Recording) bytesAt(d time.Duration) int64 {
	samples := int64(d.Seconds() * float64(r.rate))
	return samples * 2
}

// WriteInput appends caller audio.
//
// It lands where the session clock says it belongs, so a caller who stops
// sending for a while — a muted client, a stalled network — leaves silence of
// the right length rather than a recording that has quietly lost time. Audio
// that arrives a little early or late is simply appended: the microphone's
// clock and this machine's are not the same clock, and placing every frame by
// wall time would open a gap or an overlap at each one.
//
// A sender can also deliver more audio than time has passed. A phone bridge
// did, by two frames in a hundred on evening calls: appended as it came, the
// caller's side of a five-minute call ended six seconds behind the
// assistant's, and a question was heard after its answer. So while the track
// is ahead of the clock, a chunk that is the previous chunk over again — the
// frame a quiet line repeats — is left out. Nothing that differs from what
// came before it is ever dropped: speech is kept whole, and the track finds
// the clock again in the next pause.
func (r *Recording) WriteInput(pcm []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(pcm) == 0 {
		return
	}
	pos := r.inPos
	now := r.bytesAt(r.since()) - int64(len(pcm))
	// More than a fifth of a second adrift is a gap, not jitter.
	if now-pos > int64(r.rate*2/5) {
		pos = now
	}
	// More than a twelfth of a second ahead is not jitter either.
	if pos-now > int64(r.rate*2/12) && bytes.Equal(pcm, r.lastIn) {
		r.inDropped += int64(len(pcm))
		return
	}
	r.lastIn = append(r.lastIn[:0], pcm...)
	r.inPos = r.writeAt(r.in, pcm, pos)
}

// WriteOutput places assistant audio at startMS on the session clock: the
// moment the listener hears it. Audio is never placed before audio already
// written, so a turn that follows another hard on its heels continues it.
func (r *Recording) WriteOutput(pcm []byte, startMS int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(pcm) == 0 {
		return
	}
	pos := r.bytesAt(time.Duration(startMS) * time.Millisecond)
	if pos < r.outPos {
		pos = r.outPos
	}
	r.outPos = r.writeAt(r.out, pcm, pos)
}

func (r *Recording) writeAt(f *os.File, pcm []byte, pos int64) int64 {
	if pos < 0 {
		pos = 0
	}
	pos -= pos % 2
	if pos+int64(len(pcm)) > maxTrackBytes {
		return pos
	}
	if _, err := f.WriteAt(pcm, pos); err != nil {
		if r.writeErr == nil {
			r.writeErr = err
		}
		return pos
	}
	return pos + int64(len(pcm))
}

// Snapshot writes the session record while the recording is still open, so a
// live session can be forked.
func (r *Recording) Snapshot(meta Meta) error {
	meta.Rate = r.rate
	r.mu.Lock()
	meta.InputOnClock = true
	if r.rate > 0 {
		meta.InputDroppedMS = r.inDropped * 1000 / int64(r.rate*2)
	}
	r.mu.Unlock()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = r.started
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.dir, metaFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.dir, metaFile))
}

// Close finishes the recording and writes the session record.
func (r *Recording) Close(meta Meta) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	errIn, errOut, writeErr := r.in.Close(), r.out.Close(), r.writeErr
	r.mu.Unlock()

	if meta.ClosedAt.IsZero() {
		meta.ClosedAt = time.Now()
	}
	if err := r.Snapshot(meta); err != nil {
		return err
	}
	return errors.Join(writeErr, errIn, errOut)
}

// Meta reads a stored session's record.
func (s *Store) Meta(id string) (Meta, error) {
	dir, err := s.path(id)
	if err != nil {
		return Meta{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, ErrNotFound
		}
		return Meta{}, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, fmt.Errorf("store: %s is corrupt: %w", id, err)
	}
	return meta, nil
}

// WriteContent streams a session's recording as a stereo WAV: the caller on
// the left channel, the assistant on the right, on one timeline. It is the
// layout gpt-live-1 uses, and the one that makes overlap visible — in a
// waveform editor a barge-in is the two channels sounding at once.
func (s *Store) WriteContent(id string, w io.Writer) error {
	dir, err := s.path(id)
	if err != nil {
		return err
	}
	meta, err := s.Meta(id)
	if err != nil {
		return err
	}
	in, err := os.Open(filepath.Join(dir, inputFile))
	if err != nil {
		return ErrNotFound
	}
	defer in.Close()
	out, err := os.Open(filepath.Join(dir, outputFile))
	if err != nil {
		return ErrNotFound
	}
	defer out.Close()

	inSize, outSize := size(in), size(out)
	frames := inSize
	if outSize > frames {
		frames = outSize
	}
	frames /= 2

	if _, err := w.Write(wavHeader(meta.Rate, 2, frames*4)); err != nil {
		return err
	}
	const chunkFrames = 8192
	left := make([]byte, chunkFrames*2)
	right := make([]byte, chunkFrames*2)
	mixed := make([]byte, chunkFrames*4)
	for done := int64(0); done < frames; {
		n := int64(chunkFrames)
		if frames-done < n {
			n = frames - done
		}
		readFull(in, left[:n*2])
		readFull(out, right[:n*2])
		for i := int64(0); i < n; i++ {
			mixed[i*4], mixed[i*4+1] = left[i*2], left[i*2+1]
			mixed[i*4+2], mixed[i*4+3] = right[i*2], right[i*2+1]
		}
		if _, err := w.Write(mixed[:n*4]); err != nil {
			return err
		}
		done += n
	}
	return nil
}

// ContentSize is the length in bytes WriteContent will produce, for a
// Content-Length header.
func (s *Store) ContentSize(id string) (int64, error) {
	dir, err := s.path(id)
	if err != nil {
		return 0, err
	}
	var frames int64
	for _, name := range []string{inputFile, outputFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return 0, ErrNotFound
		}
		if n := info.Size() / 2; n > frames {
			frames = n
		}
	}
	return 44 + frames*4, nil
}

// readFull fills buf from f, leaving zeros — silence — past the end of a
// shorter track.
func readFull(f *os.File, buf []byte) {
	n, _ := io.ReadFull(f, buf)
	for i := n; i < len(buf); i++ {
		buf[i] = 0
	}
}

func size(f *os.File) int64 {
	info, err := f.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

func wavHeader(rate, channels int, dataBytes int64) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
	return h
}

// Sweep removes sessions older than the store's ttl, and reports how many. A
// session's age is that of its record, so one still being written is safe.
func (s *Store) Sweep() int {
	if s.ttl <= 0 {
		return 0
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		dir := filepath.Join(s.dir, entry.Name())
		info, err := os.Stat(filepath.Join(dir, metaFile))
		if err != nil {
			// No record: either still being written, or abandoned by a crash.
			// Judge it by the directory, with a day's grace for the first.
			if dinfo, derr := os.Stat(dir); derr != nil || time.Since(dinfo.ModTime()) < s.ttl+24*time.Hour {
				continue
			}
		} else if time.Since(info.ModTime()) < s.ttl {
			continue
		}
		if os.RemoveAll(dir) == nil {
			removed++
		}
	}
	return removed
}
