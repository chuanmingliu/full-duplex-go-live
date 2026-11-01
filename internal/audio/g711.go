package audio

import "fmt"

// G.711 companding, the audio a phone line speaks.
//
// gpt-live-1 accepts audio/pcmu and audio/pcma at 8 kHz on a WebSocket session
// so that a telephony bridge can hand over what it already has, one byte per
// sample, with no transcoding of its own. golive is linear PCM16 everywhere
// inside, so both directions convert at the session boundary and nothing past
// it knows a call was companded.
//
// The decode tables are built once at start-up from the reference expansion in
// ITU-T G.711; encoding is the segment search from the same recommendation.
// Neither allocates beyond its output.

// Codec names a wire encoding for session audio.
type Codec int

const (
	// CodecPCM16 is signed 16-bit little-endian linear PCM, two bytes a sample.
	CodecPCM16 Codec = iota
	// CodecMuLaw is G.711 μ-law (audio/pcmu), one byte a sample.
	CodecMuLaw
	// CodecALaw is G.711 A-law (audio/pcma), one byte a sample.
	CodecALaw
)

// MIME types as gpt-live-1 spells them in session.audio.format.type.
const (
	MIMEPCM  = "audio/pcm"
	MIMEPCMU = "audio/pcmu"
	MIMEPCMA = "audio/pcma"
)

// CodecForMIME maps a session audio type to its codec.
func CodecForMIME(mime string) (Codec, error) {
	switch mime {
	case "", MIMEPCM:
		return CodecPCM16, nil
	case MIMEPCMU:
		return CodecMuLaw, nil
	case MIMEPCMA:
		return CodecALaw, nil
	default:
		return CodecPCM16, fmt.Errorf("audio: unsupported format type %q; use %s, %s or %s",
			mime, MIMEPCM, MIMEPCMU, MIMEPCMA)
	}
}

// String is the codec's MIME type.
func (c Codec) String() string {
	switch c {
	case CodecMuLaw:
		return MIMEPCMU
	case CodecALaw:
		return MIMEPCMA
	default:
		return MIMEPCM
	}
}

// BytesPerSample on the wire.
func (c Codec) BytesPerSample() int {
	if c == CodecPCM16 {
		return BytesPerSample
	}
	return 1
}

// ToPCM16 converts wire audio to linear PCM16. PCM16 input is returned as is.
func (c Codec) ToPCM16(wire []byte) []byte {
	switch c {
	case CodecMuLaw:
		return expand(wire, &muLawToLinear)
	case CodecALaw:
		return expand(wire, &aLawToLinear)
	default:
		return wire
	}
}

// FromPCM16 converts linear PCM16 to wire audio. PCM16 output is returned as
// is; an odd trailing byte, which is half a sample, is dropped.
func (c Codec) FromPCM16(pcm []byte) []byte {
	switch c {
	case CodecMuLaw:
		return compress(pcm, linearToMuLaw)
	case CodecALaw:
		return compress(pcm, linearToALaw)
	default:
		return pcm
	}
}

var (
	muLawToLinear [256]int16
	aLawToLinear  [256]int16
)

func init() {
	for i := 0; i < 256; i++ {
		muLawToLinear[i] = decodeMuLaw(byte(i))
		aLawToLinear[i] = decodeALaw(byte(i))
	}
}

func expand(wire []byte, table *[256]int16) []byte {
	out := make([]byte, len(wire)*2)
	for i, b := range wire {
		v := uint16(table[b])
		out[i*2] = byte(v)
		out[i*2+1] = byte(v >> 8)
	}
	return out
}

func compress(pcm []byte, enc func(int16) byte) []byte {
	n := len(pcm) / 2
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = enc(int16(uint16(pcm[i*2]) | uint16(pcm[i*2+1])<<8))
	}
	return out
}

const (
	muBias = 0x84
	muClip = 32635
)

func linearToMuLaw(sample int16) byte {
	s := int(sample)
	sign := 0
	if s < 0 {
		s = -s
		sign = 0x80
	}
	if s > muClip {
		s = muClip
	}
	s += muBias

	exponent := 7
	for mask := 0x4000; exponent > 0 && s&mask == 0; mask >>= 1 {
		exponent--
	}
	mantissa := (s >> (exponent + 3)) & 0x0F
	return ^byte(sign | exponent<<4 | mantissa)
}

func decodeMuLaw(b byte) int16 {
	b = ^b
	sign := b & 0x80
	exponent := int(b>>4) & 0x07
	mantissa := int(b & 0x0F)
	s := ((mantissa << 3) + muBias) << exponent
	s -= muBias
	if sign != 0 {
		return int16(-s)
	}
	return int16(s)
}

func linearToALaw(sample int16) byte {
	s := int(sample)
	sign := 0x80
	if s < 0 {
		// A-law is defined on the magnitude of a 13-bit value; −1 keeps the
		// two halves symmetric around zero the way the reference coder does.
		s = -s - 1
		sign = 0
	}
	if s > 32767 {
		s = 32767
	}

	var compressed int
	if s >= 256 {
		exponent := 7
		for mask := 0x4000; exponent > 1 && s&mask == 0; mask >>= 1 {
			exponent--
		}
		compressed = exponent<<4 | (s>>(exponent+3))&0x0F
	} else {
		compressed = s >> 4
	}
	return byte(compressed|sign) ^ 0x55
}

func decodeALaw(b byte) int16 {
	b ^= 0x55
	sign := b & 0x80
	exponent := int(b>>4) & 0x07
	mantissa := int(b & 0x0F)

	var s int
	if exponent == 0 {
		s = mantissa<<4 + 8
	} else {
		s = (mantissa<<4 + 0x108) << (exponent - 1)
	}
	if sign == 0 {
		return int16(-s)
	}
	return int16(s)
}
