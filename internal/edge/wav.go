package edge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/chuanmingliu/golive/internal/audio"
)

// Clip is decoded audio: mono float samples at a rate.
type Clip struct {
	Samples []float32
	Rate    int
}

// Seconds is how long the clip plays.
func (c Clip) Seconds() float64 {
	if c.Rate == 0 {
		return 0
	}
	return float64(len(c.Samples)) / float64(c.Rate)
}

// Resampled is the clip at another rate, mono. Linear interpolation, as the
// rest of golive does for speech.
func (c Clip) Resampled(rate int) Clip {
	if rate == c.Rate {
		return c
	}
	return Clip{Samples: audio.Resample(c.Samples, c.Rate, rate), Rate: rate}
}

// PCM16 is the clip as little-endian 16-bit samples.
func (c Clip) PCM16() []byte { return audio.EncodePCM16(c.Samples) }

// WAV is the clip as a RIFF/WAVE file of 16-bit PCM.
func (c Clip) WAV() []byte {
	pcm := c.PCM16()
	out := make([]byte, 0, 44+len(pcm))
	out = append(out, WAVHeader(c.Rate, len(pcm))...)
	return append(out, pcm...)
}

// WAVHeader is a 44-byte header for mono 16-bit PCM at rate holding dataBytes
// of samples. A dataBytes under zero makes a header for a stream whose length
// is not known: the sizes are left at their maximum, which every player reads
// to the end of the file.
func WAVHeader(rate, dataBytes int) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	size := uint32(0xFFFFFFFF)
	data := uint32(0xFFFFFFFF)
	if dataBytes >= 0 {
		size = uint32(36 + dataBytes)
		data = uint32(dataBytes)
	}
	binary.LittleEndian.PutUint32(h[4:], size)
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 1) // mono
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], data)
	return h
}

// DecodeWAV reads a RIFF/WAVE file: PCM at 8, 16, 24 or 32 bits or IEEE float
// at 32, any channel count (mixed down to mono), any rate.
func DecodeWAV(data []byte) (Clip, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return Clip{}, errors.New("not a RIFF/WAVE file")
	}
	var (
		format, channels, bits int
		rate                   int
		haveFmt                bool
	)
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		end := body + size
		if end > len(data) || size < 0 {
			end = len(data) // a stream header's size is its maximum; read to the end
		}
		switch id {
		case "fmt ":
			if end-body < 16 {
				return Clip{}, errors.New("wav: fmt chunk too short")
			}
			format = int(binary.LittleEndian.Uint16(data[body:]))
			channels = int(binary.LittleEndian.Uint16(data[body+2:]))
			rate = int(binary.LittleEndian.Uint32(data[body+4:]))
			bits = int(binary.LittleEndian.Uint16(data[body+14:]))
			if format == 0xFFFE && end-body >= 26 { // WAVE_FORMAT_EXTENSIBLE: the real format is in the GUID
				format = int(binary.LittleEndian.Uint16(data[body+24:]))
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return Clip{}, errors.New("wav: data before fmt")
			}
			if channels <= 0 || rate <= 0 {
				return Clip{}, fmt.Errorf("wav: %d channels at %d Hz", channels, rate)
			}
			samples, err := decodeSamples(data[body:end], format, bits, channels)
			if err != nil {
				return Clip{}, err
			}
			return Clip{Samples: samples, Rate: rate}, nil
		}
		pos = body + size + size&1
	}
	return Clip{}, errors.New("wav: no data chunk")
}

func decodeSamples(raw []byte, format, bits, channels int) ([]float32, error) {
	var width int
	switch {
	case format == 1 && bits == 16:
		width = 2
	case format == 1 && bits == 8:
		width = 1
	case format == 1 && bits == 24:
		width = 3
	case format == 1 && bits == 32:
		width = 4
	case format == 3 && bits == 32:
		width = 4
	default:
		return nil, fmt.Errorf("wav: format %d at %d bits is not supported (PCM 8/16/24/32 and float 32 are)", format, bits)
	}
	frame := width * channels
	n := len(raw) / frame
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		var sum float32
		for ch := 0; ch < channels; ch++ {
			p := raw[i*frame+ch*width:]
			var v float32
			switch {
			case format == 3:
				v = math.Float32frombits(binary.LittleEndian.Uint32(p))
			case width == 2:
				v = float32(int16(binary.LittleEndian.Uint16(p))) / 32768
			case width == 1:
				v = (float32(p[0]) - 128) / 128
			case width == 3:
				v = float32(int32(uint32(p[0])<<8|uint32(p[1])<<16|uint32(p[2])<<24)>>8) / 8388608
			case width == 4:
				v = float32(int32(binary.LittleEndian.Uint32(p))) / 2147483648
			}
			sum += v
		}
		out[i] = sum / float32(channels)
	}
	return out, nil
}

// DecodePCM reads raw little-endian 16-bit mono samples.
func DecodePCM(data []byte, rate int) (Clip, error) {
	if rate <= 0 {
		return Clip{}, errors.New("pcm: a sample rate is needed")
	}
	samples, err := audio.DecodePCM16(data)
	if err != nil {
		return Clip{}, err
	}
	return Clip{Samples: samples, Rate: rate}, nil
}

// readAll reads a body up to limit bytes and says when it was longer.
func readAll(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := buf.ReadFrom(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("the audio is over the %d MB limit", limit>>20)
	}
	return buf.Bytes(), nil
}
