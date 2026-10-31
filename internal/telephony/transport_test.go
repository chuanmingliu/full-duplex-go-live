package telephony

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/gorilla/websocket"
)

func udpSocket(t *testing.T) *net.UDPConn {
	t.Helper()
	c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func testConfig(t *testing.T) Config {
	t.Helper()
	portSocket := udpSocket(t)
	port := portSocket.LocalAddr().(*net.UDPAddr).Port
	portSocket.Close()
	return Config{SIPListen: "127.0.0.1:0", AdvertiseIP: "127.0.0.1", PBX: "127.0.0.1:5060", AllowedURI: "sip:test@127.0.0.1", FromUser: "test", GoliveURL: "http://127.0.0.1:9875", RecordDir: t.TempDir(), Codec: "pcma", RTPMin: port, RTPMax: port, QueueMS: 500, MediaNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, RingTimeout: 2 * time.Second, MaxDuration: 5 * time.Second}
}
func readUDP(t *testing.T, c *net.UDPConn) ([]byte, *net.UDPAddr) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	b := make([]byte, 65536)
	n, a, e := c.ReadFromUDP(b)
	if e != nil {
		t.Fatal(e)
	}
	return b[:n], a
}
func header(msg, name string) string {
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(name)+":") {
			return strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}
	return ""
}

// Replies are deliberately built from raw datagrams, independent of sipgo's
// response builders and dialog machinery.
func sipReply(t *testing.T, c *net.UDPConn, to *net.UDPAddr, req []byte, status int, body, extra string) {
	t.Helper()
	s := string(req)
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d Test\r\n", status)
	for _, h := range []string{"Via", "From", "To", "Call-ID", "CSeq"} {
		v := header(s, h)
		if h == "To" && !strings.Contains(v, ";tag=") {
			v += ";tag=test-pbx"
		}
		fmt.Fprintf(&b, "%s: %s\r\n", h, v)
	}
	fmt.Fprintf(&b, "Contact: <sip:pbx@%s>\r\n%sContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s", c.LocalAddr(), extra, len(body), body)
	if _, e := c.WriteToUDP([]byte(b.String()), to); e != nil {
		t.Fatal(e)
	}
}
func expectMethod(t *testing.T, c *net.UDPConn, want string) ([]byte, *net.UDPAddr) {
	t.Helper()
	for i := 0; i < 8; i++ {
		b, a := readUDP(t, c)
		if strings.HasPrefix(string(b), want+" ") {
			return b, a
		}
		if !strings.HasPrefix(string(b), "INVITE ") {
			t.Fatalf("wanted %s, got %s", want, strings.SplitN(string(b), "\r\n", 2)[0])
		}
	}
	t.Fatal("too many retransmissions")
	return nil, nil
}
func testRTP(pt byte, seq uint16, ts uint32, payload []byte) []byte {
	b := make([]byte, 12+len(payload))
	b[0] = 0x80
	b[1] = pt
	binary.BigEndian.PutUint16(b[2:], seq)
	binary.BigEndian.PutUint32(b[4:], ts)
	binary.BigEndian.PutUint32(b[8:], 42)
	copy(b[12:], payload)
	return b
}

type fakeAgent struct {
	server     *httptest.Server
	input      chan []byte
	opened     chan struct{}
	send       chan any
	disconnect chan struct{}
}

func newFakeAgent(t *testing.T, format string) *fakeAgent {
	t.Helper()
	a := &fakeAgent{input: make(chan []byte, 32), opened: make(chan struct{}, 8), send: make(chan any, 32), disconnect: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pool", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"service":"golive","in_use":0,"capacity":1,"realtime":{"hold_for_opening":true}}`)
	})
	mux.HandleFunc("/v1/opening", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CallID string `json:"callId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.opened <- struct{}{}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "kicked": []string{body.CallID}})
	})
	mux.HandleFunc("/v1/realtime", func(w http.ResponseWriter, r *http.Request) {
		ws, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		var first map[string]any
		if ws.ReadJSON(&first) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"type": "session.updated", "session": map[string]any{"input_audio_format": format, "output_audio_format": format}})
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case ev := <-a.send:
					if close, ok := ev.(*websocket.CloseError); ok {
						_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(close.Code, close.Text), time.Now().Add(time.Second))
						return
					}
					if ws.WriteJSON(ev) != nil {
						return
					}
				case <-done:
					return
				case <-a.disconnect:
					ws.Close()
					return
				}
			}
		}()
		for {
			var ev struct{ Type, Audio string }
			if ws.ReadJSON(&ev) != nil {
				return
			}
			if ev.Type == "input_audio_buffer.append" {
				b, e := base64.StdEncoding.DecodeString(ev.Audio)
				if e == nil {
					a.input <- b
				}
			}
		}
	})
	a.server = httptest.NewServer(mux)
	t.Cleanup(a.server.Close)
	return a
}
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatal("signal timed out")
	}
}
func answerSDP(pt byte, port int) string {
	codec := "PCMA"
	if pt == 0 {
		codec = "PCMU"
	}
	return fmt.Sprintf("v=0\r\no=test 1 1 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/AVP %d\r\na=rtpmap:%d %s/8000\r\na=sendrecv\r\n", port, pt, pt, codec)
}

func TestDialBridge(t *testing.T) {
	for _, codec := range []string{"pcma", "pcmu"} {
		t.Run(codec, func(t *testing.T) {
			c := testConfig(t)
			c.Codec = codec
			pbx, rtp := udpSocket(t), udpSocket(t)
			c.PBX = pbx.LocalAddr().String()
			a := newFakeAgent(t, c.wireFormat())
			c.GoliveURL = a.server.URL
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := Run(ctx, c, Call{ID: "bridge", Greeting: "hello"}); done <- e }()
			invite, peer := expectMethod(t, pbx, "INVITE")
			var mediaPort int
			for _, line := range strings.Split(string(invite), "\r\n") {
				if strings.HasPrefix(line, "m=audio ") {
					fmt.Sscanf(line, "m=audio %d", &mediaPort)
				}
			}
			if mediaPort != c.RTPMin {
				t.Fatalf("unexpected SDP port %d", mediaPort)
			}
			sipReply(t, pbx, peer, invite, 180, "", "")
			dest := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: mediaPort}
			_, _ = rtp.WriteToUDP(testRTP(c.payloadType(), 1, 0, bytes.Repeat([]byte{0x11}, 160)), dest)
			time.Sleep(25 * time.Millisecond)
			select {
			case <-a.opened:
				t.Fatal("opening released before answer")
			case <-a.input:
				t.Fatal("early media forwarded")
			default:
			}
			sipReply(t, pbx, peer, invite, 200, answerSDP(c.payloadType(), rtp.LocalAddr().(*net.UDPAddr).Port), "")
			expectMethod(t, pbx, "ACK")
			waitSignal(t, a.opened)
			input := bytes.Repeat([]byte{0x34}, 160)
			_, _ = rtp.WriteToUDP(testRTP(c.payloadType(), 2, 160, input), dest)
			select {
			case got := <-a.input:
				if !bytes.Equal(got, input) {
					t.Fatal("ingress transcoded")
				}
			case <-time.After(time.Second):
				t.Fatal("no ingress")
			}
			a.send <- map[string]any{"type": "response.created", "response": map[string]any{"id": "r1"}}
			output := bytes.Repeat([]byte{0x72}, 337)
			for _, chunk := range [][]byte{output[:80], output[80:]} {
				a.send <- map[string]any{"type": "response.audio.delta", "response_id": "r1", "delta": base64.StdEncoding.EncodeToString(chunk)}
			}
			a.send <- map[string]any{"type": "response.audio.done", "response_id": "r1"}
			var prevSeq uint16
			var prevTS uint32
			var prevAt time.Time
			for i := 0; i < 3; i++ {
				b, _ := readUDP(t, rtp)
				at := time.Now()
				seq, ts, _, data, ok := parseRTP(b, c.payloadType())
				if !ok || len(data) != 160 {
					t.Fatal("invalid RTP frame")
				}
				want := bytes.Repeat([]byte{0x72}, 160)
				if i == 2 {
					for n := 17; n < 160; n++ {
						want[n] = c.silence()
					}
				}
				if !bytes.Equal(data, want) {
					t.Fatal("egress content or padding changed")
				}
				if i > 0 && (seq != prevSeq+1 || ts != prevTS+160 || at.Sub(prevAt) < 15*time.Millisecond) {
					t.Fatal("bad packet pacing/sequence")
				}
				prevSeq, prevTS, prevAt = seq, ts, at
			}
			cancel()
			bye, peer := expectMethod(t, pbx, "BYE")
			sipReply(t, pbx, peer, bye, 200, "", "")
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("call did not terminate")
			}
			for _, name := range []string{"inbound.wav", "outbound.wav"} {
				path := filepath.Join(c.RecordDir, "bridge", name)
				pcm, rate, e := audio.ReadWAV(path)
				if e != nil || rate != 8000 || len(pcm) == 0 {
					t.Fatalf("recording %s invalid: %v", name, e)
				}
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0600 {
					t.Fatal("recording permissions")
				}
			}
			if _, e := Run(context.Background(), c, Call{ID: "bridge"}); e == nil || !strings.Contains(e.Error(), "already exists") {
				t.Fatalf("duplicate call was accepted: %v", e)
			}
		})
	}
}

func TestCancelLateAnswer(t *testing.T) {
	for _, provisional := range []bool{false, true} {
		t.Run(fmt.Sprint(provisional), func(t *testing.T) {
			c := testConfig(t)
			pbx, rtp := udpSocket(t), udpSocket(t)
			c.PBX = pbx.LocalAddr().String()
			endpoint, e := newSIPEndpoint(c)
			if e != nil {
				t.Fatal(e)
			}
			defer endpoint.close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				d, e := endpoint.invite(ctx, c, "cancel", c.RTPMin, nil)
				if d != nil {
					endDialog(d, nil)
				}
				done <- e
			}()
			invite, peer := expectMethod(t, pbx, "INVITE")
			if provisional {
				sipReply(t, pbx, peer, invite, 180, "", "")
			}
			cancel()
			if provisional {
				req, p := expectMethod(t, pbx, "CANCEL")
				if header(string(req), "Via") != header(string(invite), "Via") {
					t.Fatal("CANCEL changed Via branch")
				}
				sipReply(t, pbx, p, req, 200, "", "")
			}
			sipReply(t, pbx, peer, invite, 200, answerSDP(8, rtp.LocalAddr().(*net.UDPAddr).Port), "")
			expectMethod(t, pbx, "ACK")
			bye, p := expectMethod(t, pbx, "BYE")
			sipReply(t, pbx, p, bye, 200, "", "")
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("late answer was orphaned")
			}
		})
	}
}

func TestRejectAndDigest(t *testing.T) {
	c := testConfig(t)
	pbx := udpSocket(t)
	c.PBX = pbx.LocalAddr().String()
	c.SIPUsername = "test"
	c.SIPPassword = "fixture-secret"
	endpoint, e := newSIPEndpoint(c)
	if e != nil {
		t.Fatal(e)
	}
	defer endpoint.close()
	done := make(chan error, 1)
	go func() { _, e := endpoint.invite(context.Background(), c, "digest", c.RTPMin, nil); done <- e }()
	req, peer := expectMethod(t, pbx, "INVITE")
	sipReply(t, pbx, peer, req, 401, "", "WWW-Authenticate: Digest realm=\"test\", nonce=\"nonce\", algorithm=MD5\r\n")
	expectMethod(t, pbx, "ACK")
	req, peer = expectMethod(t, pbx, "INVITE")
	if !strings.HasPrefix(header(string(req), "Authorization"), "Digest ") {
		t.Fatal("missing digest auth")
	}
	sipReply(t, pbx, peer, req, 486, "", "")
	expectMethod(t, pbx, "ACK")
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("rejection accepted")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("rejection stuck")
	}
}

func TestCancellationDoesNotRetryDigest(t *testing.T) {
	c := testConfig(t)
	pbx := udpSocket(t)
	c.PBX = pbx.LocalAddr().String()
	c.SIPUsername = "test"
	c.SIPPassword = "fixture-secret"
	e, err := newSIPEndpoint(c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.invite(ctx, c, "no-auth-redial", c.RTPMin, nil); done <- err }()
	req, peer := expectMethod(t, pbx, "INVITE")
	cancel()
	sipReply(t, pbx, peer, req, 401, "", "WWW-Authenticate: Digest realm=\"test\", nonce=\"nonce\", algorithm=MD5\r\n")
	expectMethod(t, pbx, "ACK")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled authentication succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled auth did not terminate")
	}
	_ = pbx.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
	buf := make([]byte, 2048)
	if _, _, err := pbx.ReadFromUDP(buf); err == nil {
		t.Fatal("authenticated redial after cancellation")
	}
}
