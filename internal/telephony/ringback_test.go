package telephony

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
)

func TestSimCarrierRingbackRecordedOutsideAgent(t *testing.T) {
	for _, codec := range []string{"pcma", "pcmu"} {
		t.Run(codec, func(t *testing.T) {
			s := startSim(t, codec)
			early := udpSocket(t)
			body := strings.ReplaceAll(answerSDP(s.c.payloadType(), early.LocalAddr().(*net.UDPAddr).Port), "sendrecv", "sendonly")
			sipReply(t, s.pbx, s.peer, s.invite, 183, body, "")
			path := filepath.Join(s.c.RecordDir, "sim")
			waitFile := func(name string, match func([]byte) bool) []byte {
				t.Helper()
				for until := time.Now().Add(time.Second); time.Now().Before(until); {
					b, _ := os.ReadFile(filepath.Join(path, name))
					if match(b) {
						return b
					}
					time.Sleep(time.Millisecond)
				}
				t.Fatalf("waiting for %s", name)
				return nil
			}
			waitFile("events.jsonl", func(b []byte) bool { return bytes.Contains(b, []byte(`"type":"early_media"`)) })
			tone := bytes.Repeat([]byte{0x31}, 160)
			packet := testRTP(s.c.payloadType(), 65535, 9000, tone)
			binary.BigEndian.PutUint32(packet[8:], 99)
			dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: s.c.RTPMin}
			_, _ = early.WriteToUDP(packet, dst)
			waitFile("ringback.wav", func(b []byte) bool { return len(b) > 44 })
			_, _ = early.WriteToUDP(packet, dst) // Duplicate early media must not be recorded twice.
			select {
			case <-s.agent.input:
				t.Fatal("ringback leaked to ASR")
			case <-s.agent.opened:
				t.Fatal("ringback released greeting")
			default:
			}
			s.answer(t) // A different peer, SSRC, sequence and timestamp starts the answered stream.
			voice := bytes.Repeat([]byte{0x72}, 160)
			_, _ = s.rtp.WriteToUDP(testRTP(s.c.payloadType(), 1, 0, voice), dst)
			select {
			case got := <-s.agent.input:
				if !bytes.Equal(got, voice) {
					t.Fatal("early media poisoned answered stream")
				}
			case <-time.After(time.Second):
				t.Fatal("missing answered audio")
			}
			s.cancel()
			result := s.finish(t, true)
			if result.stats.Early != 1 || result.stats.Received != 1 {
				t.Fatalf("counters: %+v", result.stats)
			}
			wav, err := os.ReadFile(filepath.Join(path, "ringback.wav"))
			if err != nil {
				t.Fatal(err)
			}
			wire := audio.CodecALaw
			if codec == "pcmu" {
				wire = audio.CodecMuLaw
			}
			if !bytes.Contains(wav[44:], wire.ToPCM16(tone)) {
				t.Fatal("carrier audio was not preserved")
			}
			if _, err := os.Stat(filepath.Join(path, "ringback.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEarlySDPMaintainsRestrictions(t *testing.T) {
	c := testConfig(t)
	body := strings.ReplaceAll(answerSDP(8, 31000), "sendrecv", "sendonly")
	if _, err := parseMediaAnswer([]byte(body), 8, c.MediaNetworks, true); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAnswer([]byte(body), 8, c.MediaNetworks); err == nil {
		t.Fatal("one-way final answer accepted")
	}
	for _, bad := range []string{strings.ReplaceAll(body, "127.0.0.1", "192.0.2.1"), strings.ReplaceAll(body, "PCMA/8000", "opus/48000"), strings.ReplaceAll(body, "sendonly", "recvonly")} {
		if _, err := parseMediaAnswer([]byte(bad), 8, c.MediaNetworks, true); err == nil {
			t.Fatal("unsafe early SDP accepted")
		}
	}
}
