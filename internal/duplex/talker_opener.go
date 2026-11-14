package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chuanmingliu/golive/internal/audio"
)

// Optional preparation never owns the floor or delays the normal opener.
// Everything mutable here belongs to the engine loop; the worker returns one
// immutable result through a buffered channel. No generated text/audio enters
// the global phrase cache or another call's context.
type talkerOpener struct {
	attempt   int
	text      string
	utterance int64
	cancel    context.CancelFunc
	ready     chan talkerOpenerResult
}
type talkerOpenerResult struct {
	phrase           string
	audio            PhraseAudio
	status           string
	modelMS, totalMS int64
}

const talkerOpenerSystem = "你是电话助手的开场词生成器。只输出与用户话题相关的短开场词，以逗号结尾。选取用户原话中的名词话题，最多三个字，然后加“呢，”或“嘛，”。不要回答问题，不要提供事实、金额、承诺或建议。例：用户问保费多少钱，输出保费呢，；用户问车损险包含什么，输出车损险呢，。"
const talkerOpenerGrammar = "root ::= topic (\"呢，\" | \"嘛，\")\ntopic ::= [一-鿿]{1,3}"

// A closed neutral grammar and an exact topic span prevent the small model
// from making a business claim. This is conservative, not a semantic verifier:
// unsupported languages, malformed/ungrounded phrases use the saved presets.
func validTalkerOpener(phrase, caller string) bool {
	topic := ""
	for _, suffix := range []string{"呢，", "嘛，"} {
		if strings.HasSuffix(phrase, suffix) {
			topic = strings.TrimSuffix(phrase, suffix)
			break
		}
	}
	if utf8.RuneCountInString(topic) < 2 || utf8.RuneCountInString(topic) > 3 || !strings.Contains(caller, topic) {
		return false
	}
	for _, r := range topic {
		if !unicode.Is(unicode.Han, r) || strings.ContainsRune("零一二两三四五六七八九十百千万亿", r) {
			return false
		}
	}
	// Even if quoted by the caller, these aren't neutral topics.
	for _, word := range []string{"可以", "保证", "一定", "赔付", "免费", "是的", "不是", "不用", "不要", "取消", "挂断"} {
		if strings.Contains(topic, word) {
			return false
		}
	}
	return true
}

func requestTalkerOpener(ctx context.Context, endpoint, caller string) (string, error) {
	if utf8.RuneCountInString(caller) > 256 || strings.Contains(caller, "<|") || strings.Contains(caller, "|>") {
		return "", errors.New("input")
	}
	prompt := "<|im_start|>system\n" + talkerOpenerSystem + "<|im_end|>\n<|im_start|>user\n" + caller + "<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
	body, _ := json.Marshal(map[string]any{"prompt": prompt, "grammar": talkerOpenerGrammar, "n_predict": 20, "temperature": 0, "seed": 0, "stream": false, "stop": []string{"<|im_end|>"}, "cache_prompt": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/completion", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("request")
	}
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return "", errors.New("unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("http")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16385))
	if err != nil || len(raw) > 16384 {
		return "", errors.New("response")
	}
	var result struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return "", errors.New("response")
	}
	phrase := strings.TrimSpace(result.Content)
	if !validTalkerOpener(phrase, caller) {
		return "", errors.New("rejected")
	}
	return phrase, nil
}

func (e *Engine) talkerEnabled() bool {
	return e.opts.Cfg.Duplex.Opener && e.opts.Cfg.Duplex.TalkerOpenerURL != "" && !e.clientMode()
}

// Called only after the existing caller/echo/backchannel gates. A revised
// hypothesis cancels its predecessor immediately. At most one current attempt,
// 8 per utterance and 64 per call; changes faster than 250 ms do not retry.
func (e *Engine) prepareTalkerOpener(text string) {
	if !e.talkerEnabled() || e.hold != nil || e.ignoring() || e.muted || e.hangingUp() {
		return
	}
	if p := e.talkerPending; p != nil {
		if p.utterance == e.utterStartMS && sameWords(p.text, text) {
			return
		}
		p.cancel()
		e.talkerPending = nil
	}
	if e.talkerUtterance != e.utterStartMS {
		e.talkerUtterance = e.utterStartMS
		e.talkerUtteranceAttempts = 0
	}
	if utf8.RuneCountInString(text) < 4 || utf8.RuneCountInString(text) > 256 || e.talkerAttempts >= 64 || e.talkerUtteranceAttempts >= 8 || time.Since(e.talkerStarted) < 250*time.Millisecond {
		return
	}
	e.talkerAttempts++
	e.talkerUtteranceAttempts++
	e.talkerStarted = time.Now()
	timeout := e.opts.Cfg.Duplex.TalkerOpenerTimeoutMS
	if timeout == 0 {
		timeout = 800
	}
	ctx, cancel := context.WithTimeout(e.ctx, time.Duration(timeout)*time.Millisecond)
	p := &talkerOpener{attempt: e.talkerAttempts, text: text, utterance: e.utterStartMS, cancel: cancel, ready: make(chan talkerOpenerResult, 1)}
	e.talkerPending = p
	endpoint := e.opts.Cfg.Duplex.TalkerOpenerURL
	opts := e.synthesisOptions()
	factory := e.deps.BackgroundTTS
	// Snapshot identity and requested settings on the loop, never read history
	// or mutable turn state from the worker.
	key := e.phraseKey("")
	rate := e.opts.ClientRate
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		started := time.Now()
		result := talkerOpenerResult{status: "unavailable"}
		defer func() {
			result.totalMS = time.Since(started).Milliseconds()
			e.log.Debug("opener: preparation", "attempt", p.attempt, "status", result.status, "model_ms", result.modelMS, "prepare_ms", result.totalMS)
			p.ready <- result
		}()
		phrase, err := requestTalkerOpener(ctx, endpoint, text)
		result.modelMS = time.Since(started).Milliseconds()
		if err != nil {
			result.status = err.Error()
			return
		}
		result.phrase = phrase
		key.Text = phrase
		prepared, ok := Phrases().Get(key)
		if !ok {
			if factory == nil {
				result.status = "tts_unsupported"
				return
			}
			speech := factory.New(ctx, opts)
			defer speech.Close()
			audio, _, err := prepareQuoteAudio(ctx, speech, phrase)
			if err != nil {
				result.status = "tts_unavailable"
				return
			}
			prepared = audio.audio
		}
		pcm, err := audio.ResamplePCM16(prepared.PCM, prepared.Rate, rate)
		if err != nil || len(pcm) == 0 {
			result.status = "audio_invalid"
			return
		}
		pcm = audio.TrimTrailingSilence(pcm, rate, -40, openerTailMS)
		pcm = audio.TrimLeadingSilence(pcm, rate, -40, openerLeadMS)
		// Never truncate a newly generated word just to fit the latency budget.
		if len(pcm) > audio.PCM16(rate).BytesForMS(openerMaxMS) {
			result.status = "audio_long"
			return
		}
		if ctx.Err() != nil {
			result.status = "expired"
			return
		}
		result.audio = PhraseAudio{PCM: pcm, Rate: rate}
		result.status = "ready"
	}()
}

// A final-only turn reaches sayOpener before beginGeneration. Attach the
// already-prepared hypothesis there too; never start work after an opener spoke.
func (e *Engine) attachTalkerOpener(turn *Turn) {
	if turn == nil || turn.Scripted || turn.Opened != "" || turn.talkerOpener != nil || !e.talkerEnabled() {
		return
	}
	if p := e.talkerPending; p != nil && p.utterance == e.utterStartMS && sameWords(p.text, turn.Transcript) {
		turn.talkerOpener = p
	}
}

// takeTalkerOpener is a nonblocking choice at the original playback boundary.
// The attached attempt is specific to this exact transcript and utterance.
func (e *Engine) takeTalkerOpener(turn *Turn, fragment bool) (string, PhraseAudio, bool) {
	p := turn.talkerOpener
	if p == nil {
		return "", PhraseAudio{}, false
	}
	defer p.cancel()
	status := "not_ready"
	var r talkerOpenerResult
	select {
	case r = <-p.ready:
		status = r.status
	default:
	}
	if fragment {
		status = "fragment"
	}
	if !sameWords(p.text, turn.Transcript) || p.utterance != e.utterStartMS || !e.gen.Valid(turn.Generation) {
		status = "stale"
	}
	if r.phrase != "" && r.phrase == e.lastSaidOpener {
		status = "repeat"
	}
	used := status == "ready"
	source := "preset"
	if used {
		source = "talker"
	}
	e.log.Debug("opener: selection", "attempt", p.attempt, "turn", turn.ID, "revision", turn.Revision, "source", source, "reason", status, "model_ms", r.modelMS, "prepare_ms", r.totalMS)
	turn.talkerOpener = nil
	if !used {
		return "", PhraseAudio{}, false
	}
	return r.phrase, r.audio, true
}
