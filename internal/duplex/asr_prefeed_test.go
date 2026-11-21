package duplex

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

func prefeedTestEngine(t *testing.T) (*Engine, *prefeedSimASR) {
	t.Helper()
	cfg := testConfig()
	cfg.Duplex.ASRPredial = true
	cfg.VAD.BargeInMinSpeechMS = 320
	a := &prefeedSimASR{text: "Please wait", streams: make(chan *prefeedSimStream, 10)}
	e := New(Options{Cfg: cfg, ClientRate: provider.PipelineRate, Delegation: live.DelegationClient}, Deps{ASR: a, Emit: func(any) {}})
	ctx, cancel := context.WithCancel(context.Background())
	e.ctx = ctx
	e.speaking.Store(true)
	e.vad.SetAssistantSpeaking(true)
	t.Cleanup(func() { cancel(); e.closeASR(true); e.closeStandby(); e.Close() })
	return e, a
}

func TestPrefeedAdoptionDoesNotRepeatOrLoseAudio(t *testing.T) {
	e, a := prefeedTestEngine(t)
	pcm := voiced(provider.PipelineRate, 800, .45)
	// A shadow detector supplies the unoptimized canonical ASR byte stream.
	shadowOwner := New(e.opts, Deps{Emit: func(any) {}})
	t.Cleanup(shadowOwner.Close)
	shadow := shadowOwner.vad
	shadow.SetAssistantSpeaking(true)
	var expected []byte
	for off := 0; off < len(pcm); off += 640 {
		frame := pcm[off : off+640]
		x, _ := audio.DecodePCM16(frame)
		d := shadow.Push(x)
		expected = append(expected, audio.EncodePCM16(d.Snapshot)...)
		e.onAudio(frame)
		if off == 0 {
			if e.asrRun != nil || e.userOpen || len(e.asrCh) != 0 {
				t.Fatal("unconfirmed candidate escaped into a caller turn")
			}
		}
	}
	var stream *prefeedSimStream
	select {
	case stream = <-a.streams:
	case <-time.After(time.Second):
		t.Fatal("recognizer never connected")
	}
	waitFor(t, "all ASR audio", time.Second, func() bool { stream.mu.Lock(); defer stream.mu.Unlock(); return len(stream.pcm) >= len(expected) })
	stream.mu.Lock()
	got := append([]byte(nil), stream.pcm...)
	stream.mu.Unlock()
	if !bytes.Equal(got, expected) {
		t.Fatalf("ASR audio differs: got %d expected %d bytes", len(got), len(expected))
	}
	if len(a.streams) != 0 {
		t.Fatal("adoption opened another recognizer")
	}
}

func TestPrefeedRejectedCandidateIsClosedAndNeverAdopted(t *testing.T) {
	e, a := prefeedTestEngine(t)
	e.onAudio(voiced(provider.PipelineRate, 80, .45))
	var first *prefeedSimStream
	select {
	case first = <-a.streams:
	case <-time.After(time.Second):
		t.Fatal("candidate not dialled")
	}
	e.onAudio(make([]byte, provider.PipelineRate*2/2))
	if e.asrStandby != nil || e.asrStandbySamples != 0 || e.asrRun != nil {
		t.Fatal("false candidate retained")
	}
	waitFor(t, "discarded recognizer close", time.Second, func() bool { first.mu.Lock(); defer first.mu.Unlock(); return first.closed })
	if len(e.asrCh) != 0 {
		t.Fatal("false candidate produced an engine ASR event")
	}
	e.onAudio(voiced(provider.PipelineRate, 500, .45))
	select {
	case second := <-a.streams:
		if second == first {
			t.Fatal("reused rejected recognizer")
		}
	case <-time.After(time.Second):
		t.Fatal("confirmed utterance did not get fresh recognition")
	}
}

func TestPrefeedLeavesOrdinaryPredialSilent(t *testing.T) {
	e, a := prefeedTestEngine(t)
	e.speaking.Store(false)
	e.vad.SetAssistantSpeaking(false)
	e.onAudio(voiced(provider.PipelineRate, 80, .45))
	select {
	case stream := <-a.streams:
		stream.mu.Lock()
		defer stream.mu.Unlock()
		if len(stream.pcm) != 0 || e.asrStandbySamples != 0 {
			t.Fatal("ordinary non-playback predial sent candidate audio")
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary handshake predial was lost")
	}
}

func TestClosingUnadoptedASRUnblocksBufferedResults(t *testing.T) {
	l := &lazyASR{results: make(chan provider.ASRResult, 1), done: make(chan struct{})}
	l.results <- provider.ASRResult{Text: "buffered"}
	done := make(chan bool, 1)
	go func() { done <- l.forward(context.Background(), provider.ASRResult{Text: "extra"}) }()
	_ = l.Close()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("closed stream forwarded another result")
		}
	case <-time.After(time.Second):
		t.Fatal("discarded stream leaked blocked forwarder")
	}
}

func TestPrefeedKeepsLatestCorrectionAndResultBarriers(t *testing.T) {
	l := &lazyASR{results: make(chan provider.ASRResult, 32)}
	err := errors.New("test provider failure")
	for _, r := range []provider.ASRResult{
		{Text: "please stop"}, {Text: "嗯"}, {Text: "嗯", Final: true},
		{Err: err}, {Text: "next partial"}, {Text: "next correction"},
	} {
		l.results <- r
	}
	got := l.bufferedResults()
	if len(got) != 3 || got[0].Text != "嗯" || !got[0].Final || got[1].Err != err || got[2].Text != "next correction" {
		t.Fatalf("superseded partial or lost barrier: %+v", got)
	}
}
