package telephony

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func TestPCM16StreamingConversion(t *testing.T) {
	for _, codec := range []string{"pcma", "pcmu"} {
		t.Run(codec, func(t *testing.T) {
			wire := make([]byte, 480)
			for i := range wire {
				wire[i] = byte(i)
			}
			var whole, split pcmUpsampler
			want := whole.convert(wire, codec)
			var got []byte
			for _, part := range [][]byte{wire[:157], wire[157:160], wire[160:]} {
				got = append(got, split.convert(part, codec)...)
			}
			if !bytes.Equal(want, got) || len(got) != 4*len(wire) {
				t.Fatal("upsampling changed across packet boundaries or duration")
			}
			c := testConfig(t)
			c.Codec = codec
			c.GoliveAudioFormat = "pcm16"
			m, e := newMedia(c, nil, func(s string) { t.Fatal(s) })
			if e != nil {
				t.Fatal(e)
			}
			defer m.conn.Close()
			// Each pair has the same value, so the expected companding is independent
			// of packetization and does not rely on the converter implementation.
			pcm := make([]byte, 4*480)
			raw := make([]byte, 2*480)
			for i := 0; i < 480; i++ {
				v := uint16(int16(i*80 - 19000))
				binary.LittleEndian.PutUint16(raw[i*2:], v)
				binary.LittleEndian.PutUint16(pcm[i*4:], v)
				binary.LittleEndian.PutUint16(pcm[i*4+2:], v)
			}
			for i := range pcm {
				if e = m.enqueue("r", pcm[i:i+1], false); e != nil {
					t.Fatal(e)
				}
			}
			if e = m.enqueue("r", nil, true); e != nil {
				t.Fatal(e)
			}
			got = nil
			for _, f := range m.queue {
				got = append(got, f.data...)
			}
			if !bytes.Equal(got, phoneCodec(codec).FromPCM16(raw)) || len(m.pcmPending) != 0 {
				t.Fatal("fragmented PCM corrupted G.711 or duration")
			}
		})
	}
}

func TestPCM16CancellationAndTail(t *testing.T) {
	c := testConfig(t)
	c.GoliveAudioFormat = "pcm16"
	m, e := newMedia(c, nil, func(s string) { t.Fatal(s) })
	if e != nil {
		t.Fatal(e)
	}
	defer m.conn.Close()
	if e = m.enqueue("old", []byte{1, 2, 3}, false); e != nil {
		t.Fatal(e)
	}
	m.clear()
	if e = m.enqueue("old", []byte{4}, true); e != nil {
		t.Fatal("cancelled malformed tail must be ignored", e)
	}
	if len(m.queue) != 0 || len(m.pcmPending) != 0 {
		t.Fatal("cancelled partial audio survived")
	}
	if e = m.enqueue("odd", []byte{1}, true); e == nil {
		t.Fatal("accepted incomplete sample")
	}
	if e = m.enqueue("tail", []byte{0, 16}, true); e != nil {
		t.Fatal(e)
	}
	if len(m.queue) != 1 || m.queue[0].data[0] != phoneCodec(c.Codec).FromPCM16([]byte{0, 16})[0] {
		t.Fatal("single-sample tail lost")
	}
	for _, v := range m.queue[0].data[1:] {
		if v != c.silence() {
			t.Fatal("tail frame not padded with silence")
		}
	}
}

func TestRejectUnknownAgentFormat(t *testing.T) {
	c := testConfig(t)
	c.GoliveAudioFormat = "pcm32"
	if c.validate() == nil {
		t.Fatal("accepted unsupported format")
	}
}

func TestPCM16RequiresConfirmedRate(t *testing.T) {
	fake := newFakeAgent(t, "pcm16")
	c := testConfig(t)
	c.GoliveAudioFormat = "pcm16"
	c.GoliveURL = fake.server.URL
	ws, err := connectAgent(context.Background(), c, Call{ID: "rate-check"})
	if ws != nil {
		ws.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "session setup failed") {
		t.Fatalf("unconfirmed rate accepted: %v", err)
	}
}
