package journal

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRingbackPlaybackAndArchive(t *testing.T) {
	root := t.TempDir()
	store, adapter := filepath.Join(root, "store"), filepath.Join(root, "adapter")
	start := time.Now().UTC().Add(-3 * time.Second).Truncate(time.Millisecond)
	c := &Call{ID: "ringback-test", Session: "session", StartedAt: start, AnsweredAt: start.Add(2 * time.Second), EndedAt: start.Add(3 * time.Second)}
	write := func(path string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pcm := func(n int, value uint16) []byte {
		b := make([]byte, n*2)
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint16(b[i*2:], value)
		}
		return b
	}
	write(filepath.Join(store, c.Session, inputTrack), pcm(48000, 100))
	write(filepath.Join(store, c.Session, outputTrack), pcm(48000, 200))
	write(filepath.Join(store, c.Session, recordFile), []byte(`{"rate":16000,"closed_at":"2026-10-31T13:00:03Z"}`))
	rdir := filepath.Join(adapter, c.ID)
	write(filepath.Join(rdir, "ringback.wav"), append(wavHeader(8000, 1, 20000), pcm(20000, 1000)...))
	meta, _ := json.Marshal(map[string]any{"started_at": start.Add(-100 * time.Millisecond), "ended_at": start.Add(2200 * time.Millisecond)})
	write(filepath.Join(rdir, "ringback.json"), meta)
	s := NewServer(Config{StoreDir: store, AdapterRecordDir: adapter, ArchiveDir: filepath.Join(root, "archive")})
	if !s.ringbackPending(c) {
		t.Fatal("unclosed adapter recording marked settled")
	}
	write(filepath.Join(rdir, "summary.json"), []byte(`{}`))
	check := func() {
		t.Helper()
		r, err := s.recording(c)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if r.ringback == nil {
			t.Fatal("missing ringback overlay")
		}
		b, err := io.ReadAll(r.WAV())
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 44+48000*4 {
			t.Fatal("ringback shifted the conversation timeline")
		}
		for _, n := range []int{0, 16000, 31999, 32000, 40000} {
			want := uint16(1000)
			if n >= 32000 {
				want = 100
			}
			if binary.LittleEndian.Uint16(b[44+n*4:]) != want || binary.LittleEndian.Uint16(b[46+n*4:]) != 200 {
				t.Fatalf("wrong overlay or channel at frame %d", n)
			}
		}
		raw, _, err := s.rawRecording(c)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		original, _ := io.ReadAll(raw.WAV())
		if binary.LittleEndian.Uint16(original[44:]) != 100 {
			t.Fatal("original engine recording modified")
		}
	}
	check()
	if _, err := s.archive.Put(c, Sources{StoreDir: store, AdapterRecordDir: adapter}); err != nil {
		t.Fatal(err)
	}
	if s.ringbackPending(c) {
		t.Fatal("complete archive still pending")
	}
	if _, _, problems := s.archive.Verify(); len(problems) > 0 {
		t.Fatalf("archive integrity: %+v", problems)
	}
	if err := os.RemoveAll(store); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(adapter); err != nil {
		t.Fatal(err)
	}
	check() // Original source expired: archival playback must still include ringing.
	if c.AnsweredAt != start.Add(2*time.Second) {
		t.Fatal("call metrics changed")
	}
}
