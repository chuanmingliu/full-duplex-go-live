package server

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/simulator"
)

// Count what reaches the recognizer, rather than treating an idle timer or an
// outbound packet as proof that the engine actually received the quiet pause.
type simulatorQuietASR struct {
	provider.ASR
	quietBytes atomic.Int64
}

func (a *simulatorQuietASR) Open(ctx context.Context, opts provider.ASROptions) (provider.ASRStream, error) {
	s, err := a.ASR.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &simulatorQuietStream{ASRStream: s, asr: a}, nil
}

type simulatorQuietStream struct {
	provider.ASRStream
	asr *simulatorQuietASR
}

func (s *simulatorQuietStream) Write(pcm []byte) error {
	if len(pcm) > 0 && bytes.Count(pcm, []byte{0}) == len(pcm) {
		s.asr.quietBytes.Add(int64(len(pcm)))
	}
	return s.ASRStream.Write(pcm)
}

func TestSimulatorQuietLineClosesTheRemoteTurnWithoutMorePackets(t *testing.T) {
	ts, receiver, controller, snap := simulatorHarness(t)
	var asr simulatorQuietASR
	receiver.Factory = func(o duplex.Options, d duplex.Deps) *duplex.Engine {
		// Keep the regression tied to audio-driven VAD, not semantic or
		// speculative shortcuts that can finalize a partial on a timer.
		o.Cfg.VAD.MinSilenceMS = 180
		o.Cfg.Duplex.SemanticEndpointing = false
		o.Cfg.Duplex.EarlyFinalMS = 0
		recognizer := mock.NewASR("Can you confirm Tuesday?")
		recognizer.MSPerRune = 10
		recognizer.VoicedOnly = true
		asr.ASR = recognizer
		d.ASR = &asr
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
	client.first("response.audio.done", 3*time.Second)
	greetingMS := client.audioMS(16000)
	client.say(voicedPCM(16000, 400), 16000)
	// The gateway now suppresses every packet. The receiver must deliver the
	// pause to VAD and ASR itself, without another packet or client commit.
	await(t, "silence reaching the recognizer after RTP stops", time.Second, func() bool {
		return asr.quietBytes.Load() >= 16000*2*100/1000
	})
	client.first("input_audio_buffer.speech_stopped", time.Second)
	client.first("conversation.item.input_audio_transcription.completed", time.Second)
	await(t, "customer response to the completed remote turn", 3*time.Second, func() bool {
		return client.count("response.created") >= 2 && client.audioMS(16000) > greetingMS+100
	})
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	page, err := controller.Events(context.Background(), snap.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	remoteStarts := 0
	for _, event := range page.Events {
		if event.Kind == "remote_speech_started" {
			remoteStarts++
		}
	}
	if remoteStarts != 1 {
		t.Fatalf("synthetic silence changed received speech evidence: %d remote turns", remoteStarts)
	}
}

func TestSimulatorLineFillsOnlyCurrentQuietPeriods(t *testing.T) {
	base := time.Now()
	var frames [][]byte
	line := simulatorInputLine{ctx: context.Background(), lastInput: base, push: func(pcm []byte) {
		frames = append(frames, append([]byte(nil), pcm...))
	}}
	// These are clock ticks, not received packets. Even after a long stalled
	// scheduler, only the current 20 ms period is emitted, without catch-up.
	for _, step := range []struct {
		at   time.Duration
		want int
	}{
		{99 * time.Millisecond, 0},
		{100 * time.Millisecond, 1},
		{119 * time.Millisecond, 1},
		{120 * time.Millisecond, 2},
		{15 * time.Second, 3},
		{15 * time.Second, 3},
		{15*time.Second + 20*time.Millisecond, 4},
	} {
		line.fillLocked(base.Add(step.at))
		if len(frames) != step.want {
			t.Fatalf("at %s: got %d frames, want %d", step.at, len(frames), step.want)
		}
	}
	for _, frame := range frames {
		if len(frame) != 640 || !bytes.Equal(frame, make([]byte, 640)) {
			t.Fatal("fill is not a 20 ms frame of 16 kHz PCM16 silence")
		}
	}
}

func TestSimulatorLineResumedAndContinuousAudioSuppressFill(t *testing.T) {
	var quiet, real int
	line := simulatorInputLine{ctx: context.Background(), push: func(pcm []byte) {
		if len(pcm) == 640 {
			quiet++
		} else {
			real++
		}
	}}
	line.fillLocked(time.Now())
	if quiet != 1 {
		t.Fatal("initial quiet did not fill")
	}
	for range 20 {
		if !line.receive([]byte{1, 0}, nil) {
			t.Fatal("real audio rejected")
		}
		line.fillLocked(line.lastInput.Add(20 * time.Millisecond))
	}
	if quiet != 1 || real != 20 {
		t.Fatalf("fill ran while real packets were arriving: quiet=%d real=%d", quiet, real)
	}
	lastInput := line.lastInput
	line.receive(nil, nil)
	line.receive([]byte{}, nil)
	if line.lastInput != lastInput {
		t.Fatal("empty append postponed silence fill")
	}
	line.fillLocked(lastInput.Add(100 * time.Millisecond))
	if quiet != 2 {
		t.Fatal("fill did not resume after the new quiet gap")
	}
}

func TestSimulatorLineStartsOnlyAtAnswerAndStopsBeforeClose(t *testing.T) {
	var line simulatorInputLine
	var count atomic.Int64
	push := func([]byte) { count.Add(1) }
	if line.receive([]byte{1, 0}, nil) {
		t.Fatal("audio admitted before the answer gate")
	}
	if line.open(context.Background(), func() bool { return false }, push) {
		t.Fatal("rejected answer gate opened the line")
	}
	if !line.open(context.Background(), func() bool { return true }, push) {
		t.Fatal("answered line did not open")
	}
	if line.open(context.Background(), func() bool { t.Fatal("duplicate activation"); return true }, push) {
		t.Fatal("duplicate line opened")
	}
	// No first-packet dependency: a quiet answered caller still advances the
	// engine. This also exercises the actual wall-clock loop, not only ticks.
	await(t, "quiet answered line fills without a first packet", time.Second, func() bool { return count.Load() >= 3 })
	line.stop()
	before := count.Load()
	if line.receive([]byte{1, 0}, nil) {
		t.Fatal("late packet admitted after stop")
	}
	time.Sleep(2 * simulatorLineGap)
	if count.Load() != before {
		t.Fatal("a frame reached the closed engine")
	}
	line.stop()
	if line.open(context.Background(), func() bool { return true }, push) {
		t.Fatal("stale session reopened")
	}
	var ended simulatorInputLine
	ended.stop()
	if ended.open(context.Background(), func() bool { return true }, push) {
		t.Fatal("hangup-before-answer started the filler")
	}
}

func TestSimulatorLineCancellationStopsBothInputSources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var line simulatorInputLine
	var count atomic.Int64
	if !line.open(ctx, func() bool { return true }, func([]byte) { count.Add(1) }) {
		t.Fatal("line did not open")
	}
	cancel()
	select {
	case <-line.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled filler remained running")
	}
	if line.receive([]byte{1, 0}, nil) || count.Load() != 0 {
		t.Fatal("cancelled line accepted a frame")
	}
	line.stop()
}

func TestSimulatorLineStopWaitsForSerializedIngress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var line simulatorInputLine
	if !line.open(ctx, func() bool { return true }, func([]byte) {
		once.Do(func() { close(entered) })
		<-release
	}) {
		t.Fatal("line did not open")
	}
	received := make(chan struct{})
	go func() { line.receive([]byte{1, 0}, nil); close(received) }()
	<-entered
	stopped := make(chan struct{})
	go func() { line.stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stop returned while ingress was still pushing")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not join ingress")
	}
	<-received
	if line.receive([]byte{1, 0}, nil) {
		t.Fatal("real input reached the engine after stop returned")
	}
}

func TestSimulatorLineObservesRealInputBeforeEngineProcessing(t *testing.T) {
	observed := false
	var line simulatorInputLine
	line.ctx = context.Background()
	line.push = func([]byte) {
		if !observed {
			t.Fatal("engine processing overtook the receive observation")
		}
	}
	if !line.receive([]byte{1, 0}, func() { observed = true }) {
		t.Fatal("real input rejected")
	}
	line.stop()
	line.receive([]byte{1, 0}, func() { t.Fatal("closed input was observed") })
}
