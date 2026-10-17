package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/simulator"
)

type simulatorFailOneTTS struct {
	provider.TTS
	failed atomic.Bool
}

func (p *simulatorFailOneTTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	stream, err := p.TTS.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &simulatorFailOneStream{TTSStream: stream, owner: p}, nil
}

type simulatorFailOneStream struct {
	provider.TTSStream
	owner *simulatorFailOneTTS
}

func (s *simulatorFailOneStream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	if s.owner.failed.CompareAndSwap(false, true) {
		chunks := make(chan provider.TTSChunk, 2)
		chunks <- provider.TTSChunk{PCM: voicedPCM(s.SampleRate(), 40)}
		chunks <- provider.TTSChunk{Err: errors.New("vendor SECRET_TOKEN private utterance")}
		close(chunks)
		return chunks, nil
	}
	return s.TTSStream.Synthesize(ctx, text)
}

// Losing the warning bridge would leave this call alive but silently omit the
// failed segment from its report. A provider fake replaces only the external
// synthesis stream; the receiver, engine, controller and WebSocket are real.
func TestSimulatorTTSWarningSurvivesTheMediaAndReportBoundary(t *testing.T) {
	ts, receiver, controller, snap := simulatorHarness(t)
	warnings := make(chan duplex.SimulatorWarningEvent, 4)
	receiver.Factory = func(o duplex.Options, d duplex.Deps) *duplex.Engine {
		o.Cfg.Duplex.PhraseCache = false
		d.TTS = &simulatorFailOneTTS{TTS: d.TTS}
		emit := d.Emit
		d.Emit = func(event any) {
			emit(event)
			if w, ok := event.(duplex.SimulatorWarningEvent); ok {
				warnings <- w
			}
		}
		return duplex.New(o, d)
	}
	client := dialRealtime(t, ts, simulatorPath(snap))
	defer client.conn.Close()
	client.send(map[string]any{"type": "session.update", "session": map[string]any{"input_audio_format": "pcm16"}})
	client.first("session.updated", time.Second)
	bound, _ := controller.Get(context.Background(), snap.RunID)
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "answered", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-warnings:
	case <-time.After(2 * time.Second):
		t.Fatal("failed synthesis did not produce a warning")
	}
	client.say(voicedPCM(16000, 400), 16000)
	await(t, "response after failed greeting segment", 4*time.Second, func() bool { return client.audioMS(16000) > 100 })
	select {
	case <-client.done:
		t.Fatal("recoverable TTS failure closed media")
	default:
	}
	if client.count("error") != 0 || client.count("simulator.warning") != 0 {
		t.Fatal("private recoverable failure escaped onto the media protocol")
	}
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	report, err := controller.Report(context.Background(), snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Phase != simulator.Completed || len(report.Failures) != 1 || !strings.HasPrefix(report.Failures[0], "warning: tts_chunk_failed") {
		t.Fatalf("recoverable failure missing or made call fatal: phase=%s failures=%v", report.Phase, report.Failures)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "SECRET_TOKEN") || strings.Contains(string(raw), "private utterance") {
		t.Fatal("raw provider error leaked into report")
	}
}

type simulatorLongGreetingTTS struct{ provider.TTS }

func (p simulatorLongGreetingTTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	stream, err := p.TTS.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	return simulatorLongGreetingStream{stream}, nil
}

type simulatorLongGreetingStream struct{ provider.TTSStream }

func (s simulatorLongGreetingStream) Synthesize(context.Context, string) (<-chan provider.TTSChunk, error) {
	chunks := make(chan provider.TTSChunk, 1)
	chunks <- provider.TTSChunk{PCM: voicedPCM(s.SampleRate(), 2000)}
	close(chunks)
	return chunks, nil
}

func TestSimulatorStopSilencesMediaUntilGatewayConfirmsHangup(t *testing.T) {
	ts, receiver, controller, snap := simulatorHarness(t)
	receiver.Factory = func(o duplex.Options, d duplex.Deps) *duplex.Engine {
		o.Cfg.Duplex.PlaybackPaced = true
		o.Cfg.Duplex.PhraseCache = false
		d.TTS = simulatorLongGreetingTTS{d.TTS}
		return duplex.New(o, d)
	}
	client := dialRealtime(t, ts, simulatorPath(snap))
	defer client.conn.Close()
	client.send(map[string]any{"type": "session.update", "session": map[string]any{"input_audio_format": "pcm16"}})
	client.first("session.updated", time.Second)
	bound, _ := controller.Get(context.Background(), snap.RunID)
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "answered", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	client.first("response.audio.delta", time.Second)
	if err := controller.Stop(context.Background(), snap.RunID); err != nil {
		t.Fatal(err)
	}
	// Drain an already-written packet at the client, then observe beyond several
	// playout periods. The gateway has not sent its ended callback yet.
	time.Sleep(50 * time.Millisecond)
	before := client.audioMS(16000)
	select {
	case <-client.done:
		t.Fatal("stop closed media before gateway ended confirmation")
	case <-time.After(150 * time.Millisecond):
	}
	if client.audioMS(16000) != before {
		t.Fatal("receiver kept speaking after stop while awaiting hangup")
	}
	if _, err := controller.Report(context.Background(), snap.RunID); err == nil {
		t.Fatal("report published before gateway ended confirmation")
	}
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("confirmed hangup did not close media")
	}
}
