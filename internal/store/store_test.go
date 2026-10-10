package store

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

func constant(v int16, samples int) []byte {
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

func TestRecordingRoundTripsAsStereoOnOneTimeline(t *testing.T) {
	s, err := Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const id, rate = "sess_0123456789abcdef", 8000
	rec, err := s.Create(id, rate)
	if err != nil {
		t.Fatal(err)
	}

	// Half a second of caller, then the assistant from 250 ms to 750 ms: the
	// two overlap for a quarter of a second, as in a barge-in.
	rec.WriteInput(constant(1000, rate/2))
	rec.WriteOutput(constant(-2000, rate/4), 250)
	rec.WriteOutput(constant(-2000, rate/4), 300) // early: continues the last, never overwrites it

	meta := Meta{
		ID:      id,
		Session: live.SessionConfig{ID: id, Model: "golive-1", Store: true},
		History: []provider.Message{{Role: provider.RoleUser, Content: "你好"}},
	}
	if err := rec.Close(meta); err != nil {
		t.Fatal(err)
	}

	got, err := s.Meta(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rate != rate || len(got.History) != 1 || got.History[0].Content != "你好" || got.ClosedAt.IsZero() {
		t.Errorf("record read back as %+v", got)
	}

	var buf bytes.Buffer
	if err := s.WriteContent(id, &buf); err != nil {
		t.Fatal(err)
	}
	wav := buf.Bytes()
	if want, _ := s.ContentSize(id); int64(len(wav)) != want {
		t.Errorf("content is %d bytes but ContentSize said %d", len(wav), want)
	}
	if string(wav[0:4]) != "RIFF" || binary.LittleEndian.Uint16(wav[22:]) != 2 ||
		binary.LittleEndian.Uint32(wav[24:]) != rate {
		t.Fatalf("not a stereo WAV at %d Hz", rate)
	}
	body := wav[44:]
	frame := func(ms int) (left, right int16) {
		off := ms * rate / 1000 * 4
		return int16(binary.LittleEndian.Uint16(body[off:])), int16(binary.LittleEndian.Uint16(body[off+2:]))
	}
	if l, r := frame(100); l != 1000 || r != 0 {
		t.Errorf("at 100 ms: left=%d right=%d; want caller only", l, r)
	}
	if l, r := frame(400); l != 1000 || r != -2000 {
		t.Errorf("at 400 ms: left=%d right=%d; want both, overlapping", l, r)
	}
	if l, r := frame(700); l != 0 || r != -2000 {
		t.Errorf("at 700 ms: left=%d right=%d; want assistant only", l, r)
	}
	if got, want := len(body)/4, rate*3/4; got != want {
		t.Errorf("recording is %d frames; want %d (to the end of the longer track)", got, want)
	}
}

func TestStoreRefusesIdsThatAreNotSessionIds(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, 0)
	for _, id := range []string{"", "..", "../etc", "sess_../x", "sess_ZZZZZZZZ", "sess_12", "/abs"} {
		if _, err := s.Create(id, 8000); err == nil {
			t.Errorf("Create(%q) succeeded", id)
		}
		if _, err := s.Meta(id); err != ErrNotFound {
			t.Errorf("Meta(%q) = %v, want ErrNotFound", id, err)
		}
		if err := s.WriteContent(id, &bytes.Buffer{}); err != ErrNotFound {
			t.Errorf("WriteContent(%q) = %v, want ErrNotFound", id, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a refused id still created %v", entries)
	}
}

func TestSweepRemovesOnlyWhatHasExpired(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, time.Hour)
	mk := func(id string, age time.Duration) {
		rec, err := s.Create(id, 8000)
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.Close(Meta{ID: id}); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		_ = os.Chtimes(filepath.Join(dir, id, metaFile), old, old)
	}
	mk("sess_aaaaaaaaaaaaaaaa", 2*time.Hour)
	mk("sess_bbbbbbbbbbbbbbbb", time.Minute)

	if n := s.Sweep(); n != 1 {
		t.Errorf("swept %d sessions, want 1", n)
	}
	if _, err := s.Meta("sess_aaaaaaaaaaaaaaaa"); err != ErrNotFound {
		t.Error("the expired session is still there")
	}
	if _, err := s.Meta("sess_bbbbbbbbbbbbbbbb"); err != nil {
		t.Errorf("the fresh session was removed: %v", err)
	}
}
