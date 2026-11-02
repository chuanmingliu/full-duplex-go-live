package backchannel

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func await(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for !f() {
		if time.Now().After(until) {
			t.Fatal("deadline")
		}
		time.Sleep(time.Millisecond)
	}
}
func server(t *testing.T, slow bool) string {
	t.Helper()
	up := websocket.Upgrader{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var hello map[string]any
		if c.ReadJSON(&hello) != nil {
			return
		}
		if hello["language"] != "zh" || hello["sample_rate"] != float64(16000) {
			t.Error("wrong hello")
		}
		c.WriteJSON(map[string]any{"type": "ready", "mode": "shadow", "fallback": "legacy", "control_allowed": false})
		for {
			var p struct {
				Seq int
				PCM string `json:"pcm_s16le"`
			}
			if c.ReadJSON(&p) != nil {
				return
			}
			raw, _ := base64.StdEncoding.DecodeString(p.PCM)
			if len(raw) != 1280 {
				t.Error("wrong audio size")
			}
			if slow {
				time.Sleep(250 * time.Millisecond)
			}
			if c.WriteJSON(map[string]any{"type": "prediction", "seq": p.Seq, "fallback": "legacy", "frames": []any{map[string]float64{"p_floor": .9, "p_backchannel": .1}, map[string]float64{"p_floor": .9, "p_backchannel": .1}}}) != nil {
				return
			}
		}
	}))
	t.Cleanup(s.Close)
	return "ws" + strings.TrimPrefix(s.URL, "http")
}
func client(t *testing.T, slow bool) *Client {
	t.Helper()
	c := New(context.Background(), server(t, slow), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(c.Close)
	await(t, c.Ready)
	return c
}
func TestFramesAndGenerationRemainFresh(t *testing.T) {
	c := client(t, false)
	c.Push(make([]byte, 640), 0, 1, true, false)
	c.Push(make([]byte, 640), 20, 2, true, false)
	await(t, func() bool { return c.Snapshot() != nil })
	if c.Snapshot().FloorFrames != 0 || !c.Ready() {
		t.Fatal("mixed generation must invalidate evidence without losing stream")
	}
	c.Push(make([]byte, 1280), 40, 2, true, false)
	await(t, func() bool { return c.Snapshot().EndMS == 80 })
	c.Push(make([]byte, 1280), 80, 2, true, false)
	await(t, func() bool { return c.Snapshot().EndMS == 120 })
	p := c.Snapshot()
	if !p.Eligible(time.Now(), 120, 40, 2) {
		t.Fatalf("not eligible: %+v", p)
	}
	if p.Eligible(time.Now(), 120, 60, 2) || p.Eligible(time.Now(), 120, 40, 3) || p.Eligible(time.Now().Add(time.Second), 120, 40, 2) {
		t.Fatal("stale evidence accepted")
	}
}
func TestGapAndOverloadFallBackWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"gap", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			c := client(t, true)
			at := time.Now()
			c.Push(make([]byte, 1280), 0, 1, true, false)
			if kind == "gap" {
				c.Push(make([]byte, 1280), 100, 1, true, false)
			} else {
				for n := 1; n < 64; n++ {
					c.Push(make([]byte, 1280), int64(n*40), 1, true, false)
				}
			}
			if time.Since(at) > 50*time.Millisecond {
				t.Fatal("input blocked")
			}
			if c.Ready() || c.Snapshot() != nil {
				t.Fatal("did not fall back")
			}
		})
	}
}
func TestServiceTimeoutAndSessionIsolation(t *testing.T) {
	c := client(t, true)
	c.Push(make([]byte, 1280), 0, 1, true, false)
	await(t, func() bool { return !c.Ready() })
	if c.Snapshot() != nil {
		t.Fatal("timeout leaked result")
	}
	fresh := client(t, false)
	if fresh.Snapshot() != nil {
		t.Fatal("state leaked")
	}
}

func TestDeployedModelStream(t *testing.T) {
	url := os.Getenv("GOLIVE_TEST_BACKCHANNEL_URL")
	if url == "" {
		t.Skip("explicit deployed-model smoke")
	}
	c := New(context.Background(), url, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer c.Close()
	await(t, c.Ready)
	for i := 0; i < 20; i++ {
		c.Push(make([]byte, 1280), int64(i*40), 1, false, false)
		end := int64((i + 1) * 40)
		await(t, func() bool { return c.Snapshot() != nil && c.Snapshot().EndMS == end })
	}
	c.Push(make([]byte, 640*32), 800, 1, false, false)
	await(t, func() bool { return c.Snapshot() != nil && c.Snapshot().EndMS == 1440 })
	if !c.Ready() {
		t.Fatal("model stream unavailable")
	}
}

func TestBatchedInputCatchesUpWithoutAcceptingOldEvidence(t *testing.T) {
	c := client(t, false)
	// WebSocket delivery can coalesce audio. Preserve every model frame even
	// though none of the early catch-up predictions is current enough to act.
	c.Push(make([]byte, 320*32), 0, 1, true, false)
	await(t, func() bool { return c.Snapshot() != nil && c.Snapshot().EndMS == 320 })
	if !c.Ready() {
		t.Fatal("bounded input burst disabled healthy model")
	}
	p := c.Snapshot()
	if !p.Eligible(time.Now(), 320, 0, 1) || p.Eligible(time.Now(), 480, 0, 1) {
		t.Fatal("catch-up changed input freshness rules")
	}
}

func TestQueuedBurstStillExpires(t *testing.T) {
	c := &Client{Updates: make(chan struct{}, 1), queue: make(chan packet, 32), done: make(chan struct{}), cancel: func() {}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.queue <- packet{At: time.Now().Add(-time.Second)}
	c.run(context.Background(), server(t, false))
	if c.Ready() || c.Snapshot() != nil || !c.failed.Load() {
		t.Fatal("stale queued burst retained evidence")
	}
}
