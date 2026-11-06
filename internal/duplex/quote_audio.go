package duplex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/segment"
	"github.com/chuanmingliu/golive/internal/tools"
)

const quoteAudioResponseBytes = 256 << 10

func (e *Engine) segmentConfig() segment.Config {
	d := e.opts.Cfg.Duplex
	return segment.Config{FirstChunkChars: d.StreamFirstChunkChars, MinChunkChars: d.StreamMinChunkChars, MaxChunkChars: d.StreamMaxChunkChars}
}

func (e *Engine) synthesisOptions() provider.TTSOptions {
	return provider.TTSOptions{SampleRate: e.ttsRate(), Voice: e.opts.Voice, Speed: e.opts.Cfg.Speed, Language: e.opts.Language}
}

func firstQuoteSegment(text string, cfg segment.Config) string {
	seg := segment.New(cfg)
	parts := seg.Push(text)
	first := ""
	if len(parts) > 0 {
		first = parts[0]
	} else {
		first = seg.FlushFirst()
	}
	first = strings.TrimSpace(first)
	if utf8.RuneCountInString(first) > 120 || !speakable(first) {
		return ""
	}
	return first
}

func (e *Engine) startQuoteAudio() {
	max := e.opts.Cfg.Duplex.QuoteReadoutWarmMax
	if max <= 0 || max > 8 || e.deps.BackgroundTTS == nil || e.tools == nil || e.opts.CallID == "" {
		return
	}
	tool, ok := e.tools.byName["quote_total"]
	if !ok || tool.Speech == nil {
		return
	}
	token := e.toolsToken()
	if token == "" || e.opts.Cfg.ToolsBaseURL == "" {
		return
	}
	endpoint := strings.TrimRight(e.opts.Cfg.ToolsBaseURL, "/") + "/api/quote/ready?call=" + url.QueryEscape(e.opts.CallID)
	speech := e.deps.BackgroundTTS.New(e.ctx, e.synthesisOptions())
	if speech == nil {
		return
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q := &quoteAudioCache{max: max, interval: 2 * time.Second, initialDelay: 2 * time.Second,
		closeSpeech: func() { _ = speech.Close() },
		fetch: func(ctx context.Context) ([]quoteAudioResult, error) {
			return fetchReadyQuoteAudio(ctx, client, endpoint, token, e.opts.CallID)
		},
		render: func(raw []byte) string { return firstQuoteSegment(tool.ReadoutFor(raw), e.segmentConfig()) },
		prepare: func(ctx context.Context, text string) (preparedQuoteAudio, bool, error) {
			started := time.Now()
			prepared, attempted, err := prepareQuoteAudio(ctx, speech, text)
			status := "ready"
			if err != nil {
				status = "failed"
			}
			if errors.Is(err, provider.ErrBackgroundUnavailable) {
				status = "capacity"
			}
			if errors.Is(err, errQuoteAudioRetired) {
				status = "retired"
			}
			e.log.Debug("quote audio: preparation", "status", status, "attempted", attempted,
				"chars", len([]rune(text)), "bytes", len(prepared.audio.PCM), "ms", time.Since(started).Milliseconds())
			return prepared, attempted, err
		},
	}
	q.start(e.ctx)
	e.quoteAudio = q
	e.log.Info("quote audio: bounded preparation enabled", "max_attempts", max)
}

func fetchReadyQuoteAudio(ctx context.Context, client *http.Client, endpoint, token, call string) ([]quoteAudioResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("quote snapshot request invalid")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("quote snapshot unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quote snapshot HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, quoteAudioResponseBytes+1))
	if err != nil || len(raw) > quoteAudioResponseBytes {
		return nil, errors.New("quote snapshot exceeds limit or is unreadable")
	}
	var snapshot tools.QuoteReadySnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Call != call || len(snapshot.Quotes) > 32 {
		return nil, errors.New("quote snapshot identity or shape differs")
	}
	rows := make([]quoteAudioResult, 0, len(snapshot.Quotes))
	for _, q := range snapshot.Quotes {
		if q.ID == "" || len(q.ID) > 128 || q.ExpiresAt.IsZero() || !time.Now().Before(q.ExpiresAt) || tools.ResultStatus(q.Result) != "quote" {
			continue
		}
		rows = append(rows, quoteAudioResult{expires: q.ExpiresAt, raw: q.Result})
	}
	return rows, nil
}

type quoteBackgroundStream interface {
	TrySynthesize(context.Context, string) (<-chan provider.TTSChunk, bool, error)
	EffectiveIdentity() (provider.AudioIdentity, bool)
	Close() error
}

func prepareQuoteAudio(ctx context.Context, speech quoteBackgroundStream, text string) (preparedQuoteAudio, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	chunks, attempted, err := speech.TrySynthesize(ctx, text)
	if errors.Is(err, provider.ErrBackgroundClosed) {
		return preparedQuoteAudio{}, attempted, errQuoteAudioRetired
	}
	if err != nil {
		return preparedQuoteAudio{}, attempted, err
	}
	fail := func(err error) (preparedQuoteAudio, bool, error) {
		_ = speech.Close()
		return preparedQuoteAudio{}, attempted, err
	}
	if chunks == nil {
		return fail(errors.New("quote synthesis returned no stream"))
	}
	var pcm []byte
	for {
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case chunk, ok := <-chunks:
			if !ok {
				if ctx.Err() != nil {
					return fail(ctx.Err())
				}
				identity, valid := speech.EffectiveIdentity()
				if !valid || len(pcm) == 0 || len(pcm)%2 != 0 {
					return fail(errors.New("quote synthesis incomplete"))
				}
				key, _ := json.Marshal(identity)
				return preparedQuoteAudio{audio: PhraseAudio{PCM: pcm, Rate: identity.Rate}, identity: string(key)}, attempted, nil
			}
			if chunk.Err != nil {
				return fail(chunk.Err)
			}
			if len(pcm)+len(chunk.PCM) > quoteAudioEntryBytes {
				return fail(errors.New("quote synthesis exceeds audio limit"))
			}
			pcm = append(pcm, chunk.PCM...)
		}
	}
}
