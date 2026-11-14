package duplex

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/journal"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

func transcriptEngine(t *testing.T) (*Engine, *bytes.Buffer, *collector) {
	t.Helper()
	var log bytes.Buffer
	cfg := testConfig()
	cfg.Duplex.PlaybackPaced = false
	cfg.Duplex.Opener = true
	cfg.Duplex.PhraseCache = true
	c := newCollector(cfg.ClientRate)
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Debug("session started", "session", "transcript-test", "call", "out-transcript-test", "realtime", true)
	e := New(Options{Cfg: cfg, SessionID: "transcript-test"}, Deps{TTS: mock.NewTTS(), Emit: c.emit, Log: logger})
	e.ctx = context.Background()
	e.startOutputTranscript()
	t.Cleanup(e.Close)
	return e, &log, c
}
func transcriptPCM(e *Engine) PhraseAudio {
	pcm := make([]byte, e.opts.ClientRate/25*2)
	for i := 0; i < len(pcm); i += 2 {
		pcm[i+1] = 16
	}
	return PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate}
}
func drainTranscriptPlayer(e *Engine) {
	queue := e.player.queue
	e.player.queue = nil
	for _, seg := range queue {
		switch seg.Kind {
		case SegBegin:
			e.player.beginSegment(&e.gen, seg)
		case SegAudio:
			e.player.playAudio(&e.gen, seg)
		case SegMark:
			e.player.markSegment(&e.gen, seg)
		case SegEnd:
			e.player.finishTurn(&e.gen, seg)
		}
	}
}
func journalTranscript(t *testing.T, log *bytes.Buffer) string {
	t.Helper()
	ix := journal.NewIndexer()
	if err := ix.ReadLog(bytes.NewReader(log.Bytes())); err != nil {
		t.Fatal(err)
	}
	calls := ix.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls=%d", len(calls))
	}
	var parts []string
	for _, line := range calls[0].Transcript {
		if line.Who == "assistant" {
			parts = append(parts, line.Text)
		}
	}
	return strings.Join(parts, "")
}
func wireTranscript(c *collector) string {
	var out strings.Builder
	for _, event := range c.events {
		if ev, ok := event.(live.TranscriptDelta); ok && ev.Type == live.ServerOutputTranscriptDelta {
			out.WriteString(ev.Delta)
		}
	}
	return out.String()
}

func TestOutputTranscriptLogMixedOpenerPreparedAndSynthesized(t *testing.T) {
	e, log, c := transcriptEngine(t)
	const opener = "Fixture opener."
	e.opts.Cfg.Duplex.OpenerPhrases = []string{opener}
	Phrases().Put(e.phraseKey(opener), transcriptPCM(e))
	turn := e.tracker.Begin("fixture", false)
	e.commitTurn(turn)
	turn.OpenerPlanned = opener
	e.sayOpener(turn)
	pipe := &speechPipe{e: e, turn: turn, ctx: context.Background(), readout: true}
	const prefix = "Prepared fixture total."
	const tail = "Remaining fixture details."
	const cached = "Cached fixture clause."
	if !pipe.playPrepared(prefix, transcriptPCM(e).PCM, true) || !pipe.speakCached(cached, transcriptPCM(e)) || !pipe.speak(tail) {
		t.Fatal("fixture did not queue speech")
	}
	if got := journalTranscript(t, log); got != "" {
		t.Fatalf("queued speech became transcript: %q", got)
	}
	drainTranscriptPlayer(e)
	want := opener + prefix + cached + tail
	if got := wireTranscript(c); got != want {
		t.Fatalf("wire=%q", got)
	}
	if got := journalTranscript(t, log); got != want {
		t.Fatalf("journal=%q", got)
	}
	for _, raw := range strings.Split(log.String(), "\n") {
		l, ok := journal.ParseLine(raw)
		if ok && l.Msg == "speak: prepared quote segment played" && l.Get("text") != "" {
			t.Fatal("prepared diagnostic leaked spoken text")
		}
	}
}

func TestOutputTranscriptLogIncludesCachedAndLiveFillers(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "cached"}[cached], func(t *testing.T) {
			e, log, c := transcriptEngine(t)
			e.opts.Cfg.Duplex.PhraseCache = cached
			phrase := "Unique filler fixture."
			if cached {
				Phrases().Put(e.phraseKey(phrase), transcriptPCM(e))
			}
			turn := &Turn{ID: "bc_1", Generation: e.gen.Current(), State: TurnCommitted}
			e.speakBackchannel(turn, phrase)
			if got := journalTranscript(t, log); got != "" {
				t.Fatalf("queued filler became transcript: %q", got)
			}
			drainTranscriptPlayer(e)
			if wireTranscript(c) != phrase || journalTranscript(t, log) != phrase {
				t.Fatal("filler output missing or duplicated")
			}
		})
	}
}

func TestOutputTranscriptLogCanceledPreparedSegmentIsAbsent(t *testing.T) {
	e, log, c := transcriptEngine(t)
	turn := e.tracker.Begin("fixture", false)
	e.commitTurn(turn)
	pipe := &speechPipe{e: e, turn: turn, ctx: context.Background(), readout: true}
	if !pipe.playPrepared("Unheard fixture.", transcriptPCM(e).PCM, true) {
		t.Fatal("not queued")
	}
	e.tracker.Begin("replacement", false)
	drainTranscriptPlayer(e)
	if wireTranscript(c) != "" || journalTranscript(t, log) != "" {
		t.Fatal("canceled segment was transcribed")
	}
}
