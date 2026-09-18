package segment

import (
	"strings"
	"testing"
)

// pushAll streams text one rune at a time, the way an LLM delivers it, and
// returns every segment plus the flushed remainder.
func pushAll(s *Segmenter, text string) []string {
	var out []string
	for _, r := range text {
		out = append(out, s.Push(string(r))...)
	}
	if rest := s.Flush(); rest != "" {
		out = append(out, rest)
	}
	return out
}

func TestFirstSegmentIsShort(t *testing.T) {
	s := New(Config{FirstChunkChars: 8, MinChunkChars: 24, MaxChunkChars: 120})
	segments := pushAll(s, "好的，我帮你查一下明天的天气。今天下午的会议也已经确认了，需要我一起提醒你吗？")

	if len(segments) < 2 {
		t.Fatalf("expected the reply to split, got %d segment(s): %q", len(segments), segments)
	}
	first := []rune(segments[0])
	if len(first) > 20 {
		t.Errorf("first segment is %d runes (%q); a long first segment delays the first syllable",
			len(first), segments[0])
	}
	if len(first) < 8 {
		t.Errorf("first segment is only %d runes (%q); below the configured floor", len(first), segments[0])
	}
}

func TestSegmentsReassembleToTheInput(t *testing.T) {
	s := New(DefaultConfig())
	input := "第一句话结束了。第二句话稍微长一点，里面还有逗号。第三句话是最后一句！"
	joined := strings.Join(pushAll(s, input), "")

	if joined != input {
		t.Errorf("segmentation lost or reordered text:\n got: %q\nwant: %q", joined, input)
	}
}

func TestDecimalsAndAbbreviationsDoNotSplit(t *testing.T) {
	s := New(Config{FirstChunkChars: 4, MinChunkChars: 8, MaxChunkChars: 200})
	segments := pushAll(s, "The reading was 3.5 degrees and Dr. Chen confirmed it this morning.")

	for _, seg := range segments {
		if strings.HasSuffix(strings.TrimSpace(seg), "3.") {
			t.Errorf("split inside a decimal: %q", seg)
		}
		if strings.HasSuffix(strings.TrimSpace(seg), "Dr.") {
			t.Errorf("split after an abbreviation: %q", seg)
		}
	}
}

func TestRunawayTextIsStillFlushed(t *testing.T) {
	s := New(Config{FirstChunkChars: 8, MinChunkChars: 16, MaxChunkChars: 40})
	// No punctuation at all: the segmenter must not buffer forever, or TTS
	// never starts and the user hears silence.
	input := strings.Repeat("一", 300)
	segments := pushAll(s, input)

	if len(segments) < 2 {
		t.Fatalf("300 runes without punctuation produced %d segment(s)", len(segments))
	}
	for _, seg := range segments {
		if len([]rune(seg)) > 120 {
			t.Errorf("segment of %d runes exceeds twice the configured max", len([]rune(seg)))
		}
	}
	if joined := strings.Join(segments, ""); joined != input {
		t.Errorf("runaway flush lost text: %d runes in, %d out", len([]rune(input)), len([]rune(joined)))
	}
}

func TestNormalizeStripsMarkupNotWords(t *testing.T) {
	cases := map[string]string{
		"**Important**: check the logs": "Important: check the logs",
		"- first item":                  "first item",
		"use `go test` now":             "use go test now",
		"## Heading":                    "Heading",
		"plain sentence":                "plain sentence",
		"line one\nline two":            "line one line two",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResetClearsPendingText(t *testing.T) {
	s := New(DefaultConfig())
	s.Push("这句话还没有说完")
	if s.Pending() == "" {
		t.Fatal("expected buffered text before reset")
	}
	s.Reset()
	if s.Pending() != "" {
		t.Errorf("Reset left %q buffered; an abandoned turn would leak into the next one", s.Pending())
	}
	// After a reset the next turn starts fresh, with first-segment behaviour.
	segments := pushAll(s, "好的，我明白了，这就去办。")
	if len(segments) == 0 {
		t.Fatal("no segment emitted after reset")
	}
	if strings.Contains(strings.Join(segments, ""), "这句话还没有说完") {
		t.Error("text from the abandoned turn leaked into the next one")
	}
}

// A pause marker split across two segments is sent as two task_continue
// messages and spoken as its fragments, so the segmenter has to treat it the
// way it already treats "3.5" and "Dr.".
func TestSegmenterNeverSplitsAPauseMarker(t *testing.T) {
	s := New(Config{FirstChunkChars: 4, MinChunkChars: 6, MaxChunkChars: 30})
	const reply = "好的<#0.40#>我先查一下这个航班。然后告诉你结果。"

	var segs []string
	for _, r := range reply {
		segs = append(segs, s.Push(string(r))...)
	}
	if rest := s.Flush(); rest != "" {
		segs = append(segs, rest)
	}

	for _, seg := range segs {
		if strings.Count(seg, "<#") != strings.Count(seg, "#>") {
			t.Fatalf("segment ends inside a pause marker: %q", seg)
		}
	}
	if joined := strings.Join(segs, ""); !strings.Contains(joined, "<#0.40#>") {
		t.Fatalf("marker did not survive segmentation: %q", joined)
	}
}

// Normalize strips '#' as markdown, and a pause marker is made of '#'. Left
// alone, it turns "<#0.40#>" into "<0.40>" and the synthesizer reads out the
// characters.
func TestNormalizeKeepsPauseMarkers(t *testing.T) {
	got := Normalize("**Right**<#0.40#>_here_ it is")
	if !strings.Contains(got, "<#0.40#>") {
		t.Fatalf("markdown stripping ate the marker: %q", got)
	}
	if strings.ContainsAny(got, "*_") {
		t.Fatalf("markdown survived: %q", got)
	}
	if got := Normalize("a # b"); strings.Contains(got, "#") {
		t.Fatalf("a bare hash is still markdown: %q", got)
	}
}
