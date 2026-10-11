// Package deepseek implements a streaming LLM adapter against any
// OpenAI-compatible /v1/chat/completions endpoint.
//
// It is named for the backend this project was built against, but nothing here
// is DeepSeek-specific: point GOLIVE_BACKEND_BASE_URL at any compatible server
// (vLLM, Ollama's OpenAI shim, a gateway) and set the matching key. Registering
// it twice under two names is how you run two backends side by side.
package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

func init() {
	provider.RegisterLLM("deepseek", func() (provider.LLM, error) {
		return NewNamed("deepseek", "DEEPSEEK_API_KEY", "https://api.deepseek.com", "deepseek-chat")
	})
	provider.RegisterLLM("openai-compatible", func() (provider.LLM, error) {
		return NewNamed("openai", "OPENAI_API_KEY", "https://api.openai.com", "gpt-4o-mini")
	})

	// Cerebras. Wafer-scale inference, and the reason to care here is the one
	// number this project keeps failing to move: time to first token. The
	// backend is 2.4–3.4 s of a ~3.8 s answer under a long persona prompt, and
	// nothing else in the cascade is within an order of magnitude of that.
	//
	// The default model is qwen-3.8-27b because it is the Qwen their own model
	// catalogue, rate-limit page and quickstart all agree is on the shared
	// endpoint. Gemma is real there — Cerebras have blogged about serving Gemma
	// 4 31B — but the only exact id their docs give is google/gemma-4-31b-it,
	// and that is on the *dedicated* endpoints, which use HuggingFace-style ids
	// and which the docs do not say share this base URL. So it is a setting,
	// not a default: GOLIVE_CEREBRAS_MODEL=google/gemma-4-31b-it with
	// GOLIVE_CEREBRAS_BASE_URL pointed at whatever your dedicated instance
	// gives you. Guessing an id here would fail at request time with something
	// that reads like a bug in golive.
	provider.RegisterLLM("cerebras", func() (provider.LLM, error) {
		return NewNamed("cerebras", "CEREBRAS_API_KEY", "https://api.cerebras.ai", "qwen-3.8-27b")
	})

	// Inception Mercury: a diffusion language model rather than an
	// autoregressive one, which is interesting here for the same reason —
	// they publish ~1,100 tokens/s and quote a voice-agent turn at about
	// 170 ms. Their API is OpenAI-shaped, so nothing below changes.
	//
	// One thing deliberately not exposed: the `diffusing: true` request flag.
	// With it on, each chunk carries the *whole* answer as it is refined rather
	// than the next piece of it, and every consumer in this engine appends —
	// conversation history, the transcript, and the segmenter that cuts text
	// into TTS segments. A replace-semantics stream cannot be spoken
	// incrementally at all, since a sentence can change after it has been said.
	// Supporting it means a Replace flag on LLMDelta and a segmenter that can
	// retract, which is a real piece of work and not a flag.
	provider.RegisterLLM("inception", func() (provider.LLM, error) {
		return NewNamed("inception", "INCEPTION_API_KEY", "https://api.inceptionlabs.ai", "mercury-2.5")
	})
}

// LLM is a streaming chat-completions client.
type LLM struct {
	name         string
	BaseURL      string
	APIKey       string
	DefaultModel string
	HTTP         *http.Client

	// lanes is the pool: see lane. Built on first use, so that a client put
	// together by hand — a test, a probe — has one without asking.
	lanesOnce sync.Once
	lanes     []*lane
	keys      []string
	next      atomic.Uint64
}

// New builds a client under the shared GOLIVE_BACKEND_* overrides.
func New(keyEnv, defaultBase, defaultModel string) (*LLM, error) {
	return NewNamed("", keyEnv, defaultBase, defaultModel)
}

// NewNamed builds a client whose base URL and model can be set per provider.
//
// The package comment has always said registering this twice is how you run two
// backends side by side, and with only GOLIVE_BACKEND_BASE_URL and
// GOLIVE_BACKEND_MODEL that was not true: the second registration would inherit
// the first one's overrides and quietly send Qwen's model id to DeepSeek. So
// each provider gets its own pair — GOLIVE_CEREBRAS_MODEL, GOLIVE_INCEPTION_BASE_URL
// — and the shared pair remains as the fallback, which keeps every existing
// .env working unchanged.
func NewNamed(name, keyEnv, defaultBase, defaultModel string) (*LLM, error) {
	keys, err := poolKeys(keyEnv)
	if err != nil {
		return nil, err
	}
	// Most specific wins: this provider's own variable, then the shared one,
	// then the built-in default. The other order would make the per-provider
	// setting useless the moment GOLIVE_BACKEND_MODEL is set at all, which is
	// the state most existing .env files are already in.
	base := config.Env("GOLIVE_BACKEND_BASE_URL", defaultBase)
	model := config.Env("GOLIVE_BACKEND_MODEL", defaultModel)
	if name != "" {
		prefix := "GOLIVE_" + strings.ToUpper(name) + "_"
		base = config.Env(prefix+"BASE_URL", base)
		model = config.Env(prefix+"MODEL", model)
	}
	return &LLM{
		name:         name,
		BaseURL:      strings.TrimRight(base, "/"),
		APIKey:       keys[0],
		DefaultModel: model,
		HTTP:         newHTTPClient(),
		keys:         keys,
	}, nil
}

func newHTTPClient() *http.Client {
	return &http.Client{
		// No overall timeout: a streaming completion is long-lived by
		// design and the engine cancels through the context instead. The
		// transport timeouts below still bound a hung connection.
		Transport: &http.Transport{
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       300 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
		},
	}
}

// Name implements provider.LLM.
func (l *LLM) Name() string {
	if l.name != "" {
		return l.name
	}
	return "deepseek"
}

// Prewarm implements provider.Prewarmer: it opens a connection to the backend
// and returns it to the idle pool, so the first completion of a session does
// not pay a TCP and TLS handshake on the critical path.
//
// The request is deliberately worthless — a GET at the base URL, which every
// OpenAI-compatible server answers with some 4xx — because the point is the
// socket, not the response. No tokens are spent and no auth is required for the
// connection to end up pooled. The body is drained rather than abandoned;
// Go only reuses a connection whose response was read to completion.
func (l *LLM) Prewarm(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.BaseURL+"/", nil)
	if err != nil {
		return err
	}
	resp, err := l.pick(ctx).client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

type chatRequest struct {
	Model       string            `json:"model"`
	Messages    []chatMessage     `json:"messages"`
	Stream      bool              `json:"stream"`
	Temperature float64           `json:"temperature,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	ToolChoice  any               `json:"tool_choice,omitempty"`
	// A pointer so "false" is sent and "unset" is not: the two mean different
	// things, and a backend that does not know the field rejects it.
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
	StreamOpts        *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

// chatToolCall is an assistant message's function call, as chat completions
// wants it echoed back.
type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toChatMessages converts the engine's messages to the wire form.
func toChatMessages(in []provider.Message) []chatMessage {
	msgs := make([]chatMessage, 0, len(in))
	for _, m := range in {
		cm := chatMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		// Name on a tool message is an older convention some compatible
		// servers reject; the call id is what ties a result to its call.
		if m.Role != provider.RoleTool {
			cm.Name = m.Name
		}
		for _, tc := range m.ToolCalls {
			var c chatToolCall
			c.ID = tc.ID
			c.Type = "function"
			c.Function.Name = tc.Name
			c.Function.Arguments = tc.Arguments
			if c.Function.Arguments == "" {
				c.Function.Arguments = "{}"
			}
			cm.ToolCalls = append(cm.ToolCalls, c)
		}
		msgs = append(msgs, cm)
	}
	return msgs
}

// promptChars totals the characters sent, which is the thing that predicts
// time-to-first-token. Runes rather than bytes, because a Chinese prompt is
// three bytes a character and the byte count would read as three times the
// prompt it is.
func promptChars(msgs []chatMessage) int {
	n := 0
	for _, m := range msgs {
		n += len([]rune(m.Content))
	}
	return n
}

// completionsURL is where a base URL's chat completions live.
//
// Most vendors serve them at <base>/v1/chat/completions and document the base
// without the version, which is what a bare base gets. Some document it with
// the version already on — and not always v1: Volcengine's is /api/v3, Zhipu's
// /api/paas/v4 — and appending /v1 to those asks for a path that does not
// exist. A base that already ends in a version segment, or in the full path,
// is taken at its word.
func completionsURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	last := base[strings.LastIndex(base, "/")+1:]
	if len(last) >= 2 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		// DeepSeek reports its prefix cache at the top level; OpenAI and most
		// compatible servers nest it. Whichever is present is used.
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails  *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// Stream implements provider.LLM.
func (l *LLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	model := req.Model
	if model == "" {
		model = l.DefaultModel
	}

	msgs := toChatMessages(req.Messages)

	creq := chatRequest{
		Model:       model,
		Messages:    msgs,
		Stream:      true,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       req.Tools,
		StreamOpts:  &streamOptions{IncludeUsage: true},
	}
	// Tool settings only mean something next to tools, and a server that is
	// strict about unknown or inapplicable fields refuses the whole request.
	if len(req.Tools) > 0 {
		creq.ToolChoice = req.ToolChoice
		creq.ParallelToolCalls = req.ParallelToolCalls
	}
	body, err := json.Marshal(creq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		completionsURL(l.BaseURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	ln := l.pick(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+ln.key)
	httpReq.Header.Set("Accept", "text/event-stream")

	started := time.Now()
	resp, err := ln.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		// Read a bounded snippet: the body may be an HTML error page, and the
		// message is surfaced to operators, so it must never carry the request
		// text back out.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		l.rest(ln, resp.StatusCode)
		return nil, fmt.Errorf("llm: %s returned HTTP %d on key %d of %d: %s",
			l.BaseURL, resp.StatusCode, ln.keyN+1, len(l.keys), summarizeError(snippet))
	}

	// prompt_chars, not just the message count, because size is what costs.
	//
	// Two logs from the same machine, model and endpoint, a day apart: a
	// seventy-nine character system prompt gave a time-to-first-token of
	// 259–546 ms, and a long persona prompt with the same history_turns gave
	// 2441–3385 ms. Nothing else differed. That is five to seven times the
	// latency, and it is the single largest term in the whole cascade — larger
	// than the silence threshold, the recognizer and synthesis together.
	//
	// The message count alone hid it, because the count barely moved while the
	// prompt behind it grew by an order of magnitude. Logging the size next to
	// the latency it buys makes the trade visible on every turn.
	slog.Debug("llm: stream open",
		"base_url", l.BaseURL,
		"lane", ln.n,
		"key", ln.keyN+1,
		"model", model,
		"messages", len(msgs),
		"prompt_chars", promptChars(msgs),
		"ms", time.Since(started).Milliseconds())

	out := make(chan provider.LLMDelta, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		l.consume(ctx, resp.Body, req.DisableThinking, out)
	}()
	return out, nil
}

func (l *LLM) consume(ctx context.Context, body io.Reader, dropThinking bool, out chan<- provider.LLMDelta) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	// Tool calls arrive in fragments keyed by index; accumulate and flush at
	// finish_reason.
	type partialCall struct {
		id   string
		name string
		args strings.Builder
	}
	calls := map[int]*partialCall{}

	send := func(d provider.LLMDelta) bool {
		select {
		case out <- d:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// flushCalls emits the accumulated calls in the order the model made them.
	// Ranging over the map directly did not: with two calls in one response the
	// order was random from run to run, and an application that executes them
	// in arrival order got a different sequence each time.
	//
	// whole says the model declared itself finished. Without that — a stream
	// that simply stopped — a call is only passed on if its arguments parse:
	// the application is about to run it, and half a JSON object is not what
	// the model asked for.
	flushCalls := func(whole bool) bool {
		if len(calls) == 0 {
			return true
		}
		idx := make([]int, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for n, i := range idx {
			pc := calls[i]
			if pc.name == "" {
				continue
			}
			if args := strings.TrimSpace(pc.args.String()); !whole && args != "" && !json.Valid([]byte(args)) {
				slog.Warn("llm: dropping a function call cut off mid-arguments", "name", pc.name)
				continue
			}
			id := pc.id
			if id == "" {
				// Some compatible servers omit the id. The engine needs one to
				// match a result to its call.
				id = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), n)
			}
			if !send(provider.LLMDelta{ToolCall: &provider.ToolCall{
				ID:        id,
				Name:      pc.name,
				Arguments: pc.args.String(),
			}}) {
				return false
			}
		}
		calls = map[int]*partialCall{}
		return true
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}

		var chunk chatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// One malformed frame should not kill a turn mid-sentence.
			continue
		}
		if chunk.Usage != nil {
			cached := chunk.Usage.PromptCacheHitTokens
			if d := chunk.Usage.PromptTokensDetails; d != nil && d.CachedTokens > cached {
				cached = d.CachedTokens
			}
			// The cache is the one thing a caller controls about time to first
			// token without changing the prompt: a prefix the provider has seen
			// is not processed again. Whether it was hit is otherwise invisible,
			// so it is logged next to the size it applies to.
			slog.Debug("llm: usage",
				"prompt_tokens", chunk.Usage.PromptTokens,
				"cached_prompt_tokens", cached,
				"completion_tokens", chunk.Usage.CompletionTokens)
			if !send(provider.LLMDelta{Usage: &provider.TokenUsage{
				InputTokens:       chunk.Usage.PromptTokens,
				OutputTokens:      chunk.Usage.CompletionTokens,
				CachedInputTokens: cached,
			}}) {
				return
			}
		}
		for _, choice := range chunk.Choices {
			if !dropThinking && choice.Delta.ReasoningContent != "" {
				// Reasoning is never spoken; it is surfaced as text only when
				// the caller has explicitly left thinking enabled.
				if !send(provider.LLMDelta{Text: ""}) {
					return
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				pc := calls[tc.Index]
				if pc == nil {
					pc = &partialCall{}
					calls[tc.Index] = pc
				}
				if tc.ID != "" {
					pc.id = tc.ID
				}
				if tc.Function.Name != "" {
					pc.name = tc.Function.Name
				}
				pc.args.WriteString(tc.Function.Arguments)
			}
			if choice.Delta.Content != "" {
				if !send(provider.LLMDelta{Text: choice.Delta.Content}) {
					return
				}
			}
			if choice.FinishReason != "" {
				if !flushCalls(true) {
					return
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() == nil {
			send(provider.LLMDelta{Err: fmt.Errorf("llm: stream read: %w", err)})
		}
		return
	}
	// A stream that ended without a finish_reason still made its calls.
	if !flushCalls(false) {
		return
	}
}

// summarizeError pulls the provider's error message out of a JSON body,
// falling back to a trimmed snippet. It never returns the full body.
func summarizeError(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Error.Message != "" {
			return parsed.Error.Message
		}
		if parsed.Message != "" {
			return parsed.Message
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}
