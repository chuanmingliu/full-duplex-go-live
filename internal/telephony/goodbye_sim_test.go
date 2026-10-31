package telephony

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/speko"
	"github.com/gorilla/websocket"
)

func TestSimSpekoReciprocalGoodbyeSendsSIPBye(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := websocket.Upgrader{}
		ws, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var e struct{ Type string }
		if ws.ReadJSON(&e) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		for ws.ReadJSON(&e) == nil {
			if e.Type != "session.commentary.append" {
				continue
			}
			pcm := make([]byte, 4800*2)
			for i := 0; i < len(pcm); i += 2 {
				binary.LittleEndian.PutUint16(pcm[i:], 2000)
			}
			_ = ws.WriteJSON(map[string]string{"type": "session.input_transcript.delta", "delta": "拜拜"})
			_ = ws.WriteJSON(map[string]string{"type": "session.output_transcript.delta", "delta": "嗯，拜拜。"})
			_ = ws.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(pcm)})
		}
	}))
	defer upstream.Close()
	relay, err := speko.New(speko.Config{URL: "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1/live", APIKey: "fixture", AuthToken: "fixture", MaxSessions: 1, AutoHangup: true})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Shutdown(context.Background())
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	c := testConfig(t)
	c.GoliveURL, c.GoliveToken = server.URL, "fixture"
	pbx, rtp := udpSocket(t), udpSocket(t)
	c.PBX = pbx.LocalAddr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Run(ctx, c, Call{ID: "goodbye", Greeting: "Hello"}); done <- err }()
	invite, peer := expectMethod(t, pbx, "INVITE")
	sipReply(t, pbx, peer, invite, 200, answerSDP(c.payloadType(), rtp.LocalAddr().(*net.UDPAddr).Port), "")
	expectMethod(t, pbx, "ACK")
	for i := 0; i < 10; i++ {
		readUDP(t, rtp)
	}
	bye, peer := expectMethod(t, pbx, "BYE")
	sipReply(t, pbx, peer, bye, 200, "", "")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("normal goodbye failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("reciprocal goodbye did not terminate SIP")
	}
}
