package duplex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

type readoutRepeatLLM struct {
	deltas    []provider.LLMDelta
	delivered chan struct{}
	release   chan struct{}
}

func (*readoutRepeatLLM) Name() string { return "readout-repeat-fixture" }
func (l *readoutRepeatLLM) Stream(ctx context.Context, _ provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	out := make(chan provider.LLMDelta)
	go func() {
		defer close(out)
		for _, delta := range l.deltas {
			select {
			case out <- delta:
			case <-ctx.Done():
				return
			}
		}
		close(l.delivered)
		select {
		case <-l.release:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

type readoutBackendResult struct {
	response, output, deltas, completedText, status string
	audioMS                                         float64
	firstToken                                      time.Time
}

func runReadoutBackend(t *testing.T, opening, readout string, deltas []provider.LLMDelta, abort string) readoutBackendResult {
	t.Helper()
	cfg := testConfig()
	cfg.Duplex.PlaybackPaced = false
	cfg.Duplex.PhraseCache = false
	cfg.Duplex.Opener = false
	c := newCollector(cfg.ClientRate)
	llm := &readoutRepeatLLM{deltas: deltas, delivered: make(chan struct{}), release: make(chan struct{})}
	tts := mock.NewTTS()
	tts.MSPerRune = 5
	e := New(Options{Cfg: cfg}, Deps{LLM: llm, TTS: tts, Emit: c.emit})
	e.ctx = context.Background()
	t.Cleanup(e.Close)
	turn := e.tracker.Begin("", false)
	turn.Opening, turn.Readout = opening, readout
	e.commitTurn(turn)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.backendPending.Add(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.runBackend(ctx, cancel, backendJob{turn: turn, delegationID: "fixture", responseID: "fixture"}, provider.LLMRequest{})
	}()
	if abort != "" {
		awaitHandoffSignal(t, llm.delivered)
		waitFor(t, "raw first-token timing", time.Second, func() bool { return !turn.Timings().FirstToken.IsZero() })
		if abort == "cancel" {
			cancel()
		} else {
			e.tracker.Begin("superseding turn", false)
		}
	}
	close(llm.release)
	awaitHandoffSignal(t, done)
	pipe := e.speech.Load()
	if pipe == nil {
		t.Fatal("backend did not create its speech pipe")
	}
	awaitHandoffSignal(t, pipe.done)
	drainTranscriptPlayer(e)
	turn.mu.Lock()
	result := readoutBackendResult{response: turn.Response}
	turn.mu.Unlock()
	result.output, result.audioMS, result.firstToken = c.allOutput(), c.audioMS(), turn.Timings().FirstToken
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, event := range c.events {
		envelope, ok := event.(live.ResponseEventEnvelope)
		if !ok {
			continue
		}
		var row struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Item  struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"item"`
			Response struct{ Status string } `json:"response"`
		}
		if err := json.Unmarshal(envelope.Event, &row); err != nil {
			t.Fatal(err)
		}
		if row.Type == "response.output_text.delta" {
			result.deltas += row.Delta
		}
		if row.Type == "response.output_item.done" {
			for _, content := range row.Item.Content {
				result.completedText += content.Text
			}
		}
		if row.Response.Status != "" {
			result.status = row.Response.Status
		}
	}
	return result
}

func TestBackendSuppressesOneExactReadoutRepeat(t *testing.T) {
	const readout = "保额100元，保费20元。"
	const followup = "还需要调整吗？"
	// Exercise both a whole delta and rune-sized deltas: provider fragment
	// boundaries must not change the result.
	for _, split := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "rune_fragments"}[split], func(t *testing.T) {
			text := readout + followup
			deltas := []provider.LLMDelta{{Text: text}}
			if split {
				deltas = nil
				for _, r := range text {
					deltas = append(deltas, provider.LLMDelta{Text: string(r)})
				}
			}
			got := runReadoutBackend(t, "", readout, deltas, "")
			if got.output != text || got.audioMS == 0 {
				t.Errorf("audio output transcript=%q audio_ms=%v, want %q with audio", got.output, got.audioMS, text)
			}
			if got.response != text || got.completedText != text || got.deltas != followup {
				t.Errorf("response=%q completed=%q deltas=%q, want one readout and unchanged continuation", got.response, got.completedText, got.deltas)
			}
		})
	}
}

func TestBackendFixedPrefixBookkeepingIsNotRepeated(t *testing.T) {
	for _, prefix := range []struct{ opening, readout string }{{"算好了。", ""}, {"", "保费20元。"}, {"算好了。", "保费20元。"}, {"", ""}} {
		got := runReadoutBackend(t, prefix.opening, prefix.readout, []provider.LLMDelta{{Text: "还需要调整吗？"}}, "")
		want := prefix.opening + prefix.readout + "还需要调整吗？"
		if got.response != want || got.completedText != want || got.output != want {
			t.Errorf("response=%q completed=%q output=%q, want %q", got.response, got.completedText, got.output, want)
		}
	}
}

func TestBackendReadoutRepeatFlushesPartialOnEOFOrError(t *testing.T) {
	const readout = "保额100元，保费20元。"
	const partial = "保额100元"
	for _, failed := range []bool{false, true} {
		deltas := []provider.LLMDelta{{Text: partial}}
		status := "completed"
		if failed {
			deltas = append(deltas, provider.LLMDelta{Err: errors.New("fixture stream error")})
			status = "failed"
		}
		got := runReadoutBackend(t, "", readout, deltas, "")
		if got.deltas != partial || got.output != readout+partial || got.response != readout+partial || got.status != status {
			t.Errorf("partial/error=%v result=%+v", failed, got)
		}
	}
}

func TestBackendReadoutRepeatDoesNotFlushAbandonedPartial(t *testing.T) {
	const readout = "保额100元，保费20元。"
	for _, reason := range []string{"cancel", "stale"} {
		t.Run(reason, func(t *testing.T) {
			got := runReadoutBackend(t, "", readout, []provider.LLMDelta{{Text: "保额100"}}, reason)
			if got.deltas != "" || got.completedText != "" || got.response != readout {
				t.Errorf("abandoned partial was flushed: %+v", got)
			}
			if got.firstToken.IsZero() {
				t.Fatal("raw first-token timing was lost while filtering")
			}
		})
	}
}

func TestBackendReadoutRepeatPreservesRevisedAmountAndLaterRepeat(t *testing.T) {
	const readout = "保费20元。"
	for _, text := range []string{"保费25元。还需要调整吗？", "好的。" + readout, readout + readout} {
		want := text
		if strings.HasPrefix(text, readout) {
			want = strings.TrimPrefix(text, readout) // exactly one leading copy
		}
		got := runReadoutBackend(t, "", readout, []provider.LLMDelta{{Text: text}}, "")
		if got.deltas != want || got.response != readout+want || got.output != readout+want {
			t.Errorf("model=%q result=%+v", text, got)
		}
	}
}

func TestReadoutRepeatFilterEveryByteSplit(t *testing.T) {
	const readout = "保额100元，保费20元。"
	for _, tc := range []struct {
		name, prefix, input, want string
	}{
		{"exact", readout, readout, ""},
		{"continuation", readout, readout + "还需要调整吗？", "还需要调整吗？"},
		{"one_shot", readout, readout + readout, readout},
		{"revised_amount", readout, "保额100元，保费25元。", "保额100元，保费25元。"},
		{"other_punctuation", readout, "保额100元，保费20元！", "保额100元，保费20元！"},
		{"later_readout", readout, "好的。" + readout, "好的。" + readout},
		{"leading_space", readout, " " + readout, " " + readout},
		{"partial_eof", readout, "保额100元", "保额100元"},
		{"no_readout", "", readout, readout},
		{"unfinished_amount", "保费20", "保费200元。", "保费200元。"},
		{"decimal_continuation", "Total 20.", "Total 20.50 dollars.", "Total 20.50 dollars."},
		{"latin_sentence", "Total 20 dollars.", "Total 20 dollars.Follow-up.", "Follow-up."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// All two-cut partitions include empty fragments and cuts inside
			// multi-byte characters. Matching is exact bytes, not normalized text.
			for a := 0; a <= len(tc.input); a++ {
				for b := a; b <= len(tc.input); b++ {
					filter := newReadoutRepeatFilter(tc.prefix)
					got := filter.Push(tc.input[:a]) + filter.Push(tc.input[a:b]) + filter.Push(tc.input[b:]) + filter.Flush()
					if got != tc.want {
						t.Fatalf("cuts=%d,%d got=%q want=%q", a, b, got, tc.want)
					}
					if filter.Flush() != "" {
						t.Fatal("second flush replayed buffered content")
					}
				}
			}
		})
	}
}

func TestReadoutRepeatFilterBuffersOnlyUntilExactMatchOrMismatch(t *testing.T) {
	filter := newReadoutRepeatFilter("保费20元。")
	if filter.Push("保费") != "" || filter.Push("") != "" || filter.Push("20") != "" {
		t.Fatal("unfinished exact prefix escaped before the decision")
	}
	if got := filter.Push("元。还有问题吗？"); got != "还有问题吗？" {
		t.Fatalf("exact match lost its continuation: %q", got)
	}
	if got := filter.Push("保费20元。"); got != "保费20元。" {
		t.Fatalf("filter removed more than the first copy: %q", got)
	}

	filter = newReadoutRepeatFilter("保费20元。")
	if filter.Push("保费") != "" {
		t.Fatal("prefix was not buffered")
	}
	if got := filter.Push("25元。"); got != "保费25元。" {
		t.Fatalf("mismatch changed a revised amount: %q", got)
	}
	if got := filter.Push("保费20元。"); got != "保费20元。" {
		t.Fatalf("filter restarted after a mismatch: %q", got)
	}
}

func TestReadoutRepeatFilterRequiresSentenceBoundary(t *testing.T) {
	for _, text := range []string{"", "保费20", "保费20元", "保费20元，", "保费20元；", "Total 20.", "Total ２０."} {
		filter := newReadoutRepeatFilter(text)
		if got := filter.Push(text); got != text {
			t.Errorf("nonterminal or decimal prefix was suppressed: %q", text)
		}
	}
	for _, text := range []string{"保费20元。", "保费20元！", "保费20元？", "Total 20 dollars.", "Done!", "Done?", "保费20元。 \n"} {
		filter := newReadoutRepeatFilter(text)
		if got := filter.Push(text); got != "" {
			t.Errorf("complete exact sentence was not suppressed: %q", text)
		}
	}
}
