package edge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is the gateway.
type Server struct {
	// Key, when set, is the bearer token every request but /healthz must carry.
	Key string
	// QueueWait is how long a request waits for a busy model before 429.
	QueueWait time.Duration
	// MaxAudioBytes bounds an upload.
	MaxAudioBytes int64
	Log           *slog.Logger

	mu     sync.Mutex
	models map[string]*Model
	order  []string
	locks  map[string]chan struct{} // one slot per model
	start  time.Time
}

// NewServer makes an empty gateway; add models with Add.
func NewServer() *Server {
	return &Server{QueueWait: 10 * time.Second, MaxAudioBytes: 32 << 20, Log: slog.Default(),
		models: map[string]*Model{}, locks: map[string]chan struct{}{}, start: time.Now()}
}

// Add serves a model.
func (s *Server) Add(m *Model) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models[m.ID] = m
	s.order = append(s.order, m.ID)
	s.locks[m.ID] = make(chan struct{}, 1)
}

// Models is every model, in the order added.
func (s *Server) Models() []*Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Model, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.models[id])
	}
	return out
}

// Handler is the HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /v1/models", s.auth(s.listModels))
	mux.HandleFunc("GET /v1/models/{id}", s.auth(s.getModel))
	mux.HandleFunc("POST /v1/audio/transcriptions", s.auth(s.transcribe))
	mux.HandleFunc("POST /v1/audio/speech", s.auth(s.speech))
	mux.HandleFunc("GET /v1/audio/diarize", s.auth(s.diarize))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, http.StatusNotFound, "invalid_request_error", "", fmt.Sprintf("there is no %s %s here; see /v1/models", r.Method, r.URL.Path))
	})
	return s.requestID(mux)
}

// --- the error object ---

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, typ, param, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": apiError{Message: msg, Type: typ, Param: param, Code: codeFor(status)}})
	s.Log.Info("edge: refused", "id", requestIDOf(r), "method", r.Method, "path", r.URL.Path, "status", status, "msg", msg)
}

func codeFor(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "invalid_api_key"
	case http.StatusNotFound:
		return "model_not_found"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "model_unavailable"
	}
	return ""
}

// failErr maps a backend error to a status.
func (s *Server) failErr(w http.ResponseWriter, r *http.Request, err error) {
	var reqErr *RequestError
	switch {
	case errors.As(err, &reqErr):
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", reqErr.Param, reqErr.Msg)
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", "2")
		s.fail(w, r, http.StatusTooManyRequests, "rate_limit_error", "", "the model is busy with another request; try again in a moment")
	case errors.Is(err, ErrUnavailable):
		s.fail(w, r, http.StatusBadGateway, "server_error", "", err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.fail(w, r, 499, "server_error", "", "the request was cancelled")
	default:
		s.fail(w, r, http.StatusInternalServerError, "server_error", "", err.Error())
	}
}

// --- middleware ---

type ctxKey int

const idKey ctxKey = 1

func requestIDOf(r *http.Request) string {
	id, _ := r.Context().Value(idKey).(string)
	return id
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			var b [8]byte
			rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), idKey, id)))
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Key != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.Key)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="edge"`)
				s.fail(w, r, http.StatusUnauthorized, "authentication_error", "", "a bearer token is required; it is in the gateway's key file")
				return
			}
		}
		next(w, r)
	}
}

// acquire takes the model's one slot, waiting up to QueueWait.
func (s *Server) acquire(ctx context.Context, id string) (release func(), err error) {
	s.mu.Lock()
	slot := s.locks[id]
	s.mu.Unlock()
	timer := time.NewTimer(s.QueueWait)
	defer timer.Stop()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-timer.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) model(w http.ResponseWriter, r *http.Request, id string, kind Kind) (*Model, bool) {
	s.mu.Lock()
	m := s.models[id]
	s.mu.Unlock()
	if id == "" {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "model", "model is required; see /v1/models")
		return nil, false
	}
	if m == nil {
		s.fail(w, r, http.StatusNotFound, "invalid_request_error", "model", fmt.Sprintf("there is no model %q here; see /v1/models", id))
		return nil, false
	}
	if m.Kind != kind {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "model", fmt.Sprintf("%s is a %s model, not %s", id, m.Kind, kind))
		return nil, false
	}
	return m, true
}

// --- models ---

type modelView struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Edge    struct {
		Kind      Kind   `json:"kind"`
		Family    string `json:"family"`
		Status    string `json:"status"`
		Error     string `json:"error,omitempty"`
		Streaming bool   `json:"streaming"`
		Rate      int    `json:"sample_rate,omitempty"`
		Licence   string `json:"licence,omitempty"`
		Note      string `json:"note,omitempty"`
		Upstream  string `json:"upstream,omitempty"`
	} `json:"edge"`
}

func (s *Server) view(ctx context.Context, m *Model) modelView {
	v := modelView{ID: m.ID, Object: "model", Created: s.start.Unix(), OwnedBy: "edge"}
	v.Edge.Kind, v.Edge.Family, v.Edge.Streaming, v.Edge.Rate = m.Kind, m.Family, m.Streaming, m.Rate
	v.Edge.Licence, v.Edge.Note, v.Edge.Upstream = m.Licence, m.Note, m.Upstream
	v.Edge.Status = "ok"
	if err := m.Health(ctx); err != nil {
		v.Edge.Status, v.Edge.Error = "unavailable", err.Error()
	}
	return v
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	models := s.Models()
	views := make([]modelView, len(models))
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func() { defer wg.Done(); views[i] = s.view(r.Context(), m) }()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": views})
}

func (s *Server) getModel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	m := s.models[r.PathValue("id")]
	s.mu.Unlock()
	if m == nil {
		s.fail(w, r, http.StatusNotFound, "invalid_request_error", "model", "no such model; see /v1/models")
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), m))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	models := map[string]string{}
	ok := true
	for _, m := range s.Models() {
		if err := m.Health(r.Context()); err != nil {
			models[m.ID] = err.Error()
			ok = false
		} else {
			models[m.ID] = "ok"
		}
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ok": ok, "models": models, "uptime_s": int(time.Since(s.start).Seconds())})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// --- transcriptions ---

// readClip takes the audio from a request: a multipart form with a "file"
// part (OpenAI's shape), or a raw body of audio/wav or audio/pcm with the rate
// in the media type (audio/pcm;rate=16000) or in X-Edge-Sample-Rate.
func (s *Server) readClip(r *http.Request) (clip Clip, fields map[string]string, err error) {
	fields = map[string]string{}
	ct, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var data []byte
	switch {
	case ct == "multipart/form-data":
		if err := r.ParseMultipartForm(s.MaxAudioBytes); err != nil {
			return Clip{}, nil, fmt.Errorf("the form could not be read: %w", err)
		}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				fields[k] = v[0]
			}
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return Clip{}, nil, errors.New("a \"file\" part with the audio is required")
		}
		defer f.Close()
		data, err = readAll(f, s.MaxAudioBytes)
		if err != nil {
			return Clip{}, nil, err
		}
		ct, params, _ = mime.ParseMediaType(hdr.Header.Get("Content-Type"))
		if strings.HasSuffix(strings.ToLower(hdr.Filename), ".wav") {
			ct = "audio/wav"
		} else if strings.HasSuffix(strings.ToLower(hdr.Filename), ".pcm") || strings.HasSuffix(strings.ToLower(hdr.Filename), ".raw") {
			ct = "audio/pcm"
		}
	case ct == "audio/wav", ct == "audio/x-wav", ct == "audio/wave", ct == "audio/pcm", ct == "audio/l16", ct == "application/octet-stream":
		for k := range r.URL.Query() {
			fields[k] = r.URL.Query().Get(k)
		}
		data, err = readAll(r.Body, s.MaxAudioBytes)
		if err != nil {
			return Clip{}, nil, err
		}
	default:
		return Clip{}, nil, fmt.Errorf("audio is taken as multipart/form-data with a \"file\" part, or as a raw audio/wav or audio/pcm body; not %q", ct)
	}
	if len(data) == 0 {
		return Clip{}, nil, errors.New("the audio is empty")
	}
	if len(data) >= 4 && string(data[:4]) == "RIFF" || ct == "audio/wav" || ct == "audio/x-wav" || ct == "audio/wave" {
		clip, err = DecodeWAV(data)
		return clip, fields, err
	}
	rate, _ := strconv.Atoi(params["rate"])
	if rate == 0 {
		rate, _ = strconv.Atoi(r.Header.Get("X-Edge-Sample-Rate"))
	}
	if rate == 0 {
		rate, _ = strconv.Atoi(fields["sample_rate"])
	}
	if rate == 0 {
		return Clip{}, nil, errors.New("raw PCM needs its rate: audio/pcm;rate=16000, or X-Edge-Sample-Rate")
	}
	clip, err = DecodePCM(data, rate)
	return clip, fields, err
}

func (s *Server) transcribe(w http.ResponseWriter, r *http.Request) {
	began := time.Now()
	clip, fields, err := s.readClip(r)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "file", err.Error())
		return
	}
	m, ok := s.model(w, r, fields["model"], Transcription)
	if !ok {
		return
	}
	format := fields["response_format"]
	if format == "" {
		format = "json"
	}
	switch format {
	case "json", "text", "verbose_json":
	default:
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "response_format", "response_format is json, text or verbose_json")
		return
	}
	stream := fields["stream"] == "true"
	if clip.Seconds() > 60 {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "file", fmt.Sprintf("the clip is %.0f s; a minute is the most one request takes", clip.Seconds()))
		return
	}
	release, err := s.acquire(r.Context(), m.ID)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	queued := time.Since(began)
	res, err := m.transcriber.Transcribe(r.Context(), clip.Resampled(m.Rate), TranscribeRequest{Language: fields["language"], Prompt: fields["prompt"]})
	release()
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	elapsed := time.Since(began)
	s.Log.Info("edge: transcribed", "id", requestIDOf(r), "model", m.ID, "audio_ms", int(clip.Seconds()*1000),
		"ms", elapsed.Milliseconds(), "queued_ms", queued.Milliseconds(), "model_ms", res.Elapsed.Milliseconds(), "chars", len([]rune(res.Text)))
	h := w.Header()
	h.Set("X-Edge-Elapsed-Ms", strconv.FormatInt(elapsed.Milliseconds(), 10))
	h.Set("X-Edge-Model-Ms", strconv.FormatInt(res.Elapsed.Milliseconds(), 10))
	h.Set("X-Edge-Audio-Ms", strconv.Itoa(int(clip.Seconds()*1000)))
	if stream {
		s.streamTranscript(w, res.Text)
		return
	}
	switch format {
	case "text":
		h.Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, res.Text+"\n")
	case "json":
		writeJSON(w, http.StatusOK, map[string]any{"text": res.Text})
	case "verbose_json":
		lang := res.Language
		writeJSON(w, http.StatusOK, map[string]any{
			"task": "transcribe", "language": lang, "duration": round3(clip.Seconds()), "text": res.Text,
			"segments": []map[string]any{{"id": 0, "start": 0.0, "end": round3(clip.Seconds()), "text": res.Text}},
			"edge": map[string]any{"model": m.ID, "elapsed_ms": elapsed.Milliseconds(), "model_ms": res.Elapsed.Milliseconds(),
				"queued_ms": queued.Milliseconds(), "tokens": res.Tokens, "audio_ms": int(clip.Seconds() * 1000)},
		})
	}
}

// streamTranscript sends the text as OpenAI's transcription events. The models
// here answer whole, so the delta is the whole text; a client written for the
// streaming shape still gets what it expects.
func (s *Server) streamTranscript(w http.ResponseWriter, text string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	send(map[string]any{"type": "transcript.text.delta", "delta": text})
	send(map[string]any{"type": "transcript.text.done", "text": text})
}

func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

// --- speech ---

type speechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`
	StreamFormat   string  `json:"stream_format"`
	// SampleRate is ours: the rate the audio is wanted at. The model's own
	// when not given.
	SampleRate int `json:"sample_rate"`
}

func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	began := time.Now()
	var req speechRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "", "the body is a JSON object: model, input, voice, response_format")
		return
	}
	m, ok := s.model(w, r, req.Model, Speech)
	if !ok {
		return
	}
	req.Input = strings.TrimSpace(req.Input)
	if req.Input == "" {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "input", "input is required")
		return
	}
	if n := len([]rune(req.Input)); n > 1000 {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "input", fmt.Sprintf("input is %d characters; 1000 is the most", n))
		return
	}
	format := req.ResponseFormat
	if format == "" {
		format = "wav"
	}
	switch format {
	case "wav", "pcm":
	default:
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "response_format", "response_format is wav or pcm (16-bit mono, little-endian)")
		return
	}
	if req.StreamFormat != "" && req.StreamFormat != "audio" {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "stream_format", "stream_format is audio: the bytes as they are made")
		return
	}
	rate := req.SampleRate
	if rate == 0 {
		rate = m.Rate
	}
	if rate < 8000 || rate > 48000 {
		s.fail(w, r, http.StatusBadRequest, "invalid_request_error", "sample_rate", "sample_rate is 8000 to 48000")
		return
	}
	release, err := s.acquire(r.Context(), m.ID)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	defer release()
	st, err := m.speaker.Speak(r.Context(), SpeakRequest{Text: req.Input, Voice: req.Voice, Speed: req.Speed})
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	defer st.Cancel()
	h := w.Header()
	if format == "wav" {
		h.Set("Content-Type", "audio/wav")
	} else {
		h.Set("Content-Type", fmt.Sprintf("audio/pcm;rate=%d", rate))
	}
	h.Set("X-Edge-Sample-Rate", strconv.Itoa(rate))
	h.Set("X-Edge-Model-Sample-Rate", strconv.Itoa(st.Rate))
	h.Set("Cache-Control", "no-store")
	fl, _ := w.(http.Flusher)
	first := true
	var out int
	var firstAt time.Duration
	for pcm := range st.Chunks {
		if st.Rate != rate {
			pcm = resamplePCM(pcm, st.Rate, rate)
		}
		if first {
			first = false
			firstAt = time.Since(began)
			h.Set("X-Edge-First-Audio-Ms", strconv.FormatInt(firstAt.Milliseconds(), 10))
			w.WriteHeader(http.StatusOK)
			if format == "wav" {
				w.Write(WAVHeader(rate, -1))
			}
		}
		if _, err := w.Write(pcm); err != nil {
			break
		}
		out += len(pcm)
		if fl != nil {
			fl.Flush()
		}
	}
	err = st.Err()
	if first { // nothing came
		if err == nil {
			err = errors.New("the model made no audio")
		}
		s.failErr(w, r, err)
		return
	}
	s.Log.Info("edge: spoke", "id", requestIDOf(r), "model", m.ID, "chars", len([]rune(req.Input)), "first_audio_ms", firstAt.Milliseconds(),
		"ms", time.Since(began).Milliseconds(), "audio_ms", out/2*1000/rate, "err", errString(err))
}

func resamplePCM(pcm []byte, from, to int) []byte {
	c, err := DecodePCM(pcm, from)
	if err != nil {
		return nil
	}
	return c.Resampled(to).PCM16()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// sortedKeys is for deterministic output in tests.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
