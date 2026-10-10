package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

// Reference points from ITU-T G.711: the codes for silence and for the largest
// magnitudes, in both laws. A coder that gets these right has its sign, bias
// and inversion conventions right, which is where every hand-written G.711
// implementation goes wrong.
func TestG711ReferenceCodes(t *testing.T) {
	cases := []struct {
		name   string
		enc    func(int16) byte
		sample int16
		want   byte
	}{
		{"mu-law zero", linearToMuLaw, 0, 0xFF},
		{"mu-law +max", linearToMuLaw, 32767, 0x80},
		{"mu-law -max", linearToMuLaw, -32768, 0x00},
		{"a-law zero", linearToALaw, 0, 0xD5},
		{"a-law +max", linearToALaw, 32767, 0xAA},
		{"a-law -max", linearToALaw, -32768, 0x2A},
	}
	for _, c := range cases {
		if got := c.enc(c.sample); got != c.want {
			t.Errorf("%s: encoded %d as %#02x, want %#02x", c.name, c.sample, got, c.want)
		}
	}
	if got := muLawToLinear[0xFF]; got != 0 {
		t.Errorf("mu-law 0xFF decodes to %d, want 0", got)
	}
	if got := aLawToLinear[0xD5]; got != 8 {
		t.Errorf("a-law 0xD5 decodes to %d, want 8", got)
	}
}

// Companding is lossy by design, so the test is on the size of the loss: a
// decoded sample must land in the same quantization step it was encoded from,
// and re-encoding it must give the same code back.
func TestG711RoundTripStaysWithinOneStep(t *testing.T) {
	for _, codec := range []Codec{CodecMuLaw, CodecALaw} {
		for s := -32768; s <= 32767; s += 7 {
			pcm := make([]byte, 2)
			binary.LittleEndian.PutUint16(pcm, uint16(int16(s)))

			wire := codec.FromPCM16(pcm)
			if len(wire) != 1 {
				t.Fatalf("%v: one sample encoded to %d bytes", codec, len(wire))
			}
			back := int(int16(binary.LittleEndian.Uint16(codec.ToPCM16(wire))))

			// The step doubles each segment; one sixteenth of the magnitude
			// plus the smallest step bounds the error in every segment.
			tolerance := int(math.Abs(float64(s)))/16 + 36
			if diff := back - s; diff > tolerance || diff < -tolerance {
				t.Fatalf("%v: %d decoded as %d, off by %d (tolerance %d)", codec, s, back, diff, tolerance)
			}

			again := codec.FromPCM16(codec.ToPCM16(wire))
			// μ-law has two zeros, 0x7F and 0xFF, and both expand to 0: the
			// negative one cannot come back from a linear sample that has no
			// sign left. It is the one code that is allowed to move.
			if codec == CodecMuLaw && wire[0] == 0x7F && again[0] == 0xFF {
				continue
			}
			if again[0] != wire[0] {
				t.Fatalf("%v: code %#02x did not survive a decode and re-encode (got %#02x)",
					codec, wire[0], again[0])
			}
		}
	}
}

func TestCodecForMIME(t *testing.T) {
	for mime, want := range map[string]Codec{
		"":           CodecPCM16,
		"audio/pcm":  CodecPCM16,
		"audio/pcmu": CodecMuLaw,
		"audio/pcma": CodecALaw,
	} {
		got, err := CodecForMIME(mime)
		if err != nil || got != want {
			t.Errorf("CodecForMIME(%q) = %v, %v; want %v", mime, got, err, want)
		}
	}
	if _, err := CodecForMIME("audio/opus"); err == nil {
		t.Error("an unsupported type was accepted")
	}
	if CodecMuLaw.BytesPerSample() != 1 || CodecPCM16.BytesPerSample() != 2 {
		t.Error("wire sample sizes are wrong")
	}
}

// A voiced signal must still read as voiced after a trip through the phone
// line's codec, or a telephony session would never open an utterance.
func TestG711KeepsSpeechAboveTheVADThreshold(t *testing.T) {
	const rate = RateTelephony
	speech := EncodePCM16(tone(rate, 400, 0.45))
	for _, codec := range []Codec{CodecMuLaw, CodecALaw} {
		back, err := DecodePCM16(codec.ToPCM16(codec.FromPCM16(speech)))
		if err != nil {
			t.Fatal(err)
		}
		orig, _ := DecodePCM16(speech)
		if got, want := DBFS(RMS(back)), DBFS(RMS(orig)); math.Abs(got-want) > 0.5 {
			t.Errorf("%v: level moved from %.1f to %.1f dBFS", codec, want, got)
		}
	}
}
