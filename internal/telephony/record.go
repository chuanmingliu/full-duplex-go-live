package telephony

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
)

// The WAVs share a monotonic origin; silence preserves gaps between talk spurts.
// They measure the adapter boundary, not sound arriving at the handset.
type recorder struct {
	mu         sync.Mutex
	start      time.Time
	events     *os.File
	wav        [3]*os.File
	samples    [3]int64
	answered   time.Time
	codec      audio.Codec
	maxSamples int64
	err        error
}

func newRecorder(dir, codec string, limit time.Duration) (*recorder, error) {
	r := &recorder{start: time.Now(), codec: audio.CodecALaw, maxSamples: int64((limit + time.Minute).Seconds() * 8000)}
	if codec == "pcmu" {
		r.codec = audio.CodecMuLaw
	}
	var err error
	r.events, err = os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	for i, name := range []string{"outbound.wav", "inbound.wav", "ringback.wav"} {
		r.wav[i], err = os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			err = audio.WriteWAVTo(r.wav[i], nil, 8000, 1)
		}
		if err != nil {
			r.close()
			return nil, err
		}
	}
	return r, nil
}

func (r *recorder) event(kind string, fields map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	r.err = json.NewEncoder(r.events).Encode(map[string]any{"type": kind, "at": time.Now().UTC(), "elapsed_us": time.Since(r.start).Microseconds(), "data": fields})
}

func (r *recorder) audio(inbound bool, wire []byte, at time.Time) {
	i := 0
	if inbound {
		i = 1
	}
	r.writeAudio(i, wire, at)
}

func (r *recorder) ringback(wire []byte, at time.Time) { r.writeAudio(2, wire, at) }

func (r *recorder) markAnswered(at time.Time) {
	if r != nil {
		r.mu.Lock()
		r.answered = at
		r.mu.Unlock()
	}
}

func (r *recorder) writeAudio(i int, wire []byte, at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	pos := at.Sub(r.start).Microseconds() * 8 / 1000
	if pos < r.samples[i] {
		pos = r.samples[i]
	}
	if pos+int64(len(wire)) > r.maxSamples {
		r.err = errors.New("recording duration limit exceeded")
		return
	}
	pcm := r.codec.ToPCM16(wire)
	_, r.err = r.wav[i].WriteAt(pcm, 44+pos*2)
	r.samples[i] = pos + int64(len(wire))
}

func (r *recorder) failure() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *recorder) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.err
	for i, f := range r.wav {
		if f == nil {
			continue
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(36+r.samples[i]*2))
		_, e := f.WriteAt(b[:], 4)
		err = errors.Join(err, e)
		binary.LittleEndian.PutUint32(b[:], uint32(r.samples[i]*2))
		_, e = f.WriteAt(b[:], 40)
		err = errors.Join(err, e, f.Close())
	}
	if r.samples[2] > 0 {
		until := r.start.Add(time.Duration(r.samples[2]) * time.Second / 8000)
		if !r.answered.IsZero() && r.answered.Before(until) {
			until = r.answered
		}
		meta, _ := json.Marshal(map[string]any{"started_at": r.start, "ended_at": until})
		err = errors.Join(err, os.WriteFile(filepath.Join(filepath.Dir(r.wav[2].Name()), "ringback.json"), meta, 0600))
	}
	if r.events != nil {
		err = errors.Join(err, r.events.Close())
	}
	return err
}
