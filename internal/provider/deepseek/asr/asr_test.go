package asr

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
)

// collect drains the stream until it closes, failing the test after timeout.
func collect(t *testing.T, st provider.ASRStream, timeout time.Duration) []provider.ASRResult {
	t.Helper()
	var out []provider.ASRResult
	deadline := time.After(timeout)
	for {
		select {
		case r, ok := <-st.Results():
			if !ok {
				return out
			}
			out = append(out, r)
		case <-deadline:
			t.Fatalf("results did not close within %s (got %d results)", timeout, len(out))
		}
	}
}

type requestObservation struct {
	auth string
	rate int
}

// TestFinalCarriesTheWholeUtterance is the adapter's core promise: the audio
// the engine wrote is sent once, as WAV, and the answer comes back as a final.
func TestFinalCarriesTheWholeUtterance(t *testing.T) {
	var requests atomic.Int32
	obs := make(chan requestObservation, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("request has no file field: %v", err)
			http.Error(w, "no file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Errorf("reading audio: %v", err)
			return
		}
		if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
			t.Errorf("the audio is not a RIFF/WAVE file: %v", data[0:12])
		}
		select {
		case obs <- requestObservation{auth: r.Header.Get("Authorization"), rate: int(binary.LittleEndian.Uint32(data[24:28]))}:
		default:
		}
		fmt.Fprint(w, `{"request_id":"r1","text":"你好，世界","duration_ms":640}`)
	}))
	defer srv.Close()

	a := &ASR{Endpoint: srv.URL, APIKey: "test-key", PartialTimeout: time.Second, FinalTimeout: 2 * time.Second}
	st, err := a.Open(context.Background(), provider.ASROptions{SampleRate: provider.PipelineRate})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 300 ms of silence is enough audio to make a request worth sending.
	if err := st.Write(make([]byte, provider.PipelineRate*2*300/1000)); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}

	results := collect(t, st, 3*time.Second)
	if len(results) != 1 {
		t.Fatalf("got %d results, want exactly the final: %+v", len(results), results)
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("final reported an error: %v", r.Err)
	}
	if !r.Final {
		t.Fatalf("the only result was not marked final: %+v", r)
	}
	if r.Text != "你好，世界" {
		t.Fatalf("text = %q, want the transcript the tool returned", r.Text)
	}
	select {
	case got := <-obs:
		if got.rate != provider.PipelineRate {
			t.Fatalf("WAV declared %d Hz, want the pipeline rate %d", got.rate, provider.PipelineRate)
		}
		if got.auth != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want the bearer key", got.auth)
		}
	default:
		t.Fatal("the tool was never called")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("sent %d requests, want exactly one for a final with partials disabled", got)
	}
}

// TestPartialHypothesisBeforeTheFinal covers the pseudo-streaming: while the
// caller is speaking, the buffer is re-transcribed and the result published as
// a partial, in the shape the engine's pause and endpointing logic reads.
func TestPartialHypothesisBeforeTheFinal(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if file, _, err := r.FormFile("file"); err == nil {
			_, _ = io.Copy(io.Discard, file)
			file.Close()
		}
		text := "你好"
		if n > 1 {
			text = "你好，世界"
		}
		fmt.Fprintf(w, `{"text":%q,"duration_ms":300}`, text)
	}))
	defer srv.Close()

	a := &ASR{
		Endpoint:        srv.URL,
		APIKey:          "test-key",
		PartialInterval: 20 * time.Millisecond,
		PartialTimeout:  time.Second,
		FinalTimeout:    2 * time.Second,
	}
	st, err := a.Open(context.Background(), provider.ASROptions{SampleRate: provider.PipelineRate, Interim: true})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.Write(make([]byte, provider.PipelineRate*2*300/1000)); err != nil {
		t.Fatal(err)
	}

	// Wait for the first hypothesis, which must not claim to be final.
	deadline := time.After(3 * time.Second)
wait:
	for {
		select {
		case r, ok := <-st.Results():
			if !ok {
				t.Fatal("results closed before a final was asked for")
			}
			if r.Err != nil {
				t.Fatalf("partial failed: %v", r.Err)
			}
			if r.Final {
				t.Fatalf("a final arrived before CloseSend: %+v", r)
			}
			if r.Text != "" {
				if r.Text != "你好" {
					t.Fatalf("partial = %q, want the tool's first answer", r.Text)
				}
				break wait
			}
		case <-deadline:
			t.Fatal("no partial hypothesis within 3 s")
		}
	}

	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var final *provider.ASRResult
	var rest []provider.ASRResult
	for _, r := range collect(t, st, 3*time.Second) {
		if r.Final {
			r := r
			final = &r
			continue
		}
		rest = append(rest, r)
	}
	if final == nil {
		t.Fatalf("no final after CloseSend; results: %+v", rest)
	}
	if final.Err != nil {
		t.Fatalf("final reported an error: %v", final.Err)
	}
	if final.Text != "你好，世界" {
		t.Fatalf("final = %q, want the whole-utterance transcript", final.Text)
	}
	if requests.Load() < 2 {
		t.Fatalf("sent %d requests, want at least a partial and a final", requests.Load())
	}
}

// TestFinalFailureIsReported: the engine has a fallback for a recognizer that
// cannot finish, and it needs the failure on the results channel to take it.
func TestFinalFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := &ASR{Endpoint: srv.URL, APIKey: "test-key", PartialTimeout: time.Second, FinalTimeout: time.Second}
	st, err := a.Open(context.Background(), provider.ASROptions{SampleRate: provider.PipelineRate})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Write(make([]byte, provider.PipelineRate*2/5)); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	results := collect(t, st, 3*time.Second)
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("got %+v, want one error result and then a closed stream", results)
	}
	var se *statusError
	if !errors.As(results[0].Err, &se) || se.code != http.StatusInternalServerError {
		t.Fatalf("error = %v, want the HTTP status from the tool", results[0].Err)
	}
	if retryable(results[0].Err) {
		t.Fatalf("a 500 must not be treated as a transient refusal")
	}
}

// TestCloseWithoutCloseSendEndsTheStream: a hard close is how a barge-in
// discards a recognition, and it must not leak a goroutine or a channel.
func TestCloseWithoutCloseSendEndsTheStream(t *testing.T) {
	a := &ASR{Endpoint: "http://127.0.0.1:1", APIKey: "test-key", PartialTimeout: time.Second, FinalTimeout: time.Second}
	st, err := a.Open(context.Background(), provider.ASROptions{SampleRate: provider.PipelineRate})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Write(make([]byte, 3200))
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-st.Results():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("results did not close after Close")
		}
	}
}

func TestWavHeader(t *testing.T) {
	pcm := make([]byte, 640)
	wav := wavBytes(pcm, 16000)
	if len(wav) != 44+len(pcm) {
		t.Fatalf("wav length = %d, want %d", len(wav), 44+len(pcm))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatalf("bad RIFF/WAVE/data markers")
	}
	if got := binary.LittleEndian.Uint32(wav[4:8]); got != uint32(36+len(pcm)) {
		t.Fatalf("RIFF size = %d, want %d", got, 36+len(pcm))
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 16000 {
		t.Fatalf("sample rate = %d, want 16000", got)
	}
	if got := binary.LittleEndian.Uint16(wav[34:36]); got != 16 {
		t.Fatalf("bits per sample = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(len(pcm)) {
		t.Fatalf("data size = %d, want %d", got, len(pcm))
	}
}

func TestWavRateRejectsUnsupportedRates(t *testing.T) {
	for _, rate := range []int{8000, 16000, 22050, 24000, 32000, 44100, 48000} {
		if got := wavRate(rate); got != rate {
			t.Errorf("wavRate(%d) = %d, want the documented rate", rate, got)
		}
	}
	if got := wavRate(12345); got != provider.PipelineRate {
		t.Errorf("wavRate(12345) = %d, want the pipeline fallback", got)
	}
}
