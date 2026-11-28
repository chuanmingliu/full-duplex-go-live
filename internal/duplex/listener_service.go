package duplex

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

// Only the engine loop mutates a candidate. Workers post results tagged with
// its identity, so a late model delta or TTS frame cannot revive an old turn.
type listenerCandidate struct {
	id, hypothesis, text              string
	cancel                            context.CancelFunc
	expires, end                      time.Time
	pcm                               []byte
	ready, released, played, finished bool
}

const listenerFrame = 20 * time.Millisecond

func (e *Engine) loadListenerConfig() {
	base := e.opts.Cfg.ToolsBaseURL
	if base == "" {
		return
	}
	token := e.toolsToken()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		cfg, err := tools.FetchBackchannelService(base, token, 2*time.Second)
		if err != nil {
			e.log.Debug("backchannel config unavailable", "err", err)
			return
		}
		e.post(func() { e.listenerConfig = cfg })
	}()
}

func (e *Engine) listenerEvent(c *listenerCandidate, stage, text, reason string, at time.Time) {
	event := live.BackchannelStageEvent{
		Envelope: live.Envelope{Type: live.ExtBackchannelStage},
		ItemID:   c.id, Stage: stage, AtMS: at.Sub(e.startAt).Milliseconds(),
		Text: text, Reason: reason, ConfigVersion: e.listenerConfig.Version,
	}
	e.log.Debug("backchannel stage", "candidate", c.id, "stage", stage, "at_ms", event.AtMS, "text", text, "reason", reason, "config_version", event.ConfigVersion)
	e.deps.Emit(event)
}

func (e *Engine) stopListener(reason string) {
	c := e.listener
	if c == nil {
		return
	}
	c.cancel()
	if e.listenerTimer != nil {
		e.listenerTimer.Stop()
	}
	e.listenerClock = nil
	e.listener = nil
	if c.played {
		e.listenerEvent(c, "playback_end", "", reason, c.end)
	} else {
		e.listenerEvent(c, "skipped", "", reason, time.Now())
	}
}

func (e *Engine) listenerUnavailable(c *listenerCandidate) string {
	if !e.userOpen || e.muted {
		return "caller_finished"
	}
	if e.speaking.Load() {
		return "main_reply"
	}
	if e.asrRun == nil || !strings.HasPrefix(NormalizePhrase(pauseWords(e.asrRun)), c.hypothesis) {
		return "transcript_revised"
	}
	if !c.released && time.Now().After(c.expires) {
		return "expired"
	}
	return ""
}

func (e *Engine) prepareListener() {
	if c := e.listener; c != nil {
		if reason := e.listenerUnavailable(c); reason != "" {
			e.stopListener(reason)
		}
		return
	}
	if !e.userOpen || e.muted || e.speaking.Load() || e.asrRun == nil || e.deps.LLM == nil || e.deps.TTS == nil || time.Now().Before(e.listenerNext) {
		return
	}
	words := strings.TrimSpace(pauseWords(e.asrRun))
	if utf8.RuneCountInString(words) < 4 {
		return
	}
	ctx, cancel := context.WithTimeout(e.ctx, 8*time.Second)
	e.bcSeq++
	c := &listenerCandidate{id: fmt.Sprintf("bc_ai_%d", e.bcSeq), hypothesis: NormalizePhrase(words), cancel: cancel, expires: time.Now().Add(5 * time.Second)}
	e.listener = c
	e.listenerNext = time.Now().Add(time.Duration(e.opts.Cfg.Duplex.BackchannelEveryMS) * time.Millisecond)
	messages := []provider.Message{{Role: provider.RoleSystem, Content: e.listenerConfig.Prompt}}
	for _, m := range e.history[max(0, len(e.history)-8):] {
		if (m.Role == provider.RoleUser || m.Role == provider.RoleAssistant) && m.Content != "" {
			messages = append(messages, provider.Message{Role: m.Role, Content: m.Content})
		}
	}
	messages = append(messages, provider.Message{Role: provider.RoleUser, Content: words})
	req := provider.LLMRequest{Model: e.opts.Cfg.BackendModel, Messages: messages, MaxTokens: 32, DisableThinking: true}
	e.listenerEvent(c, "request_started", words, "", time.Now())
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.generateListener(ctx, c, req) }()
}

func (e *Engine) generateListener(ctx context.Context, c *listenerCandidate, req provider.LLMRequest) {
	post := func(f func()) {
		e.post(func() {
			if e.listener == c {
				f()
			}
		})
	}
	fail := func(reason string) { post(func() { e.stopListener(reason) }) }
	modelCtx, stopModel := context.WithCancel(ctx)
	defer stopModel()
	deltas, err := e.deps.LLM.Stream(modelCtx, req)
	if err != nil {
		fail("model_error")
		return
	}
	var words strings.Builder
readWords:
	for {
		select {
		case <-ctx.Done():
			fail("expired")
			return
		case d, ok := <-deltas:
			if !ok {
				break readWords
			}
			if d.Err != nil || d.ToolCall != nil {
				fail("model_error")
				return
			}
			if d.Text == "" {
				continue
			}
			unit, _, complete := strings.Cut(d.Text, "\n")
			words.WriteString(unit)
			if utf8.RuneCountInString(strings.TrimSpace(words.String())) > 16 {
				fail("too_long")
				return
			}
			text := words.String()
			at := time.Now()
			post(func() { e.listenerEvent(c, "words_delta", text, "", at) })
			if complete || strings.HasSuffix(strings.TrimSpace(text), "。") || strings.HasSuffix(strings.TrimSpace(text), ".") {
				break readWords
			}
		}
	}
	stopModel()
	text := strings.TrimSpace(words.String())
	if text == "" || strings.EqualFold(strings.Trim(text, ".。"), "SILENCE") {
		fail("model_silence")
		return
	}
	at := time.Now()
	post(func() { c.text = text; e.listenerEvent(c, "words_ready", text, "", at) })
	var chunks <-chan provider.TTSChunk
	var rate int
	opts := e.synthesisOptions()
	if f := e.deps.ListenerPool; f != nil {
		stream := f.New(ctx, opts)
		defer stream.Close()
		chunks, _, err = stream.TrySynthesize(ctx, text)
		rate = stream.SampleRate()
	} else {
		tts := e.deps.ListenerTTS
		if tts == nil {
			tts = e.deps.TTS
		}
		stream, openErr := tts.Open(ctx, opts)
		if openErr != nil {
			fail("tts_unavailable")
			return
		}
		defer stream.Close()
		chunks, err = stream.Synthesize(ctx, text)
		rate = stream.SampleRate()
	}
	if err != nil || chunks == nil {
		fail("tts_unavailable")
		return
	}
	total := 0
	for {
		select {
		case <-ctx.Done():
			fail("expired")
			return
		case chunk, ok := <-chunks:
			if !ok {
				if total == 0 {
					fail("empty_audio")
				} else {
					post(func() { c.finished = true })
				}
				return
			}
			if chunk.Err != nil {
				fail("tts_error")
				return
			}
			if len(chunk.PCM) == 0 {
				continue
			}
			pcm, err := audio.ResamplePCM16(chunk.PCM, rate, e.opts.ClientRate)
			if err != nil {
				fail("tts_error")
				return
			}
			total += len(pcm)
			if total > audio.PCM16(e.opts.ClientRate).BytesForMS(2500) {
				fail("audio_too_long")
				return
			}
			frame := append([]byte(nil), pcm...)
			at := time.Now()
			post(func() {
				c.pcm = append(c.pcm, frame...)
				if !c.ready {
					c.ready = true
					e.listenerEvent(c, "audio_ready", "", "", at)
				}
				e.releaseListener()
			})
		}
	}
}

// A short pause inside an open utterance is the release window. A prepared
// candidate may span several such pauses, but never survives the turn ending.
func (e *Engine) releaseListener() {
	c := e.listener
	if c == nil || c.released {
		return
	}
	if reason := e.listenerUnavailable(c); reason != "" {
		e.stopListener(reason)
		return
	}
	pause := e.vad.TrailingSilenceMS()
	if !c.ready || pause < 60 || pause > 220 || time.Since(e.userSince) < time.Duration(e.opts.Cfg.Duplex.BackchannelAfterMS)*time.Millisecond {
		return
	}
	c.released = true
	e.listenerEvent(c, "released", "", "", time.Now())
	e.playListenerFrame()
}

// The side track has its own buffer and clock. Admit at most one 20 ms frame
// at a time to the shared output sink. No side-track audio enters the answer
// player's queue, toggles its speaking flag, or raises the input VAD threshold.
func (e *Engine) playListenerFrame() {
	e.listenerClock = nil
	c := e.listener
	if c == nil {
		return
	}
	if reason := e.listenerUnavailable(c); reason != "" {
		e.stopListener(reason)
		return
	}
	if len(c.pcm) == 0 && c.finished {
		e.stopListener("completed")
		return
	}
	if len(c.pcm) > 0 {
		e.outputMu.Lock()
		if e.ctx.Err() != nil || e.player.Active() {
			e.outputMu.Unlock()
			e.stopListener("main_playback")
			return
		}
		n := min(len(c.pcm), audio.PCM16(e.opts.ClientRate).BytesForMS(20))
		now := time.Now()
		if !c.played {
			c.played = true
			e.listenerEvent(c, "playback_start", c.text, "", now)
			e.onSegmentHeard(c.id, c.text, now)
			e.deps.Emit(live.BackchannelEvent{Envelope: live.Envelope{Type: live.ExtBackchannel}, Kind: live.BackchannelAck, Text: c.text})
			e.listenerNext = now.Add(time.Duration(e.opts.Cfg.Duplex.BackchannelEveryMS) * time.Millisecond)
		}
		e.emitPCM(c.id, c.pcm[:n], now)
		c.end = now.Add(time.Duration(n) * time.Second / time.Duration(2*e.opts.ClientRate))
		c.pcm = c.pcm[n:]
		e.outputMu.Unlock()
	}
	delay := listenerFrame
	if wait := time.Until(c.end); wait > 0 {
		delay = wait
	}
	if e.listenerTimer == nil {
		e.listenerTimer = time.NewTimer(delay)
	} else {
		e.listenerTimer.Reset(delay)
	}
	e.listenerClock = e.listenerTimer.C
}
