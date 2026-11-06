package minimax

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
)

// AccountKey deliberately follows the credential, not its position in a pool
// or the choice of v2/bidi endpoint (both consume the same account allowance).
// It is used only as an opaque process-local admission key, never logged.
func (t *TTS) AccountKey(n int) string {
	keys := t.keys()
	if n < 0 || n >= len(keys) {
		return ""
	}
	sum := sha256.Sum256([]byte("minimax\x00" + keys[n]))
	return hex.EncodeToString(sum[:])
}

// OpenBackground pins one account and does exactly one connection attempt.
// The returned stream cannot reconnect, prewarm or fail over. Unlike foreground
// connections it is closed directly on cancellation, without a task_finish write.
func (t *TTS) OpenBackground(ctx context.Context, opts provider.TTSOptions, n int) (provider.IdentifiedTTSStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys := t.keys()
	if n < 0 || n >= len(keys) {
		return nil, fmt.Errorf("minimax tts: invalid background account")
	}
	if t.rests.resting(n, time.Now()) {
		return nil, fmt.Errorf("minimax tts: background account resting")
	}
	voice := t.voiceFor(n, opts.Voice)
	if voice == "" {
		return nil, fmt.Errorf("minimax tts: background voice missing")
	}
	speed := opts.Speed
	if speed <= 0 {
		speed = t.Speed
	}
	s := &stream{tts: t, rate: supportedRate(opts.SampleRate), speed: speed, voice: voice, key: keys[n], account: n, background: true}
	// Snapshot the effective task settings. Foreground and background read from
	// the same provider instance; this stream's identity cannot change later.
	start := s.taskStart()
	raw, err := json.Marshal(struct {
		Endpoint string
		Start    map[string]any
		Flush    bool
	}{t.Endpoint, start, t.FlushPartialSegments})
	if err != nil {
		return nil, fmt.Errorf("minimax tts: invalid background settings: %w", err)
	}
	sum := sha256.Sum256(raw)
	s.identity = provider.AudioIdentity{Provider: t.Name(), Account: t.AccountKey(n), Model: t.Model, Voice: voice, Language: t.LanguageBoost, Speed: speed, Rate: s.rate, Settings: hex.EncodeToString(sum[:])}
	s.backgroundStart = start
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	err = s.connect(ctx)
	stop()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = s.Close()
		t.rest(n, err)
		return nil, err
	}
	return s, nil
}

func (s *stream) EffectiveIdentity() provider.AudioIdentity { return s.identity }

var _ provider.BackgroundAccountTTS = (*TTS)(nil)
