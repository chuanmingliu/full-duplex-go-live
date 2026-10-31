package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFillerEvidenceTracksActualOpenersAndTheirOutcome(t *testing.T) {
	ix := NewIndexer()
	log := `time=2026-10-29T10:00:00Z level=INFO msg="session started" session=s1 realtime=true call=call-one
time=2026-10-29T10:00:01Z level=DEBUG msg="speak: the opener fits the caller's words" session=s1 turn=item_1 scenario=price opener="价格嘛，"
time=2026-10-29T10:00:01.200Z level=DEBUG msg="speak: opened the answer from the phrase cache at the pause" session=s1 turn=item_1 text="价格嘛，" audio_ms=540
time=2026-10-29T10:00:01.300Z level=DEBUG msg="speak: the model's opener was already said at the pause" session=s1 turn=item_1 opener="价格嘛，"
time=2026-10-29T10:00:01.400Z level=INFO msg="speak: the opener became a backchannel; the caller went on" session=s1 turn=item_1 opener_ms=540 reason="caller continued"
time=2026-10-29T10:00:02Z level=DEBUG msg="speak: segment played from the phrase cache" session=s1 turn=item_2 text="完整的回答。" audio_ms=1000
time=2026-10-29T10:00:03Z level=DEBUG msg="speak: no opener; the floor is busy" session=s1 turn=item_3
time=2026-10-29T10:00:04Z level=DEBUG msg="speak: no opener in the cache yet" session=s1 text="好的，"
time=2026-10-29T10:00:05Z level=DEBUG msg="backchannel: holding the floor while the backend works" session=s1 turn=item_4 text="稍等一下。"
time=2026-10-29T10:00:06Z level=DEBUG msg="backchannel: dropped; the answer took the floor first" session=s1 turn=bc_1 text="稍等一下。"
time=2026-10-29T10:00:07Z level=INFO msg="session started" session=s2 realtime=true call=call-two
time=2026-10-29T10:00:08Z level=DEBUG msg="speak: opened the answer from the phrase cache at the pause" session=s2 turn=item_1 text="嗯，" audio_ms=440
`
	if err := ix.ReadLog(strings.NewReader(log)); err != nil {
		t.Fatal(err)
	}
	calls := ix.Calls()
	var c *Call
	for _, call := range calls {
		if call.ID == "call-one" {
			c = call
		}
	}
	if c == nil || c.FillerLogVersion != 1 || len(c.Fillers) != 5 {
		t.Fatalf("call %+v", c)
	}
	f := c.Fillers[0]
	if f.TS != 1.2 || f.Text != "价格嘛，" || f.Source != "phrase_cache" || f.Scenario != "price" || f.Outcome != "became_backchannel" || f.AudioMS != 540 || !f.ContinuationMatched {
		t.Fatalf("opener %+v", f)
	}
	if c.Fillers[1].Outcome != "skipped" || c.Fillers[1].Reason != "floor busy" || c.Fillers[2].Turn != "" || c.Fillers[2].Reason != "phrase not cached" {
		t.Fatalf("skips %+v", c.Fillers[1:3])
	}
	if c.Fillers[3].Source != "configured_phrase" || c.Fillers[3].Outcome != "requested" || c.Fillers[4].Outcome != "dropped" {
		t.Fatalf("unconfirmed playback %+v", c.Fillers[3:])
	}
	// Re-reading the finished index must not duplicate evidence.
	if len(ix.Calls()[1].Fillers) != 5 {
		t.Fatal("finishing duplicated filler events")
	}
}

func TestArchivedFillerEvidenceIsBackfilledWithoutChangingTheSavedCall(t *testing.T) {
	dir := t.TempDir()
	log := `time=2026-10-29T10:00:01Z level=DEBUG msg="speak: opened the answer from the phrase cache at the pause" session=other turn=item_1 text="别的通话，" audio_ms=440
time=2026-10-29T10:00:02Z level=DEBUG msg="speak: opened the answer from the phrase cache at the pause" session=ours turn=item_1 text="好的，" audio_ms=440
`
	path := filepath.Join(dir, "source.log")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-10-29T10:00:00Z")
	c := &Call{ID: "old-call", Session: "ours", StartedAt: start, EndedAt: start.Add(3 * time.Second)}
	s := NewServer(Config{ArchiveDir: filepath.Join(dir, "archive")})
	if _, err := s.archive.Put(c, Sources{LogPath: path, LogTo: int64(len(log))}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.archive.Path(c.ID, archCall))
	if err != nil {
		t.Fatal(err)
	}
	got := s.withFillerEvidence(c)
	if got.FillerLogVersion != 1 || len(got.Fillers) != 1 || got.Fillers[0].Text != "好的，" || got.Fillers[0].TS != 2 {
		t.Fatalf("backfilled %+v", got)
	}
	after, err := os.ReadFile(s.archive.Path(c.ID, archCall))
	if err != nil || string(before) != string(after) || c.FillerLogVersion != 0 || len(c.Fillers) != 0 {
		t.Fatal("backfill changed its source")
	}
	missing := *c
	missing.ID = "not-archived"
	if s.withFillerEvidence(&missing).FillerLogVersion != 0 {
		t.Fatal("missing evidence presented as an empty known log")
	}
}

func TestFillerSelectionAloneDoesNotClaimPlayback(t *testing.T) {
	cb := callBuild{}
	for _, raw := range []string{
		`time=2026-10-29T10:00:01Z level=DEBUG msg="speak: the opener fits the caller's words" turn=item_1 scenario=price opener="价格嘛，"`,
		`time=2026-10-29T10:00:02Z level=DEBUG msg="speak: the model's opener was already said at the pause" turn=item_1 opener="价格嘛，"`,
	} {
		l, _ := ParseLine(raw)
		cb.readFiller(l)
	}
	if len(cb.call.Fillers) != 0 {
		t.Fatalf("unobserved playback %+v", cb.call.Fillers)
	}
}

func TestPublishedFillerSnapshotDoesNotChangeWhenTheCallerContinues(t *testing.T) {
	ix := NewIndexer()
	for _, raw := range []string{
		`time=2026-10-29T10:00:00Z level=INFO msg="session started" session=s1 realtime=true call=call-one`,
		`time=2026-10-29T10:00:01Z level=DEBUG msg="speak: opened the answer from the phrase cache at the pause" session=s1 turn=item_1 text="嗯，" audio_ms=440`,
	} {
		l, _ := ParseLine(raw)
		ix.Feed(l)
	}
	old := ix.Calls()[0]
	l, _ := ParseLine(`time=2026-10-29T10:00:01.200Z level=INFO msg="speak: the opener became a backchannel; the caller went on" session=s1 turn=item_1 reason="caller continued"`)
	ix.Feed(l)
	if old.Fillers[0].Outcome != "queued" || ix.Calls()[0].Fillers[0].Outcome != "became_backchannel" {
		t.Fatal("later evidence mutated a published snapshot")
	}
}
