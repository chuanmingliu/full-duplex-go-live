package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
)

// Against a real recognizer the keyed tones are noise. -speak has the
// service say each phrase of the vocabulary in its own voice and plays those
// clips back as the caller, as golivebench -speak does; -clips-dir keeps them
// as WAV so a later run can reuse them (and so they can be listened to).
//
// clips, once filled, replaces toneFor: index k is phrase k's speech at the
// session rate.
var clips [][]byte

// voiceFor is what the caller sends for phrase k: a clip when one was
// spoken, the keyed tone otherwise. ms is honoured for the tone (a murmur
// is held for as long as the scenario wants) and ignored for a clip, whose
// length is its own.
func voiceFor(k, ms, rate int) []byte {
	if k < len(clips) && len(clips[k]) > 0 {
		return clips[k]
	}
	return toneFor(k, ms, rate)
}

// loadClips reads spoken_NN.wav from dir, resampled to rate, for as many
// phrases as it has.
func loadClips(dir string, rate int) (int, error) {
	clips = make([][]byte, len(phrases))
	n := 0
	for k := range phrases {
		path := filepath.Join(dir, fmt.Sprintf("spoken_%02d.wav", k))
		pcm, r, err := audio.ReadWAV(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return n, fmt.Errorf("%s: %w", path, err)
		}
		if r != rate {
			if pcm, err = audio.ResamplePCM16(pcm, r, rate); err != nil {
				return n, fmt.Errorf("%s: %w", path, err)
			}
		}
		clips[k] = pcm
		n++
	}
	return n, nil
}

// speakClips has the service say every phrase that has no clip yet.
func speakClips(ctx context.Context, url, token string, rate int, dir string) error {
	if clips == nil {
		clips = make([][]byte, len(phrases))
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for k, text := range phrases {
		if len(clips[k]) > 0 {
			continue
		}
		pcm, err := speakOne(ctx, url, token, text, rate)
		if err != nil {
			return fmt.Errorf("could not speak %q: %w", text, err)
		}
		pcm = trimSilence(pcm, rate)
		if ms := audio.PCM16(rate).DurationMS(pcm); ms < 150 {
			return fmt.Errorf("could not speak %q: only %.0f ms of audio came back", text, ms)
		}
		clips[k] = pcm
		fmt.Fprintf(os.Stderr, "· spoke %q: %.0f ms\n", text, audio.PCM16(rate).DurationMS(pcm))
		if dir != "" {
			if err := audio.WriteWAV(filepath.Join(dir, fmt.Sprintf("spoken_%02d.wav", k)), pcm, rate); err != nil {
				return err
			}
		}
		// A synthesis account allows about 20 requests a minute; pace them.
		time.Sleep(2 * time.Second)
	}
	return nil
}

func speakOne(ctx context.Context, url, token, sentence string, rate int) ([]byte, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	start, _ := json.Marshal(live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "speak"},
		Session: live.SessionConfig{
			Audio:      &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: rate}},
			Delegation: &live.DelegationConfig{Type: live.DelegationResponses},
			Golive:     &live.GoliveConfig{Greeting: &sentence},
		},
	})
	if err := conn.WriteMessage(websocket.TextMessage, start); err != nil {
		return nil, err
	}
	var pcm []byte
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if len(pcm) > 0 {
				return pcm, nil
			}
			return nil, fmt.Errorf("no audio: %w", err)
		}
		var ev struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		switch ev.Type {
		case live.ServerOutputAudioDelta:
			chunk, err := base64.StdEncoding.DecodeString(ev.Delta)
			if err != nil {
				continue
			}
			pcm = append(pcm, chunk...)
			_ = conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
		case live.ServerError:
			if len(pcm) == 0 {
				return nil, fmt.Errorf("the service refused: %s", trim(string(data), 200))
			}
		}
	}
}

// trimSilence cuts a clip down to its words, leaving a few frames either side.
func trimSilence(pcm []byte, rate int) []byte {
	const floor = 350
	frame := audio.PCM16(rate).BytesForMS(10)
	if frame <= 0 || len(pcm) < frame {
		return pcm
	}
	loud := func(off int) bool {
		for i := off; i+1 < off+frame && i+1 < len(pcm); i += 2 {
			v := int(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
			if v > floor || v < -floor {
				return true
			}
		}
		return false
	}
	first, last := -1, -1
	for off := 0; off+frame <= len(pcm); off += frame {
		if loud(off) {
			if first < 0 {
				first = off
			}
			last = off
		}
	}
	if first < 0 {
		return nil
	}
	start := first - 4*frame
	if start < 0 {
		start = 0
	}
	end := last + 2*frame
	if end > len(pcm) {
		end = len(pcm)
	}
	return pcm[start:end]
}
