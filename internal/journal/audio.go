package journal

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// A stored call is two mono PCM16 tracks on one timeline (internal/store):
// the caller as golive received them, the assistant as golive sent it.
const (
	inputTrack  = "input.pcm"
	outputTrack = "output.pcm"
	recordFile  = "session.json"
)

// Recording is a stored call's audio, opened for reading.
type Recording struct {
	in, out *os.File
	// wav is the recording as the archive keeps it: one stereo file, caller
	// left, assistant right. A recording is either the two tracks or this.
	wav  *os.File
	Rate int
	// Frames is the length of the longer track, in samples.
	Frames int64
	// caller, when set, is where each stretch of the caller's side belongs on
	// the call's clock: see Realign.
	caller   *Alignment
	ringback *ringbackAudio
}

// Alignment puts the caller's side of a recording back on the call's clock.
//
// The recorder used to append the caller's audio as it arrived. A phone
// bridge that delivers more audio than time has passed — two frames in a
// hundred, on evening calls — then leaves a caller's track that runs late
// against the assistant's: six seconds by the end of five minutes, and a
// question heard after its answer. The recorder now leaves out the repeated
// frames of a quiet line while the track is ahead. An Alignment does the same
// for a recording made before that, from the log's marks of how much audio had
// arrived by when: a list of which stretch of the track goes where.
type Alignment struct {
	runs []alignedRun
	// Frames is the caller's side's length once aligned, in samples, and
	// Dropped how many samples of repeated frames were left out.
	Frames, Dropped int64
}

// alignedRun says that length samples of the track, from src, belong at out.
type alignedRun struct{ out, src, length int64 }

// DroppedS is how much of the caller's track was left out, in seconds.
func (r *Recording) DroppedS() float64 {
	if r.caller == nil {
		return 0
	}
	return float64(r.caller.Dropped) / float64(r.Rate)
}

// Aligned returns the alignment in use, for a caller that wants to keep it.
func (r *Recording) Aligned() *Alignment { return r.caller }

// Use puts an alignment worked out earlier on a recording just opened.
func (r *Recording) Use(a *Alignment) {
	if a == nil {
		return
	}
	r.caller = a
	r.Frames = max(a.Frames, r.assistantEnd())
}

// Realign works out where the caller's audio belongs from marks of how much
// had arrived by when, and reads the recording that way from then on. It
// leaves the recording as it is, and reports false, when the marks do not
// describe it or there is nothing to put right.
func (r *Recording) Realign(marks []AudioMark) bool {
	if len(marks) < 3 || r.Rate <= 0 || r.Frames == 0 {
		return false
	}
	frame := int64(marks[0].Bytes / 2)
	if frame <= 0 || marks[0].Bytes%2 != 0 || marks[0].Frames < 1 {
		return false
	}
	for _, m := range marks {
		if int64(m.Bytes/2) != frame {
			return false // chunks of more than one size: the marks cannot place them
		}
	}
	// arrived says when chunk n (the first is 1) arrived, in samples of the
	// call's clock: between marks in proportion, past the last at its pace.
	arrived := func(n int64) float64 {
		i := sort.Search(len(marks), func(i int) bool { return int64(marks[i].Frames) >= n })
		if i == 0 {
			i = 1
		}
		if i >= len(marks) {
			i = len(marks) - 1
		}
		a, b := marks[i-1], marks[i]
		if b.Frames == a.Frames {
			return b.TS * float64(r.Rate)
		}
		t := a.TS + (b.TS-a.TS)*float64(n-int64(a.Frames))/float64(b.Frames-a.Frames)
		return t * float64(r.Rate)
	}
	// The first chunk was written where the clock stood, unless that was
	// within a fifth of a second of the start, where it was simply appended.
	origin := int64(arrived(1)) - frame
	if origin <= int64(r.Rate/5) {
		origin = 0
	}
	chunks := (r.Frames - origin) / frame
	if chunks < 2 {
		return false
	}
	gap, ahead := int64(r.Rate/5), int64(r.Rate/12)
	a := &Alignment{}
	prev, cur := make([]byte, frame*2), make([]byte, frame*2)
	pos := origin
	for n := int64(0); n < chunks; n++ {
		src := origin + n*frame
		r.readRaw(false, cur, src)
		due := int64(arrived(n+1)) - frame
		if due-pos > gap {
			pos = due
		}
		if n > 0 && pos-due > ahead && bytes.Equal(cur, prev) {
			a.Dropped += frame
			prev, cur = cur, prev
			continue
		}
		if k := len(a.runs); k > 0 && a.runs[k-1].out+a.runs[k-1].length == pos && a.runs[k-1].src+a.runs[k-1].length == src {
			a.runs[k-1].length += frame
		} else {
			a.runs = append(a.runs, alignedRun{out: pos, src: src, length: frame})
		}
		pos += frame
		prev, cur = cur, prev
	}
	// Nothing to put right, or far more than a late track explains: the marks
	// and the recording are not about the same audio.
	if a.Dropped == 0 || a.Dropped > r.Frames/5 {
		return false
	}
	a.Frames = pos
	r.Use(a)
	return true
}

// assistantEnd is where the assistant's side really ends: past it the track
// is silence that was only ever there to match the caller's length.
func (r *Recording) assistantEnd() int64 {
	buf := make([]byte, 1<<15)
	for end := r.Frames; end > 0; {
		n := int64(len(buf) / 2)
		if n > end {
			n = end
		}
		chunk := buf[:n*2]
		r.readRaw(true, chunk, end-n)
		for i := len(chunk) - 2; i >= 0; i -= 2 {
			if chunk[i] != 0 || chunk[i+1] != 0 {
				return end - n + int64(i/2) + 1
			}
		}
		end -= n
	}
	return 0
}

// readRaw fills buf with one side's samples from first, as the recording
// holds them, with silence where there is none.
func (r *Recording) readRaw(assistant bool, buf []byte, first int64) {
	clear(buf)
	if first < 0 {
		return
	}
	if r.wav != nil {
		both := make([]byte, len(buf)*2)
		readAt(r.wav, both, wavHeaderBytes+first*4)
		ch := 0
		if assistant {
			ch = 2
		}
		for i := 0; i < len(buf); i += 2 {
			buf[i], buf[i+1] = both[i*2+ch], both[i*2+ch+1]
		}
		return
	}
	if assistant {
		readAt(r.out, buf, first*2)
	} else {
		readAt(r.in, buf, first*2)
	}
}

// readSide fills buf with one side's samples from first on the call's clock:
// the assistant's as recorded, the caller's through the alignment when there
// is one.
func (r *Recording) readSide(assistant bool, buf []byte, first int64) {
	if !assistant && r.ringback != nil {
		defer r.ringback.overlay(buf, first)
	}
	if assistant || r.caller == nil {
		r.readRaw(assistant, buf, first)
		return
	}
	clear(buf)
	runs := r.caller.runs
	want := int64(len(buf) / 2)
	i := sort.Search(len(runs), func(i int) bool { return runs[i].out+runs[i].length > first })
	for ; i < len(runs) && runs[i].out < first+want; i++ {
		from := max(runs[i].out, first)
		to := min(runs[i].out+runs[i].length, first+want)
		r.readRaw(false, buf[(from-first)*2:(to-first)*2], runs[i].src+from-runs[i].out)
	}
}

// OpenArchivedWAV opens a recording the archive wrote: the stereo WAV that
// Recording.WAV produces, and nothing else.
func OpenArchivedWAV(path string) (*Recording, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, os.ErrNotExist
	}
	head := make([]byte, wavHeaderBytes)
	info, serr := f.Stat()
	if _, err := io.ReadFull(f, head); err != nil || serr != nil ||
		string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" ||
		binary.LittleEndian.Uint16(head[22:]) != 2 || binary.LittleEndian.Uint16(head[34:]) != 16 {
		f.Close()
		return nil, errors.New("journal: not a recording the archive wrote")
	}
	rate := int(binary.LittleEndian.Uint32(head[24:]))
	if rate <= 0 {
		f.Close()
		return nil, errors.New("journal: a recording with no sample rate")
	}
	return &Recording{wav: f, Rate: rate, Frames: (info.Size() - wavHeaderBytes) / 4}, nil
}

// OpenRecording opens the recording of one session in a store directory. It
// returns os.ErrNotExist when there is none: most calls in the journal have
// no audio, because recording is something the owner turns on, and what is
// recorded expires.
func OpenRecording(storeDir, session string) (*Recording, error) {
	if storeDir == "" || session == "" {
		return nil, os.ErrNotExist
	}
	dir := filepath.Join(storeDir, session)
	in, err := os.Open(filepath.Join(dir, inputTrack))
	if err != nil {
		return nil, os.ErrNotExist
	}
	out, err := os.Open(filepath.Join(dir, outputTrack))
	if err != nil {
		in.Close()
		return nil, os.ErrNotExist
	}
	r := &Recording{in: in, out: out, Rate: 16000}
	if data, err := os.ReadFile(filepath.Join(dir, recordFile)); err == nil {
		var meta struct {
			Rate int `json:"rate"`
		}
		if json.Unmarshal(data, &meta) == nil && meta.Rate > 0 {
			r.Rate = meta.Rate
		}
	}
	for _, f := range []*os.File{in, out} {
		if info, err := f.Stat(); err == nil && info.Size()/2 > r.Frames {
			r.Frames = info.Size() / 2
		}
	}
	return r, nil
}

// Close releases the files.
func (r *Recording) Close() {
	if r.wav != nil {
		r.wav.Close()
		return
	}
	r.in.Close()
	r.out.Close()
}

// DurationS is the recording's length in seconds.
func (r *Recording) DurationS() float64 { return float64(r.Frames) / float64(r.Rate) }

const wavHeaderBytes = 44

// WAV returns the recording as a stereo WAV — caller left, assistant right —
// that can be read from any offset, which is what lets a browser seek in it
// without the file ever being written out a second time.
func (r *Recording) WAV() io.ReadSeeker {
	if r.wav != nil && r.caller == nil && r.ringback == nil {
		return io.NewSectionReader(r.wav, 0, wavHeaderBytes+r.Frames*4)
	}
	return &stereoReader{rec: r, header: wavHeader(r.Rate, 2, r.Frames), size: wavHeaderBytes + r.Frames*4}
}

// wavHeader is the 44 bytes in front of 16-bit PCM.
func wavHeader(rate, channels int, frames int64) []byte {
	data := frames * int64(channels) * 2
	h := make([]byte, wavHeaderBytes)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+data))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(data))
	return h
}

// Side returns one side of the call alone, as a mono WAV: the caller as golive
// received them, or the assistant as golive sent it. The samples are the ones
// stored, untouched, on the call's own timeline — so both sides are the same
// length, the shorter one padded with silence — and it reads the same whether
// the recording is still golive's two tracks or the archive's one file.
func (r *Recording) Side(assistant bool) io.ReadSeeker {
	return &sideReader{rec: r, assistant: assistant, header: wavHeader(r.Rate, 1, r.Frames), size: wavHeaderBytes + r.Frames*2}
}

type sideReader struct {
	rec       *Recording
	assistant bool
	header    []byte
	size, pos int64
}

func (s *sideReader) Seek(offset int64, whence int) (int64, error) {
	return seekIn(&s.pos, s.size, offset, whence)
}

func (s *sideReader) Read(p []byte) (int, error) {
	if s.pos >= s.size {
		return 0, io.EOF
	}
	n := 0
	if s.pos < wavHeaderBytes {
		n = copy(p, s.header[s.pos:])
		s.pos += int64(n)
		if n == len(p) {
			return n, nil
		}
	}
	// Frames of two bytes from here on; a read may begin or end inside one.
	want := int64(len(p) - n)
	if rest := s.size - s.pos; want > rest {
		want = rest
	}
	off := s.pos - wavHeaderBytes
	first, last := off/2, (off+want+1)/2
	frames := last - first
	mono := make([]byte, frames*2)
	s.rec.readSide(s.assistant, mono, first)
	c := copy(p[n:], mono[off-first*2:off-first*2+want])
	s.pos += int64(c)
	return n + c, nil
}

// seekIn is io.Seeker for a reader that keeps its own position.
func seekIn(pos *int64, size, offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += *pos
	case io.SeekEnd:
		offset += size
	default:
		return 0, errors.New("journal: bad whence")
	}
	if offset < 0 {
		return 0, errors.New("journal: seek before the start")
	}
	*pos = offset
	return offset, nil
}

type stereoReader struct {
	rec    *Recording
	header []byte
	size   int64
	pos    int64
}

func (s *stereoReader) Seek(offset int64, whence int) (int64, error) {
	return seekIn(&s.pos, s.size, offset, whence)
}

func (s *stereoReader) Read(p []byte) (int, error) {
	if s.pos >= s.size {
		return 0, io.EOF
	}
	n := 0
	if s.pos < wavHeaderBytes {
		n = copy(p, s.header[s.pos:])
		s.pos += int64(n)
		if n == len(p) {
			return n, nil
		}
	}
	// From here on the stream is frames of four bytes: left sample, right
	// sample. A read may begin and end anywhere inside one.
	want := int64(len(p) - n)
	if rest := s.size - s.pos; want > rest {
		want = rest
	}
	off := s.pos - wavHeaderBytes
	first, last := off/4, (off+want+3)/4
	frames := last - first
	left, right := make([]byte, frames*2), make([]byte, frames*2)
	s.rec.readSide(false, left, first)
	s.rec.readSide(true, right, first)
	mixed := make([]byte, frames*4)
	for i := int64(0); i < frames; i++ {
		mixed[i*4], mixed[i*4+1] = left[i*2], left[i*2+1]
		mixed[i*4+2], mixed[i*4+3] = right[i*2], right[i*2+1]
	}
	c := copy(p[n:], mixed[off-first*4:off-first*4+want])
	s.pos += int64(c)
	return n + c, nil
}

// readAt fills buf from f at off, leaving silence where the track is shorter.
func readAt(f *os.File, buf []byte, off int64) {
	for done := 0; done < len(buf); {
		n, err := f.ReadAt(buf[done:], off+int64(done))
		done += n
		if err != nil {
			return
		}
	}
}

// Peaks is a recording reduced to something a page can draw: for each of n
// slices of the call, how loud each side got, 0 to 1000.
type Peaks struct {
	Rate      int     `json:"rate"`
	DurationS float64 `json:"duration_s"`
	Caller    []int   `json:"caller"`
	Assistant []int   `json:"assistant"`
}

// Peaks computes the waveform outline in n slices.
func (r *Recording) Peaks(n int) Peaks {
	if n < 10 {
		n = 10
	}
	if n > 8000 {
		n = 8000
	}
	p := Peaks{Rate: r.Rate, DurationS: r.DurationS(), Caller: make([]int, n), Assistant: make([]int, n)}
	if r.Frames == 0 {
		return p
	}
	track := func(assistant bool, out []int) {
		buf := make([]byte, 1<<16)
		for frame := int64(0); frame < r.Frames; frame += int64(len(buf) / 2) {
			chunk := buf
			if rest := (r.Frames - frame) * 2; rest < int64(len(chunk)) {
				chunk = chunk[:rest]
			}
			r.readSide(assistant, chunk, frame)
			for i := 0; i < len(chunk); i += 2 {
				v := int(int16(binary.LittleEndian.Uint16(chunk[i:])))
				if v < 0 {
					v = -v
				}
				slot := int((frame + int64(i/2)) * int64(n) / r.Frames)
				if slot >= n {
					slot = n - 1
				}
				if v > out[slot] {
					out[slot] = v
				}
			}
		}
		for i, v := range out {
			out[i] = v * 1000 / 32768
		}
	}
	track(false, p.Caller)
	track(true, p.Assistant)
	return p
}
