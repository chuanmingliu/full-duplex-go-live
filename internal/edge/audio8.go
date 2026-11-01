package edge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Audio8ASR is the Audio8-ASR-0.1B ONNX runtime's HTTP service
// (Edge0/Audio8-ASR-0.1B-onnx-runtime: server.py, POST /asr).
//
// It takes a whole clip and answers when it has read all of it: 16 kHz mono,
// up to about 30 s, no partial results. Hot words are a space-separated list.
type Audio8ASR struct {
	Base string
	HTTP *http.Client
}

// NewAudio8ASR serves the runtime at base (http://127.0.0.1:7860).
func NewAudio8ASR(base string) *Audio8ASR {
	return &Audio8ASR{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 120 * time.Second}}
}

// Model is the model entry for the runtime.
func (a *Audio8ASR) Model(id string) *Model {
	return &Model{ID: id, Kind: Transcription, Family: "audio8-asr", Upstream: a.Base, Rate: 16000,
		Licence:     "CC BY-NC 4.0 (non-commercial)",
		Note:        "whole-clip recognition: the text comes when the clip has been read, no partials; clips up to about 30 s",
		transcriber: a, health: a.Health}
}

// Health asks the service's /health.
func (a *Audio8ASR) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.Base+"/health", nil)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}

type audio8ASRResult struct {
	Text      string  `json:"text"`
	Elapsed   float64 `json:"elapsed_seconds"`
	Generated int     `json:"generated_tokens"`
	Detail    any     `json:"detail"`
}

// Transcribe posts the clip as a 16 kHz WAV.
func (a *Audio8ASR) Transcribe(ctx context.Context, clip Clip, req TranscribeRequest) (TranscribeResult, error) {
	clip = clip.Resampled(16000)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("audio", "clip.wav")
	if err != nil {
		return TranscribeResult{}, err
	}
	part.Write(clip.WAV())
	if req.Language != "" {
		w.WriteField("language", req.Language)
	}
	if hot := strings.Join(strings.Fields(req.Prompt), " "); hot != "" {
		w.WriteField("hotwords", hot)
	}
	w.Close()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Base+"/asr", &body)
	if err != nil {
		return TranscribeResult{}, err
	}
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := a.HTTP.Do(httpReq)
	if err != nil {
		return TranscribeResult{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return TranscribeResult{}, err
	}
	var out audio8ASRResult
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return TranscribeResult{}, &RequestError{Param: "file", Msg: fmt.Sprintf("the model's service refused the clip: %s", snippet(raw))}
	}
	if err := json.Unmarshal(raw, &out); err != nil || resp.StatusCode != http.StatusOK {
		return TranscribeResult{}, fmt.Errorf("audio8 asr: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	return TranscribeResult{Text: out.Text, Language: req.Language,
		Elapsed: time.Duration(out.Elapsed * float64(time.Second)), Tokens: out.Generated}, nil
}

// Audio8TTS is the Audio8 0.1B INT8 ONNX runtime's HTTP service
// (Audio8-AI/Audio8_TTS, onnx_runtime_0_1b_int8: POST /api/tts/stream).
//
// The stream is newline-delimited JSON: a start event with the rate, then
// audio_chunk events carrying base64 PCM16, then complete. Cancelling is a
// POST to /api/tts/cancel, which stops whatever the service is on — it runs
// one synthesis at a time.
type Audio8TTS struct {
	Base string
	HTTP *http.Client
	// MaxNewTokens bounds a synthesis: 46 ms of audio a token.
	MaxNewTokens int
}

// NewAudio8TTS serves the runtime at base (http://127.0.0.1:8024).
func NewAudio8TTS(base string) *Audio8TTS {
	return &Audio8TTS{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{}, MaxNewTokens: 512}
}

// Model is the model entry for the runtime.
func (a *Audio8TTS) Model(id string) *Model {
	return &Model{ID: id, Kind: Speech, Family: "audio8-tts", Upstream: a.Base, Rate: 44100, Streaming: true,
		Licence: "Apache 2.0",
		Note:    "voices are the names the runtime has registered (\"default\" after register_default_voice.py); audio is streamed as it is made",
		speaker: a, health: a.Health}
}

// Health asks the service's /api/health.
func (a *Audio8TTS) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.Base+"/api/health", nil)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}

type audio8Event struct {
	Event      string `json:"event"`
	SampleRate int    `json:"sample_rate"`
	Seq        int    `json:"seq"`
	FrameCount int    `json:"frame_count"`
	PCM        string `json:"pcm_b64"`
	Detail     any    `json:"detail"`
}

// Speak opens the stream and decodes it chunk by chunk.
func (a *Audio8TTS) Speak(ctx context.Context, req SpeakRequest) (*SpeechStream, error) {
	voice := req.Voice
	if voice == "" {
		voice = "default"
	}
	body, _ := json.Marshal(map[string]any{"text": req.Text, "voice_name": voice, "max_new_tokens": a.MaxNewTokens})
	ctx, cancel := context.WithCancel(ctx)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Base+"/api/tts/stream", bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTP.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		cancel()
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, &RequestError{Param: "voice", Msg: fmt.Sprintf("the model's service refused the request: %s", snippet(raw))}
		}
		return nil, fmt.Errorf("audio8 tts: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var start audio8Event
	if !sc.Scan() {
		resp.Body.Close()
		cancel()
		return nil, errors.New("audio8 tts: the stream ended before it started")
	}
	if err := json.Unmarshal(sc.Bytes(), &start); err != nil || start.Event != "start" {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("audio8 tts: unexpected first event: %s", snippet(sc.Bytes()))
	}
	chunks := make(chan []byte, 16)
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(chunks)
		defer close(done)
		defer resp.Body.Close()
		for sc.Scan() {
			var ev audio8Event
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				streamErr = fmt.Errorf("audio8 tts: bad event: %w", err)
				return
			}
			switch ev.Event {
			case "audio_chunk":
				pcm, err := base64.StdEncoding.DecodeString(ev.PCM)
				if err != nil {
					streamErr = fmt.Errorf("audio8 tts: bad audio: %w", err)
					return
				}
				select {
				case chunks <- pcm:
				case <-ctx.Done():
					return
				}
			case "complete":
				return
			case "cancelled":
				streamErr = context.Canceled
				return
			}
		}
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			streamErr = fmt.Errorf("audio8 tts: %w", err)
		}
	}()
	cancelAll := func() {
		cancel()
		// The service runs one synthesis at a time; tell it to stop ours.
		c, cc := context.WithTimeout(context.Background(), 2*time.Second)
		defer cc()
		r, _ := http.NewRequestWithContext(c, http.MethodPost, a.Base+"/api/tts/cancel", nil)
		if resp, err := a.HTTP.Do(r); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
	return &SpeechStream{Rate: start.SampleRate, Chunks: chunks, Cancel: cancelAll,
		Err: func() error { <-done; return streamErr }}, nil
}

func snippet(raw []byte) string {
	var e struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Detail != nil {
		if s, ok := e.Detail.(string); ok {
			return s
		}
		b, _ := json.Marshal(e.Detail)
		return string(b)
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
