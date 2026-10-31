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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type simResult struct {
	stats MediaStats
	err   error
}
type simCall struct {
	c        Config
	agent    *fakeAgent
	pbx, rtp *net.UDPConn
	peer     *net.UDPAddr
	invite   []byte
	cancel   context.CancelFunc
	done     chan simResult
}

func startSim(t *testing.T, codec string) *simCall {
	t.Helper()
	s := &simCall{c: testConfig(t), pbx: udpSocket(t), rtp: udpSocket(t), done: make(chan simResult, 1)}
	s.c.Codec = codec
	s.c.PBX = s.pbx.LocalAddr().String()
	s.agent = newFakeAgent(t, s.c.wireFormat())
	s.c.GoliveURL = s.agent.server.URL
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	t.Cleanup(cancel)
	go func() { stats, err := Run(ctx, s.c, Call{ID: "sim"}); s.done <- simResult{stats, err} }()
	s.invite, s.peer = expectMethod(t, s.pbx, "INVITE")
	return s
}
func (s *simCall) answer(t *testing.T) {
	t.Helper()
	sipReply(t, s.pbx, s.peer, s.invite, 200, answerSDP(s.c.payloadType(), s.rtp.LocalAddr().(*net.UDPAddr).Port), "")
	expectMethod(t, s.pbx, "ACK")
	waitSignal(t, s.agent.opened)
}
func (s *simCall) finish(t *testing.T, acknowledge bool) simResult {
	t.Helper()
	bye, peer := expectMethod(t, s.pbx, "BYE")
	if acknowledge {
		sipReply(t, s.pbx, peer, bye, 200, "", "")
	}
	select {
	case result := <-s.done:
		return result
	case <-time.After(4 * time.Second):
		t.Fatal("call cleanup exceeded bound")
	}
	return simResult{}
}
func delta(kind, id string, b []byte) map[string]any {
	return map[string]any{"type": kind, "response_id": id, "delta": base64.StdEncoding.EncodeToString(b)}
}

func TestSimInterruptQueuedSpeech(t *testing.T) {
	for _, codec := range []string{"pcma", "pcmu"} {
		for _, kind := range []string{"response.audio", "response.output_audio"} {
			t.Run(codec+"/"+kind, func(t *testing.T) {
				s := startSim(t, codec)
				s.answer(t)
				s.agent.send <- map[string]any{"type": "response.created", "response": map[string]any{"id": "old"}}
				s.agent.send <- delta(kind+".delta", "old", bytes.Repeat([]byte{0x11}, 20*160))
				readUDP(t, s.rtp)
				s.agent.send <- map[string]any{"type": "input_audio_buffer.speech_started"}
				s.agent.send <- delta(kind+".delta", "old", bytes.Repeat([]byte{0x11}, 160))
				s.agent.send <- map[string]any{"type": "response.created", "response": map[string]any{"id": "new"}}
				s.agent.send <- delta(kind+".delta", "new", bytes.Repeat([]byte{0x72}, 3*160))
				s.agent.send <- map[string]any{"type": "response.done", "response": map[string]any{"id": "old", "status": "cancelled"}}
				s.agent.send <- map[string]any{"type": kind + ".done", "response_id": "new"}
				seen := 0
				for n := 0; n < 24 && seen < 3; n++ {
					b, _ := readUDP(t, s.rtp)
					_, _, _, p, ok := parseRTP(b, s.c.payloadType())
					if !ok {
						t.Fatal("invalid audio")
					}
					if p[0] == 0x72 {
						seen++
					} else if seen > 0 {
						t.Fatal("old response resumed after replacement")
					}
				}
				if seen != 3 {
					t.Fatal("replacement was cut by old completion")
				}
				s.cancel()
				result := s.finish(t, true)
				if result.stats.Cleared == 0 || result.stats.Stale != 1 {
					t.Fatalf("interruption counters: %+v", result.stats)
				}
			})
		}
	}
}

func TestSimAgentFailuresHangUp(t *testing.T) {
	cases := []struct {
		name, want string
		event      any
	}{
		{"invalid-base64", "invalid agent audio", map[string]any{"type": "response.audio.delta", "response_id": "r", "delta": "%%%"}},
		{"missing-response-id", "lacks response identity", delta("response.audio.delta", "", make([]byte, 160))},
		{"queue-overflow", "queue exceeded", delta("response.audio.delta", "r", make([]byte, 8000))},
		{"agent-error", "agent reported", map[string]any{"type": "error"}},
		{"disconnect", "connection closed", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := startSim(t, "pcma")
			s.answer(t)
			if tc.event == nil {
				close(s.agent.disconnect)
			} else {
				s.agent.send <- tc.event
			}
			result := s.finish(t, true)
			if result.err == nil || !strings.Contains(result.err.Error(), tc.want) {
				t.Fatalf("wrong error: %v", result.err)
			}
			if _, e := os.Stat(filepath.Join(s.c.RecordDir, ".active-call")); !os.IsNotExist(e) {
				t.Fatal("call lock leaked")
			}
			conn, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: s.c.RTPMin})
			if e != nil {
				t.Fatal("RTP port leaked", e)
			}
			conn.Close()
			var summary struct{ Failed bool }
			data, e := os.ReadFile(filepath.Join(s.c.RecordDir, "sim", "summary.json"))
			if e != nil {
				t.Fatal(e)
			}
			if json.Unmarshal(data, &summary) != nil || !summary.Failed {
				t.Fatal("failure was not recorded")
			}
		})
	}
}

func TestSimUnsafeAnswerIsHungUp(t *testing.T) {
	s := startSim(t, "pcma")
	body := strings.ReplaceAll(answerSDP(8, s.rtp.LocalAddr().(*net.UDPAddr).Port), "127.0.0.1", "192.0.2.99")
	sipReply(t, s.pbx, s.peer, s.invite, 200, body, "")
	expectMethod(t, s.pbx, "ACK")
	result := s.finish(t, true)
	if result.err == nil || !strings.Contains(result.err.Error(), "allowed CIDRs") {
		t.Fatalf("unsafe SDP: %v", result.err)
	}
	select {
	case <-s.agent.opened:
		t.Fatal("unsafe answer released opening")
	default:
	}
}

func TestSimByeTimeoutIsReported(t *testing.T) {
	s := startSim(t, "pcma")
	s.answer(t)
	s.cancel()
	result := s.finish(t, false)
	if result.err == nil || !strings.Contains(result.err.Error(), "hangup was not acknowledged") {
		t.Fatalf("lost BYE acknowledgement hidden: %v", result.err)
	}
}

func TestSimRepeatedAnswerIsAcked(t *testing.T) {
	s := startSim(t, "pcma")
	s.answer(t)
	sipReply(t, s.pbx, s.peer, s.invite, 200, answerSDP(8, s.rtp.LocalAddr().(*net.UDPAddr).Port), "")
	expectMethod(t, s.pbx, "ACK")
	select {
	case <-s.agent.opened:
		t.Fatal("duplicate answer opened a second session")
	default:
	}
	s.cancel()
	s.finish(t, true)
}

func TestSimCancellationDuringAgentSetup(t *testing.T) {
	c := testConfig(t)
	ready := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pool", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"service":"golive","in_use":0,"capacity":1,"realtime":{"hold_for_opening":true}}`)
	})
	mux.HandleFunc("/v1/realtime", func(w http.ResponseWriter, r *http.Request) {
		ws, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		var ev any
		if ws.ReadJSON(&ev) != nil {
			return
		}
		close(ready)
		_, _, _ = ws.ReadMessage()
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	c.GoliveURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := Run(ctx, c, Call{ID: "cancel-setup"}); done <- e }()
	waitSignal(t, ready)
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancelled setup succeeded")
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("cancellation failed to unblock agent setup")
	}
}

func TestSimImpairedInboundRTP(t *testing.T) {
	for _, codec := range []string{"pcma", "pcmu"} {
		t.Run(codec, func(t *testing.T) {
			s := startSim(t, codec)
			s.answer(t)
			dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: s.c.RTPMin}
			rogue := udpSocket(t)
			sent := make(chan error, 1)
			go func() {
				write := func(conn *net.UDPConn, b []byte) error { _, err := conn.WriteToUDP(b, dst); return err }
				for i := 1; i <= 101; i++ {
					if i%10 == 0 || i == 6 {
						continue
					}
					seqs := []int{i}
					if i == 5 {
						seqs = []int{6, 5}
					}
					for _, seq := range seqs {
						packet := testRTP(s.c.payloadType(), uint16(seq), uint32((seq-1)*160), bytes.Repeat([]byte{byte(seq)}, 160))
						if err := write(s.rtp, packet); err != nil {
							sent <- err
							return
						}
						if seq%7 == 0 {
							if err := write(s.rtp, packet); err != nil {
								sent <- err
								return
							}
						}
					}
					if i == 1 {
						bad := testRTP(s.c.payloadType(), 2, 160, make([]byte, 160))
						if err := write(rogue, bad); err != nil {
							sent <- err
							return
						}
						bad[1] = 127
						if err := write(s.rtp, bad); err != nil {
							sent <- err
							return
						}
						bad[1] = s.c.payloadType()
						binary.BigEndian.PutUint32(bad[8:], 99)
						if err := write(s.rtp, bad); err != nil {
							sent <- err
							return
						}
					}
					time.Sleep(time.Millisecond)
				}
				sent <- nil
			}()
			samples := 0
			deadline := time.NewTimer(4 * time.Second)
			defer deadline.Stop()
			for {
				select {
				case b := <-s.agent.input:
					samples += len(b)
					if len(b) > 0 && b[0] == 101 {
						goto received
					}
				case <-deadline.C:
					t.Fatal("impaired stream stalled")
				}
			}
		received:
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			if samples != 101*160 {
				t.Fatalf("silence did not preserve RTP timeline: %d samples", samples)
			}
			s.cancel()
			result := s.finish(t, true)
			if result.stats.Received != 90 || result.stats.Lost != 11 || result.stats.Late != 14 || result.stats.Rejected != 3 {
				t.Fatalf("impaired media counters: %+v", result.stats)
			}
		})
	}
}
