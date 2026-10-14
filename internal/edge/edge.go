// Package edge serves self-hosted speech models behind an OpenAI-compatible
// API, so that a model run on a machine in the room can stand in a test
// group beside the vendors golive is built on, and be asked for the same way.
//
// What is served:
//
//	GET  /v1/models                   the models, what each is, and whether it is up
//	GET  /v1/models/{id}
//	POST /v1/audio/transcriptions     speech to text (multipart as OpenAI takes it, or a raw body)
//	POST /v1/audio/speech             text to speech, streamed as it is made
//	GET  /healthz                     the gateway and each model's service
//
// The shapes are OpenAI's where OpenAI has one, so an OpenAI client points at
// this and works. Where the API says nothing — which sample rate a stream
// should be in, how long the model took — the answer is in a field or a header
// that starts with "edge" or "X-Edge-", never in a field that means something
// else to OpenAI. Errors are OpenAI's error object. See docs/edge-api.md.
//
// A model here is a CPU: one request at a time each, the next waiting a bounded
// time and then refused with 429, so that a burst does not turn into a queue
// nobody can see the end of.
package edge

import (
	"context"
	"errors"
	"time"
)

// Kind is what a model does.
type Kind string

const (
	Transcription Kind = "transcription"
	Speech        Kind = "speech"
)

// Model is one model served, by its public name.
type Model struct {
	// ID is the name a request gives as "model".
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Family says which runtime stands behind it: audio8-asr, audio8-tts, vibeasr.
	Family string `json:"family"`
	// Upstream is where the model's own service is, for the operator.
	Upstream string `json:"upstream,omitempty"`
	// Licence is the model's, because a model served here may be licensed for
	// less than the vendors are: Audio8-ASR is CC BY-NC 4.0.
	Licence string `json:"licence,omitempty"`
	// Note is anything a user of the model should know: that it is not
	// streaming, what rate it wants, that its output is unverified on this host.
	Note string `json:"note,omitempty"`
	// Streaming says whether the model gives words (or audio) before it is done.
	Streaming bool `json:"streaming"`
	// Rate is the sample rate the model takes (transcription) or makes (speech).
	Rate int `json:"sample_rate,omitempty"`

	transcriber Transcriber
	speaker     Speaker
	health      func(ctx context.Context) error
}

// TranscribeRequest is what a transcription asks for beyond the audio.
type TranscribeRequest struct {
	Language string
	// Prompt is OpenAI's: words the model should expect. A model that takes
	// hot words gets them from it, one per whitespace-separated token.
	Prompt string
}

// TranscribeResult is what came back.
type TranscribeResult struct {
	Text     string
	Language string
	// Elapsed is the model's own time on the request, when it says.
	Elapsed time.Duration
	// Tokens is how many it generated, when it says.
	Tokens int
}

// Transcriber turns a clip into text. The clip is at the model's rate, mono.
type Transcriber interface {
	Transcribe(ctx context.Context, clip Clip, req TranscribeRequest) (TranscribeResult, error)
}

// SpeakRequest is what a synthesis asks for.
type SpeakRequest struct {
	Text  string
	Voice string
	Speed float64
}

// SpeechStream is audio as it is made: PCM16 mono at Rate, then Chunks is
// closed; an error, if any, is in Err once Chunks is closed.
type SpeechStream struct {
	Rate   int
	Chunks <-chan []byte
	Err    func() error
	// Cancel stops the synthesis; the model's service is told when it can be.
	Cancel func()
}

// Speaker turns text into audio.
type Speaker interface {
	Speak(ctx context.Context, req SpeakRequest) (*SpeechStream, error)
}

// ErrUnavailable is a model whose service is not answering.
var ErrUnavailable = errors.New("edge: the model's service is not answering")

// RequestError is a request the model's service turned down as malformed —
// an unknown voice, text it cannot take — which is the caller's to fix.
type RequestError struct {
	Param string
	Msg   string
}

func (e *RequestError) Error() string { return e.Msg }

// ErrBusy is a model that has a request already and could not take this one
// within the wait allowed.
var ErrBusy = errors.New("edge: the model is busy")

// Health says whether the model's service is up.
func (m *Model) Health(ctx context.Context) error {
	if m.health == nil {
		return nil
	}
	return m.health(ctx)
}
