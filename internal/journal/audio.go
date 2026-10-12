package journal

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	Rate    int
	// Frames is the length of the longer track, in samples.
	Frames int64
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
	data := r.Frames * 4
	h := make([]byte, wavHeaderBytes)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+data))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 2) // channels
	binary.LittleEndian.PutUint32(h[24:], uint32(r.Rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(r.Rate*4))
	binary.LittleEndian.PutUint16(h[32:], 4)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(data))
	return &stereoReader{rec: r, header: h, size: wavHeaderBytes + data}
}

type stereoReader struct {
	rec    *Recording
	header []byte
	size   int64
	pos    int64
}

func (s *stereoReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += s.pos
	case io.SeekEnd:
		offset += s.size
	default:
		return 0, errors.New("journal: bad whence")
	}
	if offset < 0 {
		return 0, errors.New("journal: seek before the start")
	}
	s.pos = offset
	return offset, nil
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
	readAt(s.rec.in, left, first*2)
	readAt(s.rec.out, right, first*2)
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
	track := func(f *os.File, out []int) {
		buf := make([]byte, 1<<16)
		var frame int64
		for {
			got, err := f.ReadAt(buf, frame*2)
			got -= got % 2
			for i := 0; i < got; i += 2 {
				v := int(int16(binary.LittleEndian.Uint16(buf[i:])))
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
			frame += int64(got / 2)
			if err != nil || got == 0 {
				break
			}
		}
		for i, v := range out {
			out[i] = v * 1000 / 32768
		}
	}
	track(r.in, p.Caller)
	track(r.out, p.Assistant)
	return p
}
