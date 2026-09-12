package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// WAV read/write for 16-bit PCM only. golive never needs more: every provider
// and every transport in the stack is PCM16 mono, and a general WAV library
// would be a dependency carried for one test harness.

// WriteWAV writes mono PCM16 samples as a RIFF/WAVE file.
func WriteWAV(path string, pcm []byte, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteWAVTo(f, pcm, rate, 1)
}

// WriteWAVStereo writes two channels interleaved, which is how a session
// recording is stored: input on the left, output on the right, exactly like
// gpt-live-1's stored-session download.
func WriteWAVStereo(path string, left, right []byte, rate int) error {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	n -= n % BytesPerSample

	interleaved := make([]byte, 0, n*2)
	for i := 0; i < n; i += BytesPerSample {
		interleaved = append(interleaved, sampleAt(left, i)...)
		interleaved = append(interleaved, sampleAt(right, i)...)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteWAVTo(f, interleaved, rate, 2)
}

func sampleAt(buf []byte, i int) []byte {
	if i+BytesPerSample <= len(buf) {
		return buf[i : i+BytesPerSample]
	}
	return []byte{0, 0}
}

// WriteWAVTo writes a WAV header and PCM16 body to w.
func WriteWAVTo(w io.Writer, pcm []byte, rate, channels int) error {
	if channels <= 0 {
		channels = 1
	}
	byteRate := rate * channels * BytesPerSample
	blockAlign := channels * BytesPerSample

	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(pcm)))
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16) // PCM fmt chunk size
	binary.LittleEndian.PutUint16(header[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(header[22:], uint16(channels))
	binary.LittleEndian.PutUint32(header[24:], uint32(rate))
	binary.LittleEndian.PutUint32(header[28:], uint32(byteRate))
	binary.LittleEndian.PutUint16(header[32:], uint16(blockAlign))
	binary.LittleEndian.PutUint16(header[34:], 16) // bits per sample
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))

	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(pcm)
	return err
}

// ReadWAV reads a 16-bit PCM WAV file, returning the samples and sample rate.
// Multi-channel input is downmixed to mono, since the pipeline is mono
// throughout and silently taking only the left channel loses half the energy.
func ReadWAV(path string) ([]byte, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("audio: %s is not a RIFF/WAVE file", path)
	}

	var (
		rate     int
		channels int
		bits     int
		body     []byte
	)

	// Walk the chunk list rather than assuming a 44-byte header: real files
	// carry LIST/fact chunks before the data.
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		payload := pos + 8
		if payload+size > len(data) {
			size = len(data) - payload
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, fmt.Errorf("audio: %s has a truncated fmt chunk", path)
			}
			format := binary.LittleEndian.Uint16(data[payload:])
			channels = int(binary.LittleEndian.Uint16(data[payload+2:]))
			rate = int(binary.LittleEndian.Uint32(data[payload+4:]))
			bits = int(binary.LittleEndian.Uint16(data[payload+14:]))
			if format != 1 {
				return nil, 0, fmt.Errorf("audio: %s is WAVE format %d; only PCM (1) is supported", path, format)
			}
		case "data":
			body = data[payload : payload+size]
		}
		pos = payload + size
		if size%2 == 1 {
			pos++ // chunks are word-aligned
		}
	}

	if body == nil || rate == 0 {
		return nil, 0, fmt.Errorf("audio: %s has no usable PCM data", path)
	}
	if bits != 16 {
		return nil, 0, fmt.Errorf("audio: %s is %d-bit; only 16-bit PCM is supported", path, bits)
	}
	if channels > 1 {
		body = downmix(body, channels)
	}
	return body, rate, nil
}

func downmix(pcm []byte, channels int) []byte {
	frame := channels * BytesPerSample
	out := make([]byte, 0, len(pcm)/channels)
	for i := 0; i+frame <= len(pcm); i += frame {
		var sum int
		for c := 0; c < channels; c++ {
			sum += int(int16(binary.LittleEndian.Uint16(pcm[i+c*BytesPerSample:])))
		}
		var buf [2]byte
		binary.LittleEndian.PutUint16(buf[:], uint16(int16(sum/channels)))
		out = append(out, buf[:]...)
	}
	return out
}
