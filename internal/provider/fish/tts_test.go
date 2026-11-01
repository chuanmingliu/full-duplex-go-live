package fish

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
)

// A stand-in vendor: checks the request, streams PCM in pieces, and refuses
// one key.
func vendor(t *testing.T, got *[]map[string]any, models *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(http.StatusPaymentRequired)
			io.WriteString(w, `{"status":402,"message":"insufficient balance","reason":"billing"}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		*got = append(*got, body)
		*models = append(*models, r.Header.Get("model"))
		w.Header().Set("Content-Type", "application/octet-stream")
		flusher := w.(http.Flusher)
		// 3 pieces, one of them an odd number of bytes, as a chunked body may split.
		for i, n := range []int{641, 639, 320} {
			buf := make([]byte, n)
			for k := 0; k+1 < n; k += 2 {
				binary.LittleEndian.PutUint16(buf[k:], uint16(int16(100*(i+1))))
			}
			w.Write(buf)
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
}

func TestSynthesizeStreamsWholeSamplesAndSendsTheModel(t *testing.T) {
	var got []map[string]any
	var models []string
	srv := vendor(t, &got, &models)
	defer srv.Close()
	tts := &TTS{Endpoint: srv.URL, APIKeys: []string{"k1", "k2"}, VoiceID: "voice-1", Model: "s2.1-pro", Latency: "balanced", Speed: 1.2,
		HTTP: srv.Client(), Log: slog.Default()}
	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := st.Synthesize(context.Background(), " 好的，我看一下。 ")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for c := range chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		if len(c.PCM)%2 != 0 {
			t.Errorf("a chunk of %d bytes splits a sample", len(c.PCM))
		}
		total += len(c.PCM)
	}
	if total != 1600 {
		t.Errorf("%d bytes came back of 1600", total)
	}
	req := got[0]
	if req["text"] != "好的，我看一下。" || req["reference_id"] != "voice-1" || req["format"] != "pcm" || req["sample_rate"] != float64(16000) || req["latency"] != "balanced" {
		t.Errorf("request: %v", req)
	}
	if p, _ := req["prosody"].(map[string]any); p["speed"] != 1.2 {
		t.Errorf("prosody: %v", req["prosody"])
	}
	if models[0] != "s2.1-pro" {
		t.Errorf("model header %q", models[0])
	}

	// The model and voice asked for at Open win; lane 1 is the second key.
	st2, _ := tts.Open(provider.WithLane(context.Background(), 1), provider.TTSOptions{Model: "drama-3", Voice: "voice-2", Speed: 1})
	if st2.(*stream).Account() != 1 {
		t.Errorf("lane 1 opened on account %d", st2.(*stream).Account())
	}
	chunks, _ = st2.Synthesize(context.Background(), "x")
	for range chunks {
	}
	if models[1] != "drama-3" || got[1]["reference_id"] != "voice-2" || got[1]["prosody"] != nil || got[1]["sample_rate"] != float64(16000) {
		t.Errorf("second request: model %q, %v", models[1], got[1])
	}
	if _, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 22050}); err == nil {
		t.Error("a rate the vendor does not make was accepted")
	}
}

func TestARefusalSaysTheStatusAndACancelStopsTheStream(t *testing.T) {
	var got []map[string]any
	var models []string
	srv := vendor(t, &got, &models)
	defer srv.Close()
	tts := &TTS{Endpoint: srv.URL, APIKeys: []string{"bad"}, HTTP: srv.Client(), Log: slog.Default()}
	st, _ := tts.Open(context.Background(), provider.TTSOptions{})
	_, err := st.Synthesize(context.Background(), "x")
	var r *refusal
	if !errors.As(err, &r) || r.Status() != 402 || err == nil || !contains(err.Error(), "insufficient balance") {
		t.Fatalf("refusal: %v", err)
	}

	tts.APIKeys = []string{"ok"}
	st, _ = tts.Open(context.Background(), provider.TTSOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	chunks, err := st.Synthesize(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	<-chunks
	cancel()
	done := make(chan struct{})
	go func() {
		for range chunks {
			n.Add(1)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream did not close after a cancel")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
