package edge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAudio8 stands in for both runtimes: /asr answers with the clip's
// length, /api/tts/stream makes a tone in the runtime's event shape.
type fakeAudio8 struct {
	mu       sync.Mutex
	asrSeen  []float64 // seconds of each clip, as decoded
	hotwords []string
	slow     time.Duration
	cancels  int
}

func (f *fakeAudio8) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) })
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) })
	mux.HandleFunc("POST /asr", func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(32 << 20)
		file, _, err := r.FormFile("audio")
		if err != nil {
			http.Error(w, `{"detail":"no audio"}`, 400)
			return
		}
		data, _ := io.ReadAll(file)
		clip, err := DecodeWAV(data)
		if err != nil || clip.Rate != 16000 {
			http.Error(w, `{"detail":"not a 16k wav"}`, 400)
			return
		}
		f.mu.Lock()
		f.asrSeen = append(f.asrSeen, clip.Seconds())
		f.hotwords = strings.Fields(r.FormValue("hotwords"))
		f.mu.Unlock()
		time.Sleep(f.slow)
		json.NewEncoder(w).Encode(map[string]any{"text": fmt.Sprintf("%.1f秒", clip.Seconds()), "elapsed_seconds": 0.25, "generated_tokens": 7})
	})
	mux.HandleFunc("POST /api/tts/stream", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text         string `json:"text"`
			Voice        string `json:"voice_name"`
			MaxNewTokens int    `json:"max_new_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Voice != "default" {
			http.Error(w, `{"detail":"unknown voice"}`, 404)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		enc := json.NewEncoder(w)
		enc.Encode(map[string]any{"event": "start", "sample_rate": 44100})
		fl.Flush()
		// 3 chunks of 100 ms each of a 440 Hz tone
		for seq := 0; seq < 3; seq++ {
			pcm := make([]byte, 4410*2)
			for i := 0; i < 4410; i++ {
				v := int16(10000 * math.Sin(2*math.Pi*440*float64(seq*4410+i)/44100))
				pcm[2*i], pcm[2*i+1] = byte(v), byte(v>>8)
			}
			time.Sleep(f.slow)
			if r.Context().Err() != nil {
				return
			}
			enc.Encode(map[string]any{"event": "audio_chunk", "seq": seq, "frame_count": 4410, "pcm_b64": base64.StdEncoding.EncodeToString(pcm)})
			fl.Flush()
		}
		enc.Encode(map[string]any{"event": "complete"})
	})
	mux.HandleFunc("POST /api/tts/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cancels++
		f.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	})
	return mux
}

func newTestGateway(t *testing.T, f *fakeAudio8) (*Server, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(f.handler())
	t.Cleanup(up.Close)
	s := NewServer()
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Key = "secret"
	s.QueueWait = 200 * time.Millisecond
	s.Add(NewAudio8ASR(up.URL).Model("audio8-asr"))
	s.Add(NewAudio8TTS(up.URL).Model("audio8-tts"))
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return s, gw
}

func do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func tone(rate int, seconds float64) Clip {
	n := int(float64(rate) * seconds)
	c := Clip{Rate: rate, Samples: make([]float32, n)}
	for i := range c.Samples {
		c.Samples[i] = float32(0.3 * math.Sin(2*math.Pi*300*float64(i)/float64(rate)))
	}
	return c
}

func TestModelsListAndHealth(t *testing.T) {
	_, gw := newTestGateway(t, &fakeAudio8{})
	req, _ := http.NewRequest("GET", gw.URL+"/v1/models", nil)
	resp, body := do(t, req)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var list struct {
		Object string      `json:"object"`
		Data   []modelView `json:"data"`
	}
	json.Unmarshal(body, &list)
	if list.Object != "list" || len(list.Data) != 2 || list.Data[0].ID != "audio8-asr" || list.Data[1].ID != "audio8-tts" {
		t.Fatalf("list: %s", body)
	}
	if list.Data[0].Edge.Status != "ok" || list.Data[0].Edge.Kind != Transcription || list.Data[0].Edge.Licence == "" {
		t.Fatalf("asr entry: %+v", list.Data[0])
	}
	if list.Data[1].Edge.Rate != 44100 || !list.Data[1].Edge.Streaming {
		t.Fatalf("tts entry: %+v", list.Data[1])
	}
	// no token: refused with OpenAI's error object
	resp, err := http.Get(gw.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 || !strings.Contains(string(body), `"type":"authentication_error"`) || !strings.Contains(string(body), `"code":"invalid_api_key"`) {
		t.Fatalf("no token: %d %s", resp.StatusCode, body)
	}
	// healthz needs no token
	resp, err = http.Get(gw.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"audio8-asr":"ok"`) {
		t.Fatalf("healthz: %d %s", resp.StatusCode, body)
	}
}

func TestHealthReportsAModelWhoseServiceIsDown(t *testing.T) {
	s := NewServer()
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Add(NewAudio8ASR("http://127.0.0.1:1").Model("audio8-asr"))
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	resp, _ := http.Get(gw.URL + "/healthz")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 503 || !strings.Contains(string(body), `"ok":false`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest("GET", gw.URL+"/v1/models/audio8-asr", nil)
	resp, body = do(t, req)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"status":"unavailable"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	// a request to it is a 502 with model_unavailable
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("model", "audio8-asr")
	p, _ := w.CreateFormFile("file", "a.wav")
	p.Write(tone(16000, 0.5).WAV())
	w.Close()
	req, _ = http.NewRequest("POST", gw.URL+"/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, body = do(t, req)
	if resp.StatusCode != 502 || !strings.Contains(string(body), `"code":"model_unavailable"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func transcribeForm(t *testing.T, gw string, clip Clip, fields map[string]string) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		w.WriteField(k, v)
	}
	p, _ := w.CreateFormFile("file", "clip.wav")
	p.Write(clip.WAV())
	w.Close()
	req, _ := http.NewRequest("POST", gw+"/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return do(t, req)
}

func TestTranscriptionResamplesAndAnswersInEachFormat(t *testing.T) {
	f := &fakeAudio8{}
	_, gw := newTestGateway(t, f)
	// a 44.1 kHz clip of 1.5 s reaches the model as 16 kHz, 1.5 s
	resp, body := transcribeForm(t, gw.URL, tone(44100, 1.5), map[string]string{"model": "audio8-asr", "prompt": "车险 报价"})
	if resp.StatusCode != 200 || string(bytes.TrimSpace(body)) != `{"text":"1.5秒"}` {
		t.Fatalf("json: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Edge-Audio-Ms") != "1500" || resp.Header.Get("X-Edge-Model-Ms") != "250" || resp.Header.Get("X-Request-Id") == "" {
		t.Fatalf("headers: %v", resp.Header)
	}
	f.mu.Lock()
	if len(f.asrSeen) != 1 || math.Abs(f.asrSeen[0]-1.5) > 0.01 || strings.Join(f.hotwords, ",") != "车险,报价" {
		t.Fatalf("upstream saw %v hotwords %v", f.asrSeen, f.hotwords)
	}
	f.mu.Unlock()

	resp, body = transcribeForm(t, gw.URL, tone(16000, 0.5), map[string]string{"model": "audio8-asr", "response_format": "text"})
	if resp.StatusCode != 200 || string(body) != "0.5秒\n" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("text: %d %q %s", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}

	resp, body = transcribeForm(t, gw.URL, tone(16000, 0.5), map[string]string{"model": "audio8-asr", "response_format": "verbose_json", "language": "zh"})
	var v struct {
		Task     string  `json:"task"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
		Text     string  `json:"text"`
		Segments []struct {
			End float64 `json:"end"`
		} `json:"segments"`
		Edge struct {
			ModelMs int `json:"model_ms"`
			Tokens  int `json:"tokens"`
		} `json:"edge"`
	}
	json.Unmarshal(body, &v)
	if resp.StatusCode != 200 || v.Task != "transcribe" || v.Language != "zh" || v.Duration != 0.5 || v.Text != "0.5秒" || len(v.Segments) != 1 || v.Segments[0].End != 0.5 || v.Edge.ModelMs != 250 || v.Edge.Tokens != 7 {
		t.Fatalf("verbose: %d %s", resp.StatusCode, body)
	}

	// streamed: OpenAI's events, the whole text as one delta
	resp, body = transcribeForm(t, gw.URL, tone(16000, 0.5), map[string]string{"model": "audio8-asr", "stream": "true"})
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(string(body), `data: {"delta":"0.5秒","type":"transcript.text.delta"}`) || !strings.Contains(string(body), `"type":"transcript.text.done"`) {
		t.Fatalf("stream: %d %s", resp.StatusCode, body)
	}
}

func TestTranscriptionTakesRawPCMAndRefusesWhatItCannotRead(t *testing.T) {
	_, gw := newTestGateway(t, &fakeAudio8{})
	clip := tone(8000, 1)
	req, _ := http.NewRequest("POST", gw.URL+"/v1/audio/transcriptions?model=audio8-asr", bytes.NewReader(clip.PCM16()))
	req.Header.Set("Content-Type", "audio/pcm;rate=8000")
	resp, body := do(t, req)
	if resp.StatusCode != 200 || string(bytes.TrimSpace(body)) != `{"text":"1.0秒"}` {
		t.Fatalf("raw pcm: %d %s", resp.StatusCode, body)
	}
	// raw without a rate
	req, _ = http.NewRequest("POST", gw.URL+"/v1/audio/transcriptions?model=audio8-asr", bytes.NewReader(clip.PCM16()))
	req.Header.Set("Content-Type", "audio/pcm")
	resp, body = do(t, req)
	if resp.StatusCode != 400 || !strings.Contains(string(body), "rate") {
		t.Fatalf("no rate: %d %s", resp.StatusCode, body)
	}
	// no model
	resp, body = transcribeForm(t, gw.URL, tone(16000, 0.5), nil)
	if resp.StatusCode != 400 || !strings.Contains(string(body), `"param":"model"`) {
		t.Fatalf("no model: %d %s", resp.StatusCode, body)
	}
	// the wrong kind of model
	resp, body = transcribeForm(t, gw.URL, tone(16000, 0.5), map[string]string{"model": "audio8-tts"})
	if resp.StatusCode != 400 || !strings.Contains(string(body), "speech model") {
		t.Fatalf("wrong kind: %d %s", resp.StatusCode, body)
	}
	// unknown model
	resp, body = transcribeForm(t, gw.URL, tone(16000, 0.5), map[string]string{"model": "whisper-1"})
	if resp.StatusCode != 404 || !strings.Contains(string(body), `"code":"model_not_found"`) {
		t.Fatalf("unknown: %d %s", resp.StatusCode, body)
	}
	// too long
	resp, body = transcribeForm(t, gw.URL, tone(16000, 61), map[string]string{"model": "audio8-asr"})
	if resp.StatusCode != 400 || !strings.Contains(string(body), "a minute") {
		t.Fatalf("long: %d %s", resp.StatusCode, body)
	}
}

func TestSpeechStreamsWAVAndPCMAtTheRateAsked(t *testing.T) {
	_, gw := newTestGateway(t, &fakeAudio8{})
	body := `{"model":"audio8-tts","input":"你好"}`
	req, _ := http.NewRequest("POST", gw.URL+"/v1/audio/speech", strings.NewReader(body))
	resp, out := do(t, req)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("wav: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), out)
	}
	if resp.Header.Get("X-Edge-Sample-Rate") != "44100" || resp.Header.Get("X-Edge-First-Audio-Ms") == "" {
		t.Fatalf("headers: %v", resp.Header)
	}
	clip, err := DecodeWAV(out)
	if err != nil || clip.Rate != 44100 || math.Abs(clip.Seconds()-0.3) > 0.001 {
		t.Fatalf("decode: %v %d %.3f", err, clip.Rate, clip.Seconds())
	}
	// pcm at 16 kHz
	body = `{"model":"audio8-tts","input":"你好","response_format":"pcm","sample_rate":16000}`
	req, _ = http.NewRequest("POST", gw.URL+"/v1/audio/speech", strings.NewReader(body))
	resp, out = do(t, req)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/pcm;rate=16000" || resp.Header.Get("X-Edge-Model-Sample-Rate") != "44100" {
		t.Fatalf("pcm: %d %v", resp.StatusCode, resp.Header)
	}
	if n := len(out) / 2; math.Abs(float64(n)/16000-0.3) > 0.002 {
		t.Fatalf("pcm length: %d samples", n)
	}
	// an unknown voice is the upstream's refusal, surfaced
	body = `{"model":"audio8-tts","input":"你好","voice":"nobody"}`
	req, _ = http.NewRequest("POST", gw.URL+"/v1/audio/speech", strings.NewReader(body))
	resp, out = do(t, req)
	if resp.StatusCode != 400 || !strings.Contains(string(out), "unknown voice") || !strings.Contains(string(out), `"param":"voice"`) {
		t.Fatalf("voice: %d %s", resp.StatusCode, out)
	}
	// a bad format
	body = `{"model":"audio8-tts","input":"你好","response_format":"mp3"}`
	req, _ = http.NewRequest("POST", gw.URL+"/v1/audio/speech", strings.NewReader(body))
	resp, out = do(t, req)
	if resp.StatusCode != 400 || !strings.Contains(string(out), `"param":"response_format"`) {
		t.Fatalf("format: %d %s", resp.StatusCode, out)
	}
}

func TestSpeechIsStreamedAsItIsMadeAndCancelledWithTheClient(t *testing.T) {
	f := &fakeAudio8{slow: 150 * time.Millisecond}
	_, gw := newTestGateway(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/audio/speech", strings.NewReader(`{"model":"audio8-tts","input":"你好","response_format":"pcm"}`))
	req.Header.Set("Authorization", "Bearer secret")
	began := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 8820)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	// the first chunk came before the third was made
	if d := time.Since(began); d > 400*time.Millisecond {
		t.Fatalf("first chunk after %v: not streamed", d)
	}
	cancel()
	time.Sleep(300 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancels != 1 {
		t.Fatalf("the runtime was told to cancel %d times", f.cancels)
	}
}

func TestOneRequestAtATimePerModel(t *testing.T) {
	f := &fakeAudio8{slow: 400 * time.Millisecond}
	_, gw := newTestGateway(t, f)
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := transcribeForm(t, gw.URL, tone(16000, 0.2), map[string]string{"model": "audio8-asr"})
			codes[i] = resp.StatusCode
		}()
		time.Sleep(20 * time.Millisecond)
	}
	wg.Wait()
	// the first runs, the second waits its 200 ms and is refused, the third too
	got := map[int]int{}
	for _, c := range codes {
		got[c]++
	}
	if got[200] != 1 || got[429] != 2 {
		t.Fatalf("codes %v", codes)
	}
}
