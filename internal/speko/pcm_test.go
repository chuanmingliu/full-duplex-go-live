package speko

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSIPUpstreamUsesPCM(t *testing.T) {
	for _, format := range []string{"g711_alaw", "g711_ulaw"} {
		settings, err := parseRealtimeStart(wireEvent{Type: "session.update", Session: json.RawMessage(`{"input_audio_format":"` + format + `","output_audio_format":"` + format + `"}`)})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(settings.start("gpt-6-luna", "Hello."))
		var event struct {
			Session struct {
				Audio struct {
					Format struct {
						Type string
						Rate int
					}
				}
			}
		}
		if err = json.Unmarshal(b, &event); err != nil {
			t.Fatal(err)
		}
		if event.Session.Audio.Format.Type != "audio/pcm" || event.Session.Audio.Format.Rate != 24000 {
			t.Fatalf("Speko rejects G.711 upstream: %s", b)
		}
	}
}

func TestPCMConversionPreservesClockAcrossChunks(t *testing.T) {
	for _, format := range []string{"g711_alaw", "g711_ulaw"} {
		wire := make([]byte, 160)
		for i := range wire {
			wire[i] = byte(i)
		}
		whole := newPCMTransport(format).encode(wire)
		enc := newPCMTransport(format)
		var split []byte
		for _, b := range wire {
			split = append(split, enc.encode([]byte{b})...)
		}
		if !bytes.Equal(whole, split) || len(whole) != 960 {
			t.Fatal("input clock depends on packet boundaries")
		}
		expected := newPCMTransport(format).decode(whole)
		dec := newPCMTransport(format)
		var got []byte
		for _, b := range whole {
			got = append(got, dec.decode([]byte{b})...)
		}
		if !bytes.Equal(got, expected) || len(got) != 160 || len(dec.pending) != 0 {
			t.Fatal("output clock depends on packet boundaries")
		}
	}
}
