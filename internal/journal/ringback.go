package journal

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
)

// Carrier ringback is an overlay on the caller channel, never an ASR input or
// a replacement for the original engine audio. Keep absolute timestamps so
// the adapter's earlier start does not shift conversation/latency markers.
type ringbackAudio struct {
	pcm    []byte
	offset int64
}

func (r *ringbackAudio) overlay(dst []byte, first int64) {
	from := max(first, r.offset)
	to := min(first+int64(len(dst)/2), r.offset+int64(len(r.pcm)/2))
	if to > from {
		copy(dst[(from-first)*2:(to-first)*2], r.pcm[(from-r.offset)*2:(to-r.offset)*2])
	}
}

func (s *Server) attachRingback(c *Call, rec *Recording) {
	dir := ""
	if p := s.archive.Path(c.ID, "ringback.json"); p != "" && fileExists(p) {
		dir = filepath.Dir(p)
	} else if s.cfg.AdapterRecordDir != "" && idPattern.MatchString(c.ID) {
		p := filepath.Join(s.cfg.AdapterRecordDir, c.ID)
		if fileExists(filepath.Join(p, "summary.json")) {
			dir = p
		}
	}
	if dir == "" || rec.Rate <= 0 || rec.Rate > 48000 || c.StartedAt.IsZero() {
		return
	}
	f, err := os.Open(filepath.Join(dir, "ringback.json"))
	if err != nil {
		return
	}
	var meta struct {
		Start time.Time `json:"started_at"`
		End   time.Time `json:"ended_at"`
	}
	err = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&meta)
	f.Close()
	if err != nil || meta.Start.IsZero() || !meta.End.After(meta.Start) || meta.End.Sub(meta.Start) > 3*time.Minute {
		return
	}
	if !c.AnsweredAt.IsZero() && c.AnsweredAt.Before(meta.End) {
		meta.End = c.AnsweredAt
	}
	if !meta.End.After(meta.Start) {
		return
	}
	f, err = os.Open(filepath.Join(dir, "ringback.wav"))
	if err != nil {
		return
	}
	const limit = 44 + 3*60*8000*2
	wav, err := io.ReadAll(io.LimitReader(f, limit+1))
	f.Close()
	if err != nil || len(wav) < 44 || len(wav) > limit || (len(wav)-44)%2 != 0 ||
		string(wav[:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " ||
		binary.LittleEndian.Uint32(wav[16:20]) != 16 ||
		binary.LittleEndian.Uint16(wav[20:22]) != 1 || binary.LittleEndian.Uint16(wav[22:24]) != 1 ||
		binary.LittleEndian.Uint32(wav[24:28]) != 8000 || binary.LittleEndian.Uint16(wav[34:36]) != 16 ||
		string(wav[36:40]) != "data" || int(binary.LittleEndian.Uint32(wav[40:44])) != len(wav)-44 {
		return
	}
	frames := min(int64((len(wav)-44)/2), int64(meta.End.Sub(meta.Start))*8000/int64(time.Second))
	pcm, err := audio.ResamplePCM16(wav[44:44+frames*2], 8000, rec.Rate)
	if err != nil || len(pcm) == 0 {
		return
	}
	// Both clocks originate on the box. Reject corrupt metadata rather than
	// extending an otherwise valid recording by an arbitrary amount.
	delta := meta.Start.Sub(c.StartedAt)
	if delta < -30*time.Second || delta > 30*time.Second {
		return
	}
	offset := int64(delta) * int64(rec.Rate) / int64(time.Second)
	rec.ringback = &ringbackAudio{pcm: pcm, offset: offset}
	rec.Frames = max(rec.Frames, offset+int64(len(pcm)/2))
}

// The engine may finish a few milliseconds before the adapter closes its WAVs.
// Do not mark the archive settled while that final recording is still pending.
func (s *Server) ringbackPending(c *Call) bool {
	if s.cfg.AdapterRecordDir == "" || !idPattern.MatchString(c.ID) {
		return false
	}
	dir := filepath.Join(s.cfg.AdapterRecordDir, c.ID)
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	if !fileExists(filepath.Join(dir, "summary.json")) {
		return time.Since(c.EndedAt) < 5*time.Minute
	}
	if !fileExists(filepath.Join(dir, "ringback.json")) {
		return false
	}
	e := s.archive.Entry(c.ID)
	return e == nil || e.Files["ringback.wav"].Bytes == 0 || e.Files["ringback.json"].Bytes == 0
}
