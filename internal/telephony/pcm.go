package telephony

import (
	"encoding/binary"
	"errors"

	"github.com/chuanmingliu/golive/internal/audio"
)

func (c Config) agentFormat() string {
	if c.GoliveAudioFormat == "pcm16" {
		return "pcm16"
	}
	return c.wireFormat()
}

func phoneCodec(name string) audio.Codec {
	if name == "pcmu" {
		return audio.CodecMuLaw
	}
	return audio.CodecALaw
}

// pcmUpsampler preserves the interpolation history across RTP packets. It
// emits exactly 20 ms of PCM for 20 ms of G.711, without a batching delay.
type pcmUpsampler struct {
	last int16
	have bool
}

func (u *pcmUpsampler) convert(wire []byte, codec string) []byte {
	pcm := phoneCodec(codec).ToPCM16(wire)
	out := make([]byte, len(pcm)*2)
	for i := 0; i < len(pcm); i += 2 {
		sample := int16(binary.LittleEndian.Uint16(pcm[i:]))
		if !u.have {
			u.last = sample
			u.have = true
		}
		midpoint := int16((int32(u.last) + int32(sample)) / 2)
		binary.LittleEndian.PutUint16(out[2*i:], uint16(midpoint))
		binary.LittleEndian.PutUint16(out[2*i+2:], uint16(sample))
		u.last = sample
	}
	return out
}

// convertAgentPCM runs under the media mutex. A response can split a PCM
// sample or sample pair across WebSocket deltas; carry at most three bytes.
// Averaging each pair low-pass filters the 16-to-8 kHz conversion. No state
// crosses response identities or survives cancellation.
func (m *media) convertAgentPCM(id string, data []byte, done bool) ([]byte, error) {
	if _, ok := m.pcmPending[id]; !ok && len(m.pcmPending) >= 4096 {
		return nil, errors.New("too many partial PCM responses")
	}
	b := append(m.pcmPending[id], data...)
	if done && len(b)%2 != 0 {
		return nil, errors.New("incomplete PCM16 sample")
	}
	if done && len(b)%4 == 2 {
		b = append(b, b[len(b)-2:]...)
	}
	n := len(b) / 4 * 4
	pcm := make([]byte, n/2)
	for i := 0; i < n; i += 4 {
		a := int16(binary.LittleEndian.Uint16(b[i:]))
		c := int16(binary.LittleEndian.Uint16(b[i+2:]))
		binary.LittleEndian.PutUint16(pcm[i/2:], uint16(int16((int32(a)+int32(c))/2)))
	}
	if n < len(b) {
		m.pcmPending[id] = append([]byte(nil), b[n:]...)
	} else {
		delete(m.pcmPending, id)
	}
	codec := audio.CodecALaw
	if m.pt == 0 {
		codec = audio.CodecMuLaw
	}
	return codec.FromPCM16(pcm), nil
}
