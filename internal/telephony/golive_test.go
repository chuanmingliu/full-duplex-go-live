package telephony

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	_ "github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/server"
)

// Use the actual golive Realtime implementation and mock speech providers, not
// a duplicate event schema. Verify an opening reaches RTP and remote BYE ends
// the call without leaking the session or calling production services.
func TestRealGoliveOpeningAndRemoteHangup(t *testing.T) {
	cfg := config.Default()
	cfg.ASR = "mock"
	cfg.LLM = "mock"
	cfg.TTS = "mock"
	cfg.AuthRequired = false
	cfg.MaxSessions = 1
	cfg.Realtime.Enabled = true
	cfg.Realtime.HoldForOpening = true
	cfg.Realtime.HoldTimeoutSeconds = 120
	cfg.Duplex.PlaybackPaced = true
	cfg.Duplex.PlaybackLeadMS = 100
	golive := server.NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	httpServer := httptest.NewServer(golive.Handler())
	defer httpServer.Close()
	c := testConfig(t)
	c.GoliveURL = httpServer.URL
	pbx, rtp := udpSocket(t), udpSocket(t)
	c.PBX = pbx.LocalAddr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := Run(ctx, c, Call{ID: "actual-golive", Instructions: "Be brief.", Greeting: "Hello."})
		done <- e
	}()
	req, peer := expectMethod(t, pbx, "INVITE")
	sipReply(t, pbx, peer, req, 200, answerSDP(8, rtp.LocalAddr().(*net.UDPAddr).Port), "")
	ack, _ := expectMethod(t, pbx, "ACK")
	packet, _ := readUDP(t, rtp)
	if _, _, _, data, ok := parseRTP(packet, 8); !ok || len(data) != 160 {
		t.Fatal("actual golive opening did not produce G.711 RTP")
	}
	bye := fmt.Sprintf("BYE sip:test@%s SIP/2.0\r\nVia: SIP/2.0/UDP %s;branch=z9hG4bK-remote-bye\r\nFrom: %s\r\nTo: %s\r\nCall-ID: %s\r\nCSeq: 1 BYE\r\nMax-Forwards: 70\r\nContent-Length: 0\r\n\r\n", peer, pbx.LocalAddr(), header(string(ack), "To"), header(string(ack), "From"), header(string(ack), "Call-ID"))
	_, _ = pbx.WriteToUDP([]byte(bye), peer)
	res, _ := readUDP(t, pbx)
	if !strings.HasPrefix(string(res), "SIP/2.0 200 ") {
		t.Fatalf("remote BYE not acknowledged: %s", strings.SplitN(string(res), "\r\n", 2)[0])
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("remote BYE left call alive")
	}
}
