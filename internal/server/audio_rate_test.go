package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/simulator"
)

// Keep the flood diagnostic on the actual wire handler: accidentally metering
// base64/JSON instead of decoded PCM must not silently change the audio budget.
func TestAudioFloodDiagnosticIdentifiesTheReceivedWireBudget(t *testing.T) {
	cfg := config.Default()
	cfg.WebRoot, cfg.Greeting = "", ""
	cfg.Duplex.MaxAudioKbps = 512
	logs := audioFloodLog{records: make(chan map[string]any, 1)}
	srv := NewServer(cfg, slog.New(slog.NewJSONHandler(logs, nil)))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := dialRealtime(t, ts, "/v1/realtime")
	defer client.conn.Close()
	client.send(map[string]any{"type": "session.update", "session": map[string]any{"input_audio_format": "pcm16"}})
	client.first("session.updated", time.Second)
	// 64,000 bytes is the entire 512 kbps one-second budget, independent of
	// the 85,336-character base64 representation. The following 640-byte
	// (20 ms at 16 kHz) packet must be the one that trips the guard.
	for _, n := range []int{64000, 640} {
		client.send(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(make([]byte, n))})
	}
	select {
	case record := <-logs.records:
		for key, want := range map[string]float64{
			"wire_audio_bytes": 64640, "wire_audio_frames": 2,
			"frame_bytes": 640, "max_audio_kbps": 512,
		} {
			if got := record[key]; got != want {
				t.Errorf("%s = %v, want %v", key, got, want)
			}
		}
		if ms, ok := record["window_ms"].(float64); !ok || ms < 0 || ms >= 1000 {
			t.Errorf("window_ms = %v, want the elapsed current window", record["window_ms"])
		}
	case <-time.After(time.Second):
		t.Fatal("audio flood did not produce an operational diagnostic")
	}
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("a genuine wire flood left the media socket open")
	}
}

type audioFloodLog struct{ records chan map[string]any }

func (w audioFloodLog) Write(p []byte) (int, error) {
	var record map[string]any
	if json.Unmarshal(p, &record) == nil && record["msg"] == "session rate limit exceeded" {
		select {
		case w.records <- record:
		default:
		}
	}
	return len(p), nil
}

func TestSimulatorQuietAndResumedWireAudioStaysWithin512Kbps(t *testing.T) {
	ts, receiver, controller, snap := simulatorHarness(t)
	receiver.cfg.Duplex.MaxAudioKbps = 512
	client := dialRealtime(t, ts, simulatorPath(snap))
	defer client.conn.Close()
	client.send(map[string]any{"type": "session.update", "session": map[string]any{"input_audio_format": "pcm16"}})
	client.first("session.updated", time.Second)
	bound, _ := controller.Get(context.Background(), snap.RunID)
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "answered", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	client.first("response.audio.done", 3*time.Second)
	for range 3 {
		// Let the real wall-clock filler run, then resume gateway-shaped
		// 640-byte PCM16 frames at 50 fps, including a voiced boundary.
		time.Sleep(300 * time.Millisecond)
		client.say(voicedPCM(16000, 200), 16000)
		client.say(make([]byte, 32000), 16000)
	}
	select {
	case <-client.done:
		t.Fatal("ordinary 16 kHz media after silence fill closed the socket")
	default:
	}
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
}

func TestSimulatorFillDoesNotSpendTheReceivedAudioBudget(t *testing.T) {
	ts, receiver, controller, snap := simulatorHarness(t)
	// 1 kbps permits 125 wire bytes. One 640-byte synthetic frame alone would
	// trip this limit if it ever went through the client flood guard.
	receiver.cfg.Duplex.MaxAudioKbps = 1
	client := dialRealtime(t, ts, simulatorPath(snap))
	defer client.conn.Close()
	client.send(map[string]any{"type": "session.update", "session": map[string]any{"input_audio_format": "pcm16"}})
	client.first("session.updated", time.Second)
	bound, _ := controller.Get(context.Background(), snap.RunID)
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "answered", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	session, err := receiver.session(bound.Binding)
	if err != nil {
		t.Fatal(err)
	}
	await(t, "the wall-clock filler to actually push a frame", time.Second, func() bool {
		line := &session.sim.input
		line.mu.Lock()
		defer line.mu.Unlock()
		return !line.lastFill.IsZero()
	})
	client.send(map[string]string{"type": "input_audio_buffer.append", "audio": "AAA="})
	// A response after the append is a wire-level barrier: both the fill and
	// the real PCM sample left the client connection operational.
	client.send(map[string]any{"type": "session.update", "session": map[string]any{}})
	await(t, "session still answers after filled and received audio", time.Second, func() bool { return client.count("session.updated") == 2 })
	select {
	case <-client.done:
		t.Fatal("synthetic fill consumed the received audio budget")
	default:
	}
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
}

func TestAudioBudgetCountsDecodedBytesAndRetainsTheFloodBoundary(t *testing.T) {
	var rate byteRate
	for frame := range 100 {
		if !rate.allowAudio(640, 512) {
			t.Fatalf("20 ms frame %d exhausted the 64,000-byte budget early", frame+1)
		}
	}
	if rate.allowAudio(2, 512) {
		t.Fatal("real wire bytes beyond 64,000 were admitted")
	}
	rate.start = time.Now().Add(-time.Second)
	if !rate.allowAudio(640, 512) {
		t.Fatal("a new one-second window did not restore the audio budget")
	}
	if !rate.allowEvent(1) || rate.allowEvent(1) {
		t.Fatal("audio accounting changed the independent event budget")
	}
	if !rate.allowAudio(1<<20, 0) {
		t.Fatal("explicitly unlimited audio was refused")
	}
}

// This exercises the ordinary endpoint as well as the simulator path above:
// the new diagnostic must not change the existing closed-event contract.
func TestAudioFloodKeepsTheOrdinarySessionCloseReason(t *testing.T) {
	cfg := config.Default()
	cfg.Greeting = ""
	cfg.Duplex.MaxAudioKbps = 512
	ts := serve(t, cfg)
	conn := dial(t, ts)
	defer conn.Close()
	r := startReader(conn)
	startSession(t, conn, cfg.ClientRate)
	await(t, "session.started", time.Second, func() bool { return r.count(live.ServerSessionStarted) > 0 })
	send(t, conn, live.InputAudioAppendEvent{
		Envelope: live.Envelope{Type: live.ClientInputAudioAppend},
		Audio:    base64.StdEncoding.EncodeToString(make([]byte, 64002)),
	})
	await(t, "audio flood close", time.Second, func() bool { return r.count(live.ServerSessionClosed) > 0 })
	if got := r.closeReason(); got != live.CloseFlooded {
		t.Fatalf("close reason = %q, want %q", got, live.CloseFlooded)
	}
}
