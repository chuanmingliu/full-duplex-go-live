package telephony

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/server"
)

func TestSimGoliveSpeechPipeline(t *testing.T) {
	// Fix mock settings so an operator's shell cannot silently change this test.
	for _, key := range []string{"ASR_DIAL_MS", "ASR_LAG_MS", "ASR_FINAL_MS", "PHRASES", "TTS_DIAL_MS", "TTS_SERIAL", "LLM_RUNE_MS"} {
		t.Setenv("GOLIVE_MOCK_"+key, "")
	}
	t.Setenv("GOLIVE_MOCK_TRANSCRIPT", "测试问题")
	t.Setenv("GOLIVE_MOCK_ASR_MS_PER_RUNE", "100")
	t.Setenv("GOLIVE_MOCK_ASR_VOICED_ONLY", "true")
	t.Setenv("GOLIVE_MOCK_LLM_TTFT_MS", "80")
	t.Setenv("GOLIVE_MOCK_TTS_TTFB_MS", "60")
	for _, codec := range []string{"pcma", "pcmu"} {
		for _, dialect := range []string{"beta", "ga"} {
			t.Run(codec+"/"+dialect, func(t *testing.T) {
				cfg := config.Default()
				cfg.ASR = "mock"
				cfg.LLM = "mock"
				cfg.TTS = "mock"
				cfg.AuthRequired = false
				cfg.MaxSessions = 1
				cfg.Realtime.Enabled = true
				cfg.Realtime.HoldForOpening = true
				cfg.Realtime.HoldTimeoutSeconds = 120
				cfg.Realtime.Dialect = dialect
				cfg.VAD.MinSilenceMS = 200
				cfg.Duplex.Speculative = false
				cfg.Duplex.PlaybackPaced = true
				cfg.Duplex.PlaybackLeadMS = 100
				srv := server.NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
				httpServer := httptest.NewServer(srv.Handler())
				defer httpServer.Close()
				c := testConfig(t)
				c.Codec = codec
				c.MaxDuration = 15 * time.Second
				c.GoliveURL = httpServer.URL
				pbx, rtp := udpSocket(t), udpSocket(t)
				c.PBX = pbx.LocalAddr().String()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan simResult, 1)
				go func() {
					stats, e := Run(ctx, c, Call{ID: "speech-pipeline", Instructions: "Answer briefly."})
					done <- simResult{stats, e}
				}()
				req, peer := expectMethod(t, pbx, "INVITE")
				sipReply(t, pbx, peer, req, 200, answerSDP(c.payloadType(), rtp.LocalAddr().(*net.UDPAddr).Port), "")
				expectMethod(t, pbx, "ACK")
				opened := false
				for until := time.Now().Add(time.Second); time.Now().Before(until); {
					var pool struct{ Sessions []struct{ Opened bool } }
					if e := agentHTTP(ctx, c, "/v1/pool", nil, &pool); e == nil && len(pool.Sessions) == 1 && pool.Sessions[0].Opened {
						opened = true
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !opened {
					t.Fatal("actual GoLive session never opened")
				}
				// Receive independently so sender pacing does not contaminate the timestamp.
				first := make(chan time.Time, 1)
				recvErr := make(chan error, 1)
				_ = rtp.SetReadDeadline(time.Now().Add(6 * time.Second))
				go func() {
					buf := make([]byte, 2048)
					n, _, e := rtp.ReadFromUDP(buf)
					if e != nil {
						recvErr <- e
						return
					}
					_, _, _, p, ok := parseRTP(buf[:n], c.payloadType())
					if !ok || len(p) != 160 {
						recvErr <- io.ErrUnexpectedEOF
						return
					}
					first <- time.Now()
				}()
				samples := make([]float32, 160)
				for i := range samples {
					samples[i] = float32(0.3 * math.Sin(2*math.Pi*400*float64(i)/8000))
				}
				wireCodec := audio.CodecALaw
				if codec == "pcmu" {
					wireCodec = audio.CodecMuLaw
				}
				speech := wireCodec.FromPCM16(audio.EncodePCM16(samples))
				silence := make([]byte, 160)
				for i := range silence {
					silence[i] = c.silence()
				}
				dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: c.RTPMin}
				tick := time.NewTicker(20 * time.Millisecond)
				defer tick.Stop()
				var endSpeech time.Time
				for i := 0; i < 100; i++ {
					payload := speech
					if i >= 50 {
						payload = silence
					}
					if _, e := rtp.WriteToUDP(testRTP(c.payloadType(), uint16(i+1), uint32(i*160), payload), dst); e != nil {
						t.Fatal(e)
					}
					if i == 49 {
						endSpeech = time.Now().Add(20 * time.Millisecond)
					}
					<-tick.C
				}
				var arrival time.Time
				select {
				case arrival = <-first:
				case e := <-recvErr:
					t.Fatal(e)
				case <-time.After(5 * time.Second):
					t.Fatal("ASR/LLM/TTS produced no RTP")
				}
				if arrival.Before(endSpeech) {
					t.Fatal("unexpected pre-endpoint audio in non-speculative test")
				}
				cancel()
				bye, peer := expectMethod(t, pbx, "BYE")
				sipReply(t, pbx, peer, bye, 200, "", "")
				select {
				case result := <-done:
					if result.stats.Received < 90 || result.stats.Sent == 0 {
						t.Fatalf("pipeline did not exchange audio: %+v", result.stats)
					}
				case <-time.After(4 * time.Second):
					t.Fatal("pipeline cleanup timed out")
				}
				events, e := os.ReadFile(filepath.Join(c.RecordDir, "speech-pipeline", "events.jsonl"))
				if e != nil {
					t.Fatal(e)
				}
				recognized := false
				for _, line := range strings.Split(strings.TrimSpace(string(events)), "\n") {
					var ev struct {
						Type string
						Data struct{ Event, Text string }
					}
					if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "transcript" && ev.Data.Event == "conversation.item.input_audio_transcription.completed" && ev.Data.Text == "测试问题" {
						recognized = true
					}
				}
				if !recognized {
					t.Fatal("synthetic input transcript missing from call record")
				}
				pcm, rate, e := audio.ReadWAV(filepath.Join(c.RecordDir, "speech-pipeline", "outbound.wav"))
				if e != nil || rate != 8000 || len(pcm) == 0 {
					t.Fatal("output recording missing")
				}
				t.Logf("mock speech-end to first received RTP: %.1f ms (not handset latency)", float64(arrival.Sub(endSpeech).Microseconds())/1000)
			})
		}
	}
}

func FuzzSDPAnswer(f *testing.F) {
	f.Add(offerSDP("127.0.0.1", 31000, 8))
	f.Add([]byte("m=audio 0 RTP/AVP 8\r\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		addr, e := parseAnswer(b, 8, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
		if e == nil && (addr.Port < 1 || !addr.IP.Equal(net.IPv4(127, 0, 0, 1))) {
			t.Fatal("SDP escaped media allowlist")
		}
	})
}
