package speko

import (
	"encoding/binary"

	"github.com/chuanmingliu/golive/internal/audio"
)

const upstreamRate = 24000

// pcmTransport keeps the SIP side at 8 kHz G.711 while Speko uses 24 kHz
// PCM16. Conversion is streaming: incomplete downsampling groups survive
// WebSocket boundaries, and no extra audio frame is buffered.
type pcmTransport struct {
	codec        audio.Codec
	pending      []byte
	previous     int32
	havePrevious bool
}

func newPCMTransport(format string) *pcmTransport {
	codec := audio.CodecALaw
	if format == "g711_ulaw" {
		codec = audio.CodecMuLaw
	}
	return &pcmTransport{codec: codec}
}

// Causal linear interpolation adds one 8 kHz sample of delay (0.125 ms).
func (p *pcmTransport) encode(wire []byte) []byte {
	pcm := p.codec.ToPCM16(wire)
	out := make([]byte, len(wire)*6)
	for i := range wire {
		current := int32(int16(binary.LittleEndian.Uint16(pcm[i*2:])))
		if !p.havePrevious {
			p.previous = current
			p.havePrevious = true
		}
		for j := 0; j < 3; j++ {
			value := p.previous + (current-p.previous)*int32(j)/3
			binary.LittleEndian.PutUint16(out[i*6+j*2:], uint16(int16(value)))
		}
		p.previous = current
	}
	return out
}

// A three-sample box filter attenuates high frequencies before decimation.
// It is intentionally modest; this path carries narrowband telephone speech.
func (p *pcmTransport) decode(pcm []byte) []byte {
	data := append(p.pending, pcm...)
	n := len(data) / 6
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		var sum int32
		for j := 0; j < 3; j++ {
			sum += int32(int16(binary.LittleEndian.Uint16(data[i*6+j*2:])))
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(sum/3)))
	}
	p.pending = append([]byte(nil), data[n*6:]...)
	return p.codec.FromPCM16(out)
}
