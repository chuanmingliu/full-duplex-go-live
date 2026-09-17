// Package minimax implements streaming text-to-speech against MiniMax's T2A
// WebSocket API (wss://.../ws/v1/t2a_v2).
//
// The protocol is a long-lived task: connect, wait for connected_success, send
// task_start, then one task_continue per text segment, reading frames whose
// data.audio is hex-encoded PCM until is_final. Keeping the task open across
// segments is what keeps time-to-first-audio in the tens of milliseconds — a
// fresh connection per sentence would dominate the latency budget.
package minimax

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

func init() {
	provider.RegisterTTS("minimax", func() (provider.TTS, error) { return New() })
}

// TTS is a MiniMax T2A client.
type TTS struct {
	Endpoint       string
	APIKey         string
	Model          string
	VoiceID        string
	Speed          float64
	LanguageBoost  string
	OpenTimeout    time.Duration
	ReceiveTimeout time.Duration
	MaxIdle        time.Duration

	// --- expressiveness ---

	// Emotion biases delivery: happy, sad, angry, fearful, disgusted,
	// surprised, calm, fluent, whisper. Empty lets MiniMax choose per
	// sentence, which is usually better than committing a whole call to one
	// mood — set it only when the agent has a fixed register.
	Emotion string
	// Vol and Pitch are the plain voice controls, (0,10] and [-12,12].
	Vol   float64
	Pitch float64
	// ContinuousSound asks for continuous inference across segments rather
	// than synthesizing them concurrently.
	//
	// This is the one real fluency-versus-latency dial the vendor offers, and
	// it matters here more than most callers because golive deliberately cuts
	// a tiny first segment to get a syllable out early. Continuous inference
	// carries prosody across that seam, so the sentence sounds like one
	// sentence instead of a short phrase followed by the rest; it costs
	// latency, because a segment can no longer start before the one before it
	// finishes. Off by default: an agent that answers late sounds worse than
	// one that answers with a slightly flat first phrase. speech-2.8 only.
	ContinuousSound bool
	// VoiceModify reshapes the timbre itself — pitch, intensity and timbre in
	// [-100,100], plus an optional sound_effects preset. Sent only when set.
	VoiceModify map[string]any
	// PronunciationTone is the pronunciation dictionary, as "written/spoken"
	// pairs. The reason to care: a voice agent says the same handful of proper
	// nouns, product names and initialisms hundreds of times a day, and
	// getting them wrong is the single most noticeable unnaturalness there is.
	PronunciationTone []string
	// TimberWeights blends up to four voices by weight.
	TimberWeights []map[string]any
	// EnglishNormalization reads numbers and units English-style. Costs a
	// little latency, so it is off unless asked for.
	EnglishNormalization bool

	// --- protocol behaviour ---

	// CancelOnAbandon sends task_cancel when a synthesis is abandoned, instead
	// of draining the rest of it. Requires the bidirectional endpoint; see
	// cancelTask and Bidi.
	CancelOnAbandon bool
	// FlushPartialSegments sends task_flush after a segment that does not end
	// in sentence-ending punctuation, so the server synthesizes a short opening
	// fragment instead of holding it back. Requires the bidirectional endpoint;
	// see Synthesize and Bidi.
	FlushPartialSegments bool
	// KeepAliveEvery pings an idle connection. The server sends no pings of its
	// own and closes an idle socket at around 120 s with error 2201, so without
	// this a pause in the conversation costs a reconnect on the next turn.
	KeepAliveEvery time.Duration

	// warnOnce keeps the endpoint-mismatch warning to one line per client
	// rather than one per turn. A pointer so a TTS stays copyable — ttsprobe
	// copies one per variant to vary the endpoint, and a bare sync.Once would
	// make that a vet error.
	warnOnce *sync.Once
}

// New builds the client from the environment.
func New() (*TTS, error) {
	key, err := config.EnvRequired("MINIMAX_TTS_API_KEY")
	if err != nil {
		return nil, err
	}
	return &TTS{
		// api.minimaxi.com for mainland-platform keys, api.minimax.io for
		// global ones. Using the wrong host authenticates and then fails at
		// task_start, which is a confusing way to find out.
		Endpoint:      config.Env("MINIMAX_TTS_WEBSOCKET_ENDPOINT", "wss://api.minimaxi.com/ws/v1/t2a_v2"),
		APIKey:        key,
		Model:         config.Env("MINIMAX_TTS_MODEL", "speech-2.8-turbo"),
		VoiceID:       config.Env("MINIMAX_TTS_VOICE_ID", ""),
		Speed:         config.EnvFloat("MINIMAX_TTS_SPEED", 1.2),
		LanguageBoost: config.Env("MINIMAX_TTS_LANGUAGE_BOOST", "auto"),

		Emotion:              config.Env("MINIMAX_TTS_EMOTION", ""),
		Vol:                  config.EnvFloat("MINIMAX_TTS_VOL", 1.0),
		Pitch:                config.EnvFloat("MINIMAX_TTS_PITCH", 0),
		ContinuousSound:      config.EnvBool("MINIMAX_TTS_CONTINUOUS_SOUND", false),
		VoiceModify:          jsonObjectEnv("MINIMAX_TTS_VOICE_MODIFY"),
		PronunciationTone:    listEnv("MINIMAX_TTS_PRONUNCIATION"),
		TimberWeights:        jsonArrayEnv("MINIMAX_TTS_TIMBER_WEIGHTS"),
		EnglishNormalization: config.EnvBool("MINIMAX_TTS_ENGLISH_NORMALIZATION", false),

		CancelOnAbandon:      config.EnvBool("MINIMAX_TTS_CANCEL_ON_ABANDON", true),
		FlushPartialSegments: config.EnvBool("MINIMAX_TTS_FLUSH_PARTIAL_SEGMENTS", true),
		KeepAliveEvery: time.Duration(
			config.EnvFloat("MINIMAX_TTS_KEEPALIVE_S", 30) * float64(time.Second)),
		warnOnce:       &sync.Once{},
		OpenTimeout:    time.Duration(config.EnvFloat("MINIMAX_TTS_WEBSOCKET_OPEN_TIMEOUT_S", 5) * float64(time.Second)),
		ReceiveTimeout: time.Duration(config.EnvFloat("MINIMAX_TTS_WEBSOCKET_RECEIVE_TIMEOUT_S", 30) * float64(time.Second)),
		MaxIdle:        time.Duration(config.EnvFloat("MINIMAX_TTS_WEBSOCKET_MAX_IDLE_S", 90) * float64(time.Second)),
	}, nil
}

// Name implements provider.TTS.
func (t *TTS) Name() string { return "minimax" }

// Bidi reports whether the configured endpoint is the bidirectional one.
//
// MiniMax ships two WebSocket T2A endpoints with the same auth, the same
// task_start and the same audio frames, and they are easy to mistake for one
// another: /ws/v1/t2a_v2 and /ws/v1/t2a_v2_bidi. Only the second accepts
// task_flush and task_cancel, and only the second documents task_continue at
// arbitrary granularity. On the first, those events come back as 2202 illegal
// event — a per-turn failure that looks like a synthesis bug rather than a
// configuration one, which is precisely why this is detected rather than left
// to a flag someone can set wrongly.
func (t *TTS) Bidi() bool { return strings.HasSuffix(t.Endpoint, "_bidi") }

// bidiOnly reports whether a feature may be used, and says so once if not.
func (t *TTS) bidiOnly(feature string) bool {
	if t.Bidi() {
		return true
	}
	if t.warnOnce == nil {
		t.warnOnce = &sync.Once{}
	}
	t.warnOnce.Do(func() {
		slog.Warn("minimax tts: bidirectional-only features are configured but the endpoint is not the bidirectional one; ignoring them",
			"feature", feature,
			"endpoint", t.Endpoint,
			"hint", "either drop the setting, or set MINIMAX_TTS_WEBSOCKET_ENDPOINT to the "+
				"same host with /ws/v1/t2a_v2_bidi — the plain endpoint is the safer default")
	})
	return false
}

// listEnv reads a comma-separated variable. Used for the pronunciation
// dictionary, whose entries are "written/spoken" pairs.
func listEnv(key string) []string {
	raw := strings.TrimSpace(config.Env(key, ""))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// jsonObjectEnv and jsonArrayEnv read a JSON blob from the environment.
//
// These pass through to the vendor unvalidated on purpose: they are the
// shapes MiniMax documents and extends, and a wrapper struct here would go
// stale the first time a field is added while offering nothing — a typo is
// rejected by the server either way. Malformed JSON is dropped with a warning
// rather than failing startup, since an unparseable expressiveness setting is
// not a reason to refuse to answer the phone.
func jsonObjectEnv(key string) map[string]any {
	raw := strings.TrimSpace(config.Env(key, ""))
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		slog.Warn("minimax tts: ignoring malformed JSON in environment", "key", key, "err", err)
		return nil
	}
	return out
}

func jsonArrayEnv(key string) []map[string]any {
	raw := strings.TrimSpace(config.Env(key, ""))
	if raw == "" {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		slog.Warn("minimax tts: ignoring malformed JSON in environment", "key", key, "err", err)
		return nil
	}
	return out
}

// Open implements provider.TTS.
func (t *TTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	rate := opts.SampleRate
	if rate <= 0 {
		rate = provider.PipelineRate
	}
	voice := opts.Voice
	if voice == "" {
		voice = t.VoiceID
	}
	if voice == "" {
		return nil, fmt.Errorf("minimax tts: no voice configured; set MINIMAX_TTS_VOICE_ID or session.audio.output.voice")
	}
	speed := opts.Speed
	if speed <= 0 {
		speed = t.Speed
	}
	s := &stream{
		tts:   t,
		rate:  rate,
		voice: voice,
		speed: speed,
	}
	if err := s.connect(ctx); err != nil {
		return nil, err
	}
	s.startKeepAlive()
	return s, nil
}

type stream struct {
	tts   *TTS
	rate  int
	voice string
	speed float64

	// busy serializes Synthesize calls; the provider contract says callers do
	// not overlap them, and the MiniMax task is a single ordered stream.
	busy sync.Mutex

	// connMu guards the connection itself. It is deliberately separate from
	// busy so Close can tear down a socket that a synthesis is still blocked
	// reading from, instead of waiting out the 30-second receive timeout.
	connMu  sync.Mutex
	conn    *websocket.Conn
	lastUse time.Time
	closed  bool

	// staleFlushAcks counts task_flushed frames the server still owes for
	// flushes that earlier segments asked for but did not end on.
	//
	// On /ws/v1/t2a_v2_bidi a flushed segment comes back is_final, sentence_end
	// and task_flushed in that order, all in the same millisecond. The reader
	// returns on is_final, so the acknowledgement stays in the socket and the
	// *next* segment's first read finds it — and used to end there, with no
	// audio at all. That is a greeting stopping at the first segment boundary,
	// at the same word every time, which is how this was reported.
	//
	// A counter rather than a drain, because draining means reading with a
	// deadline, and a read timeout on a gorilla connection is terminal: the
	// socket would have to be thrown away after every flushed segment. Counting
	// costs nothing and survives two flushed segments in a row, which the
	// segmenter can produce when a long sentence hits stream_max_chunk_chars.
	//
	// Guarded by busy, which serializes Synthesize and the read that follows
	// it; reset with the connection, since the server owes nothing on a socket
	// it has never seen.
	staleFlushAcks int

	stopKA    chan struct{}
	stopKAOne sync.Once
}

// startKeepAlive runs the idle ping in the background for the life of the
// stream. It lives here rather than on the engine's tick because it is a
// property of this vendor's protocol, not of the conversation.
func (s *stream) startKeepAlive() {
	if s.tts.KeepAliveEvery <= 0 {
		return
	}
	s.stopKA = make(chan struct{})
	go func() {
		// Checking several times per interval so a connection that goes idle
		// just after a tick is not left until the next one.
		t := time.NewTicker(s.tts.KeepAliveEvery / 3)
		defer t.Stop()
		for {
			select {
			case <-s.stopKA:
				return
			case <-t.C:
				s.KeepAlive()
			}
		}
	}()
}

type wsFrame struct {
	Event   string `json:"event"`
	IsFinal bool   `json:"is_final"`
	Data    struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func (s *stream) SampleRate() int { return s.rate }

func (s *stream) taskStart() map[string]any {
	t := s.tts
	voice := map[string]any{
		"voice_id":              s.voice,
		"speed":                 s.speed,
		"vol":                   orDefault(t.Vol, 1.0),
		"pitch":                 t.Pitch,
		"english_normalization": t.EnglishNormalization,
	}
	// Omitted rather than sent empty: MiniMax picks an emotion per sentence
	// when none is given, which is more natural across a whole call than
	// pinning every sentence to one mood.
	if t.Emotion != "" {
		voice["emotion"] = t.Emotion
	}
	if len(t.VoiceModify) > 0 {
		voice["voice_modify"] = t.VoiceModify
	}
	if len(t.TimberWeights) > 0 {
		voice["timber_weights"] = t.TimberWeights
	}

	start := map[string]any{
		"event":          "task_start",
		"model":          t.Model,
		"language_boost": t.LanguageBoost,
		"voice_setting":  voice,
		"audio_setting": map[string]any{
			"sample_rate": s.rate,
			"format":      "pcm",
			"channel":     1,
		},
		// False synthesizes segments concurrently, which is the lower-latency
		// mode and the right default for a conversational turn. See
		// TTS.ContinuousSound for the trade.
		"continuous_sound": t.ContinuousSound,
	}
	if len(t.PronunciationTone) > 0 {
		start["pronunciation_dict"] = map[string]any{"tone": t.PronunciationTone}
	}
	return start
}

// sentenceEnders are the characters MiniMax treats as "synthesize this now".
// Anything else leaves the text in the server's buffer.
const sentenceEnders = "。！？!?.…\n"

func endsSentence(text string) bool {
	text = strings.TrimRight(text, " \t\"'”’）)】」』")
	if text == "" {
		return false
	}
	return strings.ContainsRune(sentenceEnders, []rune(text)[len([]rune(text))-1])
}

func orDefault(v, def float64) float64 {
	if v <= 0 {
		return def
	}
	return v
}

func (s *stream) connect(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: s.tts.OpenTimeout,
		ReadBufferSize:   64 << 10,
		WriteBufferSize:  16 << 10,
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+s.tts.APIKey)

	dialStarted := time.Now()
	conn, resp, err := dialer.DialContext(ctx, s.tts.Endpoint, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("minimax tts: dial failed with HTTP %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("minimax tts: dial failed: %w", err)
	}

	s.setConn(conn)
	for _, step := range []func() error{
		func() error { return s.expect(ctx, "connected_success", s.tts.OpenTimeout) },
		func() error { return s.send(s.taskStart()) },
		func() error { return s.expect(ctx, "task_started", s.tts.OpenTimeout) },
	} {
		if err := step(); err != nil {
			s.dropConn()
			return err
		}
	}
	s.touch()
	slog.Debug("minimax tts: task started",
		"endpoint", s.tts.Endpoint,
		"model", s.tts.Model,
		"voice", s.voice,
		"rate", s.rate,
		"speed", s.speed,
		"ms", time.Since(dialStarted).Milliseconds())
	return nil
}

// Synthesize implements provider.TTSStream. Calls are serialized; the engine
// never overlaps them on one stream.
func (s *stream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		ch := make(chan provider.TTSChunk)
		close(ch)
		return ch, nil
	}

	s.busy.Lock()
	if s.isClosed() {
		s.busy.Unlock()
		return nil, fmt.Errorf("minimax tts: stream is closed")
	}
	if s.needsReconnect() {
		s.dropConn()
		if err := s.connect(ctx); err != nil {
			s.busy.Unlock()
			return nil, err
		}
	}
	if err := s.send(map[string]any{"event": "task_continue", "text": text}); err != nil {
		s.busy.Unlock()
		return nil, err
	}
	// The server buffers text and decides for itself when to synthesize: a
	// segment ending in sentence-ending punctuation goes immediately, one
	// ending in a comma waits for more, and one ending in nothing waits for a
	// length limit or a silence window.
	//
	// That is sensible for a caller streaming a document and wrong for this
	// one. golive deliberately cuts a very short first segment
	// (stream_first_chunk_chars, six characters) precisely to get a syllable
	// out early, and a six-character fragment is exactly what the server holds
	// on to. The whole point of the small segment was being spent waiting for
	// the vendor to decide it had enough. task_flush says we meant it.
	//
	// This deliberately does NOT flush a segment that ends a sentence, on
	// either endpoint, and one theory for why bidi answers broke said it
	// should. That theory was that a bidirectional stream has no per-segment
	// is_final — text keeps arriving, audio keeps coming back — so a
	// sentence-ending segment would have nothing to terminate it and would sit
	// until ReceiveTimeout. It is wrong. `ttsprobe -trace` against a live
	// account ends on is_final in all four cases:
	//
	//   /ws/v1/t2a_v2       flush=false  is_final  420ms  92138 bytes
	//   /ws/v1/t2a_v2       flush=true   is_final  322ms  86564 bytes
	//   /ws/v1/t2a_v2_bidi  flush=false  is_final  410ms  96224 bytes
	//   /ws/v1/t2a_v2_bidi  flush=true   is_final  357ms  93624 bytes
	//
	// Both endpoints finalise every task_continue. So the flush stays what it
	// always was — a way to stop the server sitting on a short unpunctuated
	// fragment — and whatever breaks on the bidirectional endpoint is still
	// unaccounted for.
	//
	// An earlier note here claimed the flush also reached first audio sooner on
	// both endpoints, from one sample each. A second run of the same trace
	// reversed it — 400 ms without against 424 with, and 437 against 461 — so
	// that was noise, and the claim is withdrawn rather than left standing. If
	// the question is worth settling, `ttsprobe -flush both` does paired medians
	// over a text set, which is what it is for.
	flushed := false
	if s.tts.FlushPartialSegments && !endsSentence(text) && s.tts.bidiOnly("task_flush") {
		if err := s.send(map[string]any{"event": "task_flush"}); err != nil {
			s.busy.Unlock()
			return nil, err
		}
		flushed = true
	}

	out := make(chan provider.TTSChunk, 16)
	go func() {
		defer s.busy.Unlock()
		defer close(out)
		s.readAudio(ctx, out, flushed)
	}()
	return out, nil
}

// KeepAlive pings an idle connection so it survives a pause in the
// conversation.
//
// MiniMax sends no pings of its own and closes an idle socket at around 120
// seconds with error 2201. Without this, a caller who stops to think for two
// minutes — or an agent waiting on a slow backend — pays a full reconnect on
// the next thing it says, which is the one moment the reconnect is most
// visible. It pairs with Prewarm: that one makes the connection early, this
// one keeps it.
//
// Like Prewarm, it declines to wait on a synthesis in progress, since a
// connection carrying audio plainly does not need a keepalive.
func (s *stream) KeepAlive() {
	if s.tts.KeepAliveEvery <= 0 {
		return
	}
	if !s.busy.TryLock() {
		return
	}
	defer s.busy.Unlock()
	if s.isClosed() {
		return
	}
	conn := s.currentConn()
	if conn == nil {
		return
	}
	s.connMu.Lock()
	idle := time.Since(s.lastUse)
	s.connMu.Unlock()
	if idle < s.tts.KeepAliveEvery {
		return
	}
	if err := conn.WriteControl(websocket.PingMessage, nil,
		time.Now().Add(2*time.Second)); err != nil {
		slog.Debug("minimax tts: keepalive ping failed; dropping the connection", "err", err)
		s.dropConn()
		return
	}
	// Deliberately touching on the ping: the point is that the socket is not
	// idle, and treating a successful ping as use is what keeps needsReconnect
	// from tearing down a connection this call is actively maintaining.
	s.touch()
}

// Prewarm implements provider.Prewarmer: it re-establishes the socket and
// replays task_start so the next Synthesize is a single frame on a live
// connection.
//
// The dial plus handshake is worth 150–300 ms against MiniMax, and it lands
// squarely between the caller's last syllable and their first heard one — on
// the first turn of a session, and again after any idle long enough for the
// vendor to drop the task. Both are moments the engine can see coming.
//
// TryLock, not Lock: a synthesis in progress is proof the connection is already
// warm, and a prewarm has no business queueing behind real work to discover it.
func (s *stream) Prewarm(ctx context.Context) error {
	if !s.busy.TryLock() {
		return nil
	}
	defer s.busy.Unlock()
	if s.isClosed() {
		return fmt.Errorf("minimax tts: stream is closed")
	}
	if !s.needsReconnect() {
		return nil
	}
	s.dropConn()
	return s.connect(ctx)
}

// readAudio consumes one segment. flushed says whether this segment sent a
// task_flush, which decides whether a task_flushed frame belongs to it.
func (s *stream) readAudio(ctx context.Context, out chan<- provider.TTSChunk, flushed bool) {
	// MiniMax repeats the whole utterance in a trailing status=2 frame. Once
	// incremental status=1 audio has been delivered, replaying it would double
	// every sentence.
	sawIncremental := false

	for {
		if ctx.Err() != nil {
			s.abandon(ctx)
			return
		}
		frame, err := s.recv(s.tts.ReceiveTimeout)
		if err != nil {
			// A read deadline here is not a slow server, it is a segment with
			// nothing to end it: the terminator this loop is waiting for was
			// never going to arrive. That is worth naming, because what the
			// caller hears is an answer that stops partway with a generic
			// read error underneath it.
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				slog.Warn("minimax tts: no is_final and no task_flushed before the read deadline; "+
					"the segment had nothing to terminate it",
					"endpoint", s.tts.Endpoint,
					"bidi", s.tts.Bidi(),
					"timeout", s.tts.ReceiveTimeout,
					"audio_so_far", sawIncremental,
					"hint", "raise MINIMAX_TTS_WEBSOCKET_RECEIVE_TIMEOUT_S if the server is merely slow; "+
						"otherwise try the other endpoint — /ws/v1/t2a_v2 and /ws/v1/t2a_v2_bidi "+
						"end a segment differently")
			}
			s.emit(ctx, out, provider.TTSChunk{Err: err})
			s.dropConn()
			return
		}
		if frame.BaseResp != nil && frame.BaseResp.StatusCode != 0 {
			s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("minimax tts: status %d: %s",
				frame.BaseResp.StatusCode, frame.BaseResp.StatusMsg)})
			s.dropConn()
			return
		}
		if frame.Event == "task_failed" {
			s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("minimax tts: task failed")})
			s.dropConn()
			return
		}

		if frame.Data.Audio != "" {
			if frame.Data.Status == 2 && sawIncremental {
				// Aggregate duplicate of audio already sent.
			} else {
				pcm, err := hex.DecodeString(frame.Data.Audio)
				if err != nil {
					s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("minimax tts: audio is not hex: %w", err)})
					s.dropConn()
					return
				}
				if frame.Data.Status != 2 {
					sawIncremental = true
				}
				if !s.emit(ctx, out, provider.TTSChunk{PCM: pcm}) {
					s.abandon(ctx)
					return
				}
			}
		}

		// A segment ends on is_final, and also on task_flushed when we asked
		// for the buffer to be emitted early. Both leave the task open for the
		// next task_continue; only task_finish ends it.
		//
		// task_flushed counts only for the segment that asked for it. A
		// terminator has to belong to the segment being read, and this one can
		// outlive it: a flushed segment that reaches is_final first returns
		// there and leaves the acknowledgement in the socket, where the *next*
		// segment's first read finds it and ends immediately with no audio.
		//
		// That is the shape of the fault reported on /ws/v1/t2a_v2_bidi. golive
		// cuts a short unpunctuated opening fragment and flushes it; everything
		// after ends a sentence and does not flush. So the fragment plays, the
		// next segment inherits the stale acknowledgement and is silent, and the
		// greeting stops at the first segment boundary — the same word every
		// time. The plain endpoint never sends a flush at all (bidiOnly gates
		// it), which is why the same call is fine there.
		if frame.Event == "task_flushed" {
			// The acknowledgement for a flush an *earlier* segment sent, which
			// that segment did not end on. Account for it and keep reading.
			if s.staleFlushAcks > 0 {
				s.staleFlushAcks--
				slog.Debug("minimax tts: consumed a flush acknowledgement left by an earlier segment",
					"endpoint", s.tts.Endpoint, "still_outstanding", s.staleFlushAcks)
				continue
			}
			if !flushed {
				slog.Debug("minimax tts: ignoring a task_flushed nothing asked for",
					"endpoint", s.tts.Endpoint)
				continue
			}
			s.touch()
			return
		}
		if frame.IsFinal {
			// This segment asked for a flush and finished before the
			// acknowledgement arrived, so the acknowledgement is still coming.
			// Remember that, or the next segment will end on it with no audio.
			if flushed {
				s.staleFlushAcks++
			}
			s.touch()
			return
		}
	}
}

// abandon returns an interrupted task's connection to a usable state.
//
// Cancelling is strictly better than draining when the vendor supports it, so
// it is tried first and the drain remains as the fallback. See cancelTask and
// resync for why each exists.
func (s *stream) abandon(ctx context.Context) {
	if s.tts.CancelOnAbandon && s.tts.bidiOnly("task_cancel") && s.cancelTask() {
		return
	}
	s.resync(ctx)
}

// cancelTask asks MiniMax to stop generating, and reports whether the
// connection came back in a known state.
//
// This is what the drain below should have been. task_cancel tells the vendor
// to stop producing audio for text it has already been given and returns the
// session to its post-task_start state, so the cost of a barge-in is one round
// trip rather than however much audio was already queued — which, on a long
// sentence cut early, can be seconds. It also removes the reason the drain
// sometimes gave up and reconnected.
//
// Frames already in flight still have to be read and discarded: the cancel and
// the audio cross on the wire. The loop ends on task_canceled, or on is_final
// if the task happened to finish before the cancel landed, and both leave the
// connection reusable.
func (s *stream) cancelTask() bool {
	if s.currentConn() == nil {
		return false
	}
	started := time.Now()
	if err := s.send(map[string]any{"event": "task_cancel"}); err != nil {
		slog.Debug("minimax tts: task_cancel could not be sent; reconnecting", "err", err)
		s.dropConn()
		return true // handled: the connection is gone, which is a known state
	}

	discarded := 0
	deadline := started.Add(1500 * time.Millisecond)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		frame, err := s.recv(remaining)
		if err != nil {
			slog.Debug("minimax tts: cancel not acknowledged; reconnecting",
				"err", err, "discarded_frames", discarded)
			s.dropConn()
			return true
		}
		discarded++
		if frame.BaseResp != nil && frame.BaseResp.StatusCode != 0 {
			s.dropConn()
			return true
		}
		if frame.Event == "task_canceled" || frame.Event == "task_failed" || frame.IsFinal {
			slog.Debug("minimax tts: cancelled an abandoned task",
				"event", frame.Event, "discarded_frames", discarded,
				"ms", time.Since(started).Milliseconds())
			s.touch()
			return true
		}
	}
	// No acknowledgement inside the window. Fall back rather than guess: the
	// one outcome that must not happen is reusing a stream whose state we are
	// not sure of.
	slog.Debug("minimax tts: cancel timed out; falling back to draining",
		"discarded_frames", discarded)
	return false
}

// resync consumes the rest of an abandoned task so the connection can be
// reused.
//
// This is the fallback for a server that does not honour task_cancel, and the
// original mechanism. It is kept because its failure mode is well understood
// and because the bug it fixes is nasty enough to be worth two answers.
//
// This is the difference between a barge-in that works and one that appears to
// work. The task is a single ordered stream: when the caller stops reading
// mid-sentence, MiniMax keeps generating audio for text it has already been
// given, and those frames stay in the socket. Reuse the connection without
// draining them and the *next* task_continue reads the tail of the interrupted
// sentence first — so the answer nobody wanted plays ahead of the answer to
// what was just asked, with nothing in the server's own logs to show for it.
//
// The drain is bounded. A task that will not finish promptly is not worth
// waiting for, so the connection is dropped and the next synthesis reconnects:
// a reconnect costs a few hundred milliseconds once, while a desynchronized
// stream is wrong on every turn that follows.
func (s *stream) resync(ctx context.Context) {
	conn := s.currentConn()
	if conn == nil {
		return
	}
	started := time.Now()
	frames := 0
	deadline := started.Add(2 * time.Second)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		frame, err := s.recv(remaining)
		if err != nil {
			slog.Debug("minimax tts: resync failed; reconnecting", "err", err, "frames", frames)
			s.dropConn()
			return
		}
		frames++
		if frame.Event == "task_failed" ||
			(frame.BaseResp != nil && frame.BaseResp.StatusCode != 0) {
			s.dropConn()
			return
		}
		if frame.IsFinal {
			// Fully drained: the connection is back in a known state and the
			// next sentence starts clean.
			slog.Debug("minimax tts: resynced an abandoned task",
				"discarded_frames", frames, "ms", time.Since(started).Milliseconds())
			s.touch()
			return
		}
	}
	slog.Warn("minimax tts: abandoned task did not drain in time; reconnecting",
		"frames", frames, "ms", time.Since(started).Milliseconds())
	s.dropConn()
}

// emit hands one chunk to the caller, and reports whether it was taken.
//
// The bounded wait is defensive, and earned. The contract says a caller who
// stops reading must cancel the context, and a caller that does neither used to
// block this goroutine forever — with the lock that serializes synthesis still
// held, so every later segment on this connection deadlocked and the session
// produced nothing but fragments from then on. A provider should not turn a
// caller's mistake into a permanent outage of itself.
//
// The timeout is generous because a slow consumer is normal: a paced player
// legitimately takes real time to drain audio. This is meant to catch a
// consumer that has gone away entirely, and it degrades to what a cancellation
// would have done — abandon the task, resynchronize, carry on.
func (s *stream) emit(ctx context.Context, out chan<- provider.TTSChunk, chunk provider.TTSChunk) bool {
	stall := s.tts.ReceiveTimeout
	if stall <= 0 {
		stall = 30 * time.Second
	}
	timer := time.NewTimer(stall)
	defer timer.Stop()
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		slog.Warn("minimax tts: the caller stopped reading without cancelling; abandoning the segment",
			"stall", stall)
		return false
	}
}

func (s *stream) send(payload map[string]any) error {
	conn := s.currentConn()
	if conn == nil {
		return fmt.Errorf("minimax tts: not connected")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("minimax tts: write: %w", err)
	}
	return nil
}

func (s *stream) recv(timeout time.Duration) (*wsFrame, error) {
	conn := s.currentConn()
	if conn == nil {
		return nil, fmt.Errorf("minimax tts: not connected")
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("minimax tts: read: %w", err)
	}
	var frame wsFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, fmt.Errorf("minimax tts: malformed frame: %w", err)
	}
	return &frame, nil
}

func (s *stream) expect(ctx context.Context, event string, timeout time.Duration) error {
	// Informational frames are skipped rather than treated as a protocol
	// error, because the server sends ones this adapter was not written for.
	//
	// A live trace of /ws/v1/t2a_v2_bidi shows sentence_start arriving in the
	// same millisecond as task_started. It happened to arrive second, so the
	// handshake survived — but nothing guarantees that ordering, and the other
	// way round this returned `expected "task_started" but got
	// "sentence_start"` and failed the connection outright. A frame that
	// carries no error and is not the one being waited for is not grounds to
	// give up on the socket.
	//
	// An error frame still fails immediately, and the read deadline still
	// bounds the wait, so an endpoint that never sends the awaited event
	// cannot spin here.
	for {
		frame, err := s.recv(timeout)
		if err != nil {
			return err
		}
		if frame.BaseResp != nil && frame.BaseResp.StatusCode != 0 {
			return fmt.Errorf("minimax tts: %s failed with status %d: %s",
				event, frame.BaseResp.StatusCode, frame.BaseResp.StatusMsg)
		}
		if frame.Event == event {
			return nil
		}
		if frame.Event == "task_failed" {
			return fmt.Errorf("minimax tts: %s failed: task_failed", event)
		}
		slog.Debug("minimax tts: skipping a frame while waiting for another",
			"waiting_for", event, "got", frame.Event)
	}
}

func (s *stream) dropConn() {
	// The server owes nothing on a socket it has never seen.
	s.staleFlushAcks = 0
	s.connMu.Lock()
	conn := s.conn
	s.conn = nil
	s.connMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (s *stream) currentConn() *websocket.Conn {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn
}

func (s *stream) setConn(conn *websocket.Conn) {
	s.connMu.Lock()
	s.conn = conn
	s.lastUse = time.Now()
	s.connMu.Unlock()
}

func (s *stream) touch() {
	s.connMu.Lock()
	s.lastUse = time.Now()
	s.connMu.Unlock()
}

func (s *stream) isClosed() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.closed
}

// needsReconnect reports a missing socket, or one idle past the server's
// window. A task left idle that long is dead but still looks open, and
// discovering that mid-sentence costs a turn.
func (s *stream) needsReconnect() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn == nil {
		return true
	}
	return s.tts.MaxIdle > 0 && time.Since(s.lastUse) > s.tts.MaxIdle
}

// Close ends the task and the connection. It never waits on an in-flight
// synthesis: closing the socket is what unblocks one.
func (s *stream) Close() error {
	if s.stopKA != nil {
		s.stopKAOne.Do(func() { close(s.stopKA) })
	}
	s.connMu.Lock()
	if s.closed {
		s.connMu.Unlock()
		return nil
	}
	s.closed = true
	conn := s.conn
	s.conn = nil
	s.connMu.Unlock()

	if conn == nil {
		return nil
	}
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if data, err := json.Marshal(map[string]any{"event": "task_finish"}); err == nil {
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}
	return conn.Close()
}
