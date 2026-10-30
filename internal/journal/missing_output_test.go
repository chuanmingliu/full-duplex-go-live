package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func completeOutputBaseline() []Line {
	var lines []Line
	for i := 0; i < 20; i++ {
		lines = append(lines, latencyMetric(fmt.Sprintf("item_%d", i), nil))
	}
	return lines
}

func TestMissingOutputRecordIsAnUnverifiedResponseOverlay(t *testing.T) {
	lines := append(completeOutputBaseline(), transcriptEnabled(),
		transcriptSegment("item_20", 1, 1000, "synthetic reply"),
		transcriptSegment("item_20", 1, 1000, "synthetic reply"),
		transcriptSegment("item_20", 2, 1100, "continued reply"))
	c := latencyCall(t, lines...)
	before, _ := json.Marshal(c)
	s := NewServer(Config{})
	got := s.withLatencyEvidence(c)
	if got.Latency.Version != 4 || got.Latency.MissingOutputRecords != 1 || got.Latency.Unverified != 1 || got.Latency.FirstContent.N != 20 || *got.Latency.FirstContent.P95 != 880 {
		t.Fatalf("missing complete record was hidden or timing invented: %+v", got.Latency)
	}
	again := s.withLatencyEvidence(got)
	if again.Latency.Unverified != 1 || again.Latency.MissingOutputRecords != 1 {
		t.Fatalf("overlay counted twice: %+v", again.Latency)
	}
	after, _ := json.Marshal(c)
	if !bytes.Equal(before, after) || c.Latency.Unverified != 0 || c.Latency.MissingOutputRecords != 0 || c.Latency.Version != 3 {
		t.Fatal("response overlay mutated the persisted source")
	}
}

func TestMissingOutputRecordKeepsExplicitNonanswersAndLegacy(t *testing.T) {
	for name, extra := range map[string][]Line{
		"completed":   {latencyMetric("item_20", nil)},
		"backchannel": {transcriptLog(1500, "speak: the opener became a backchannel; the caller went on", map[string]string{"turn": "item_20"})},
		"greeting":    {transcriptLog(900, "speak: opening line", map[string]string{"turn": "item_20"})},
		"greet API":   {transcriptLog(900, "speak: greeting", map[string]string{"turn": "item_20"})},
	} {
		t.Run(name, func(t *testing.T) {
			lines := append(completeOutputBaseline(), transcriptSegment("item_20", 1, 1000, "synthetic"))
			c := latencyCall(t, append(lines, extra...)...)
			if got := NewServer(Config{}).withLatencyEvidence(c); got.Latency.Unverified != 0 || got.Latency.MissingOutputRecords != 0 {
				t.Fatalf("explicit nonanswer or complete output counted: %+v", got.Latency)
			}
		})
	}
	for name, extra := range map[string]Line{
		"standalone filler": transcriptSegment("bc_20", 1, 1000, "synthetic filler"),
		"legacy readiness":  legacySegment("item_20", "legacy synthesized text", 1000),
	} {
		t.Run(name, func(t *testing.T) {
			c := latencyCall(t, append(completeOutputBaseline(), extra)...)
			if got := NewServer(Config{}).withLatencyEvidence(c); got.Latency.Unverified != 0 || got.Latency.MissingOutputRecords != 0 {
				t.Fatalf("legacy/filler output counted: %+v", got.Latency)
			}
		})
	}
	lines := append(completeOutputBaseline(), transcriptSegment("item_20", 1, 1000, "synthetic reply"),
		transcriptLog(1500, "think: abandoning the speculative turn", map[string]string{"turn": "item_20"}))
	if got := NewServer(Config{}).withLatencyEvidence(latencyCall(t, lines...)); got.Latency.Unverified != 1 {
		t.Fatalf("generic abandonment hid already-started output: %+v", got.Latency)
	}
}

func TestMissingOutputRecordRecomputesArchiveWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	lines := append(completeOutputBaseline(), transcriptEnabled(), transcriptSegment("item_20", 1, 1000, "synthetic reply"))
	log := latencyLog(append([]Line{latencyStart("sess_latency", "call-latency")}, lines...))
	path := filepath.Join(dir, "source.log")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	c := latencyCall(t, lines...)
	c.EndedAt = latencyTestTime.Add(time.Minute)
	store, sessionDir := recordedFixture(t, true)
	if err := os.Rename(sessionDir, filepath.Join(store, c.Session)); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Config{ArchiveDir: filepath.Join(dir, "archive")})
	if _, err := s.archive.Put(c, Sources{LogPath: path, LogTo: int64(len(log)), StoreDir: store}); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{archCall, archTranscript, archLog, archAudio, archSession, archManifest} {
		p := filepath.Join(filepath.Dir(s.archive.Path(c.ID, archCall)), name)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = data
	}
	archived, err := s.archive.Call(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := s.withLatencyEvidence(archived)
	if got.Latency.Version != 4 || got.Latency.MissingOutputRecords != 1 || got.Latency.Unverified != 1 || !got.Latency.Recomputed || archived.Latency.Unverified != 0 {
		t.Fatalf("archive overlay missing or source mutated: %+v", got.Latency)
	}
	// A normal index refresh is still free to archive the original base view.
	if changed, err := s.archive.Put(c, Sources{}); err != nil || changed {
		t.Fatalf("normal refresh rewrote the archive: changed=%v err=%v", changed, err)
	}
	for p, want := range before {
		data, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(data, want) {
			t.Fatalf("source archive changed: %s", filepath.Base(p))
		}
	}
}

func TestMissingOutputRecordIgnoresInvalidSequenceBeforeAnswer(t *testing.T) {
	for name, sequence := range map[string]string{
		"overflow": "9223372036854775808", "long overflow": strings.Repeat("9", 5000),
		"non-ASCII": "\u0669", "negative": "-1", "fractional": "1.5", "infinite": "Inf", "boolean": "true",
	} {
		t.Run(name, func(t *testing.T) {
			filler := transcriptSegment("bc_fixture", 1, 1000, "synthetic filler")
			filler.kv["seq"] = sequence
			lines := append(completeOutputBaseline(), filler, transcriptSegment("item_20", 1, 1100, "synthetic answer"))
			got := NewServer(Config{}).withLatencyEvidence(latencyCall(t, lines...))
			if got.Latency.Unverified != 1 || got.Latency.MissingOutputRecords != 1 {
				t.Fatalf("invalid filler sequence hid the missing answer: %+v", got.Latency)
			}
		})
	}
}
