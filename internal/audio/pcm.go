// Package audio holds the sample-level primitives the duplex engine needs:
// PCM16 <-> float32 conversion, rate conversion, fixed-size framing and the
// energy measurements the VAD is built on.
//
// Everything here is allocation-conscious and lock-free; it sits on the hot
// path of both the listen and the speak channel, which in a full-duplex
// session are live at the same time.
package audio

import (
	"encoding/binary"
	"errors"
	"math"
)

// Format describes a linear PCM stream. golive only ever speaks 16-bit
// little-endian mono internally; Rate is the only axis that varies.
type Format struct {
	Rate     int // samples per second
	Channels int // always 1 internally
}

// PCM16 at the given rate.
func PCM16(rate int) Format { return Format{Rate: rate, Channels: 1} }

// Common rates.
const (
	RateTelephony = 8000
	RatePipeline  = 16000 // Tencent ASR + MiniMax TTS both work at 16 kHz
	RateWideband  = 24000 // gpt-live-1 default client format
)

// BytesPerSample for PCM16.
const BytesPerSample = 2

var errOddBytes = errors.New("audio: pcm16 buffer has an odd byte count")

// DurationMS returns the wall-clock duration of a PCM16 byte buffer.
func (f Format) DurationMS(pcm []byte) float64 {
	if f.Rate <= 0 {
		return 0
	}
	samples := float64(len(pcm)) / float64(BytesPerSample)
	return samples / float64(f.Rate) * 1000
}

// BytesForMS returns the PCM16 byte count that holds ms milliseconds, rounded
// down to a whole sample.
func (f Format) BytesForMS(ms int) int {
	samples := f.Rate * ms / 1000
	return samples * BytesPerSample
}

// DecodePCM16 converts little-endian PCM16 bytes into normalized float32
// samples in [-1, 1).
func DecodePCM16(pcm []byte) ([]float32, error) {
	if len(pcm)%BytesPerSample != 0 {
		return nil, errOddBytes
	}
	out := make([]float32, len(pcm)/BytesPerSample)
	for i := range out {
		v := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		out[i] = float32(v) / 32768
	}
	return out, nil
}

// EncodePCM16 converts normalized float32 samples into little-endian PCM16,
// clipping rather than wrapping on overflow and dropping non-finite samples to
// silence. A NaN that reaches a provider shows up as a click, so it is cheaper
// to scrub here.
func EncodePCM16(samples []float32) []byte {
	out := make([]byte, len(samples)*BytesPerSample)
	for i, s := range samples {
		v := float64(s)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			v = 0
		}
		v *= 32768
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(v)))
	}
	return out
}

// Resample converts between sample rates with linear interpolation.
//
// Linear interpolation is not a great anti-alias filter, but every rate pair we
// actually use (8k/16k/24k/48k) is a simple ratio of speech-band audio, and the
// alternative is a windowed-sinc bank whose cost lands squarely on the duplex
// hot path. If you feed golive music, replace this.
func Resample(in []float32, fromRate, toRate int) []float32 {
	if fromRate == toRate || len(in) == 0 {
		return in
	}
	ratio := float64(toRate) / float64(fromRate)
	n := int(float64(len(in)) * ratio)
	if n <= 0 {
		return nil
	}
	out := make([]float32, n)
	for i := range out {
		src := float64(i) / ratio
		lo := int(src)
		if lo >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		frac := float32(src - float64(lo))
		out[i] = in[lo]*(1-frac) + in[lo+1]*frac
	}
	return out
}

// ResamplePCM16 is Resample over raw PCM16 bytes.
func ResamplePCM16(pcm []byte, fromRate, toRate int) ([]byte, error) {
	if fromRate == toRate {
		return pcm, nil
	}
	samples, err := DecodePCM16(pcm)
	if err != nil {
		return nil, err
	}
	return EncodePCM16(Resample(samples, fromRate, toRate)), nil
}

// RMS returns the root-mean-square level of a frame, the quantity the VAD
// thresholds against.
func RMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// ZeroCrossingRate discriminates voiced speech (low ZCR, high energy) from
// fricatives and wideband noise (high ZCR). The VAD uses it to reject hiss that
// clears the energy threshold.
func ZeroCrossingRate(samples []float32) float64 {
	if len(samples) < 2 {
		return 0
	}
	crossings := 0
	for i := 1; i < len(samples); i++ {
		if (samples[i-1] >= 0) != (samples[i] >= 0) {
			crossings++
		}
	}
	return float64(crossings) / float64(len(samples)-1)
}

// DBFS expresses an RMS level in dB relative to full scale.
func DBFS(rms float64) float64 {
	if rms <= 1e-9 {
		return -180
	}
	return 20 * math.Log10(rms)
}

// Framer slices a byte stream that arrives in arbitrary chunk sizes into
// fixed-size frames. Clients send whatever their capture buffer produces;
// the VAD and the ASR adapters both want a stable frame size.
//
// Framer is not safe for concurrent use; it lives on one goroutine.
type Framer struct {
	size int
	buf  []byte
}

// NewFramer builds a framer emitting frames of exactly size bytes.
func NewFramer(size int) *Framer {
	return &Framer{size: size, buf: make([]byte, 0, size*4)}
}

// Push appends data and returns every whole frame that is now available. The
// returned slices alias a fresh copy, so callers may retain them.
func (f *Framer) Push(data []byte) [][]byte {
	f.buf = append(f.buf, data...)
	var frames [][]byte
	for len(f.buf) >= f.size {
		frame := make([]byte, f.size)
		copy(frame, f.buf[:f.size])
		frames = append(frames, frame)
		f.buf = f.buf[f.size:]
	}
	return frames
}

// Flush returns any partial frame, zero-padded to the frame size, and resets.
func (f *Framer) Flush() []byte {
	if len(f.buf) == 0 {
		return nil
	}
	frame := make([]byte, f.size)
	copy(frame, f.buf)
	f.buf = f.buf[:0]
	return frame
}

// Buffered reports how many bytes are held back in a partial frame.
func (f *Framer) Buffered() int { return len(f.buf) }
