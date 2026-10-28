package duplex

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// logBook keeps what an engine logged, for tests of the lines an operator
// reads a call by.
type logBook struct {
	mu    sync.Mutex
	lines []logLine
}

type logLine struct {
	msg   string
	attrs map[string]int64
}

func (b *logBook) logger() *slog.Logger { return slog.New(logBookHandler{b}) }

// named returns the lines with this message, with their integer fields.
func (b *logBook) named(msg string) []logLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []logLine
	for _, l := range b.lines {
		if l.msg == msg {
			out = append(out, l)
		}
	}
	return out
}

type logBookHandler struct{ book *logBook }

func (h logBookHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h logBookHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h logBookHandler) WithGroup(string) slog.Handler            { return h }
func (h logBookHandler) Handle(_ context.Context, r slog.Record) error {
	line := logLine{msg: r.Message, attrs: map[string]int64{}}
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindInt64 {
			line.attrs[a.Key] = a.Value.Int64()
		}
		return true
	})
	h.book.mu.Lock()
	h.book.lines = append(h.book.lines, line)
	h.book.mu.Unlock()
	return nil
}

// Choosing duplex.early_final_ms takes the lengths of the pauses callers carry
// on from. The pause that ends the turn is not one of them, and it is the one
// that is easy to count by mistake: when the utterance closes, its trailing
// silence reads zero again, exactly as it does when the caller resumes.
func TestAPauseTheCallerCarriesOnFromIsCountedAndThePauseThatEndsTheTurnIsNot(t *testing.T) {
	cfg := parityConfig()
	rate := cfg.ClientRate
	c := newCollector(rate)
	book := &logBook{}
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true},
		Deps{ASR: voicedASR("你好帮我查一下明天的天气"), LLM: mock.NewLLM(), Emit: c.emit, Log: book.logger()})

	speak(e, rate, 800)
	pause(e, rate, 100)
	speak(e, rate, 800)
	deadline := time.Now().Add(3 * time.Second)
	for c.count(live.ExtSpeechStopped) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the turn never closed")
		}
		pause(e, rate, 20)
	}
	pause(e, rate, 200)
	if n := c.count(live.ExtSpeechStopped); n != 1 {
		t.Fatalf("one sentence with a 100 ms pause in it was heard as %d utterances; this test is about a pause inside one", n)
	}
	e.Close()

	seen := book.named("listen: the caller paused inside the utterance")
	if len(seen) != 1 {
		t.Fatalf("%d pauses inside the utterance were noted; the caller paused once and then stopped", len(seen))
	}
	if ms := seen[0].attrs["pause_ms"]; ms < 80 || ms > 120 {
		t.Errorf("the pause was noted as %d ms; it was 100", ms)
	}

	summary := book.named("listen: pauses inside utterances")
	if len(summary) != 1 {
		t.Fatalf("%d summaries of the pauses at close; want one", len(summary))
	}
	got := summary[0].attrs
	if got["n"] != 1 {
		t.Errorf("the summary counts %d pauses; there was one", got["n"])
	}
	bucket := "ms_120_plus"
	switch ms := seen[0].attrs["pause_ms"]; {
	case ms < 100:
		bucket = "ms_80_99"
	case ms < 120:
		bucket = "ms_100_119"
	}
	if got[bucket] != 1 || got["ms_60_79"]+got["ms_80_99"]+got["ms_100_119"]+got["ms_120_plus"] != 1 {
		t.Errorf("one pause of %d ms was sorted as %v; want it under %s and nowhere else", seen[0].attrs["pause_ms"], got, bucket)
	}
}

// A call nobody paused in, on a line that returned nothing, has nothing to
// report, and says nothing rather than a line of zeros.
func TestNothingIsReportedAboutTheLineWhenNothingWasMeasured(t *testing.T) {
	cfg := parityConfig()
	rate := cfg.ClientRate
	c := newCollector(rate)
	book := &logBook{}
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true},
		Deps{ASR: voicedASR("你好"), LLM: mock.NewLLM(), Emit: c.emit, Log: book.logger()})

	speak(e, rate, 600)
	pause(e, rate, 300)
	e.Close()

	for _, msg := range []string{"listen: pauses inside utterances", "listen: echo return time"} {
		if n := len(book.named(msg)); n != 0 {
			t.Errorf("%q was logged %d time(s) with nothing measured", msg, n)
		}
	}
}

// The detector times the assistant's voice coming back; the engine is what
// starts that clock, by telling the detector when the assistant speaks, and
// what reports it. Here the caller's line returns a burst of sound shortly
// after the answer begins.
func TestTheAssistantsVoiceComingBackIsTimedAndReportedAtClose(t *testing.T) {
	cfg := parityConfig()
	rate := cfg.ClientRate
	c := newCollector(rate)
	book := &logBook{}
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true},
		Deps{ASR: voicedASR("今天天气怎么样"), LLM: mock.NewLLM(), Emit: c.emit, Log: book.logger()})

	speak(e, rate, 1000)
	deadline := time.Now().Add(5 * time.Second)
	for c.audioMS() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the assistant never answered")
		}
		pause(e, rate, 20)
	}
	pause(e, rate, 100)
	// The echo: far quieter than the caller, and too short to be taken for
	// somebody interrupting.
	pushPaced(e, voiced(rate, 100, 0.05), rate)
	pause(e, rate, 200)
	e.Close()

	seen := book.named("listen: the assistant's voice came back")
	if len(seen) != 1 {
		t.Fatalf("the assistant's voice came back once and was noted %d time(s)", len(seen))
	}
	// The answer began some time before its first audio was seen here, and
	// the echo 100 ms after that.
	if ms := seen[0].attrs["ms"]; ms < 100 || ms > 600 {
		t.Errorf("the voice was timed coming back after %d ms; it was sent back about 100 ms after the answer was first heard", ms)
	}

	summary := book.named("listen: echo return time")
	if len(summary) != 1 {
		t.Fatalf("%d summaries of the echo return at close; want one", len(summary))
	}
	got := summary[0].attrs
	if got["n"] != 1 || got["p50_ms"] != seen[0].attrs["ms"] || got["min_ms"] != got["p50_ms"] || got["max_ms"] != got["p50_ms"] {
		t.Errorf("one measurement of %d ms was summarised as %v", seen[0].attrs["ms"], got)
	}
}
