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
	"net/http"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

func init() {
	provider.RegisterLLM("deepseek", func() (provider.LLM, error) {
		return New("DEEPSEEK_API_KEY", "https://api.deepseek.com", "deepseek-chat")
	})
	provider.RegisterLLM("openai-compatible", func() (provider.LLM, error) {
		return New("OPENAI_API_KEY", "https://api.openai.com", "gpt-4o-mini")
	})
}

// LLM is a streaming chat-completions client.
type LLM struct {
	BaseURL      string
	APIKey       string
	DefaultModel string
	HTTP         *http.Client
}

// New builds a client, reading the key from keyEnv and allowing
// GOLIVE_BACKEND_BASE_URL / GOLIVE_BACKEND_MODEL to override the defaults.
func New(keyEnv, defaultBase, defaultModel string) (*LLM, error) {
	key, err := config.EnvRequired(keyEnv)
	if err != nil {
		return nil, err
	}
	return &LLM{
		BaseURL:      strings.TrimRight(config.Env("GOLIVE_BACKEND_BASE_URL", defaultBase), "/"),
		APIKey:       key,
		DefaultModel: config.Env("GOLIVE_BACKEND_MODEL", defaultModel),
		HTTP: &http.Client{
			// No overall timeout: a streaming completion is long-lived by
			// design and the engine cancels through the context instead. The
			// transport timeouts below still bound a hung connection.
			Transport: &http.Transport{
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       300 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
			},
		},
	}, nil
}

// Name implements provider.LLM.
func (l *LLM) Name() string { return "deepseek" }

type chatRequest struct {
	Model       string            `json:"model"`
	Messages    []chatMessage     `json:"messages"`
	Stream      bool              `json:"stream"`
	Temperature float64           `json:"temperature,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	StreamOpts  *streamOptions    `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
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
	} `json:"usage"`
}

// Stream implements provider.LLM.
func (l *LLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	model := req.Model
	if model == "" {
		model = l.DefaultModel
	}

	msgs := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, chatMessage{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		})
	}

	body, err := json.Marshal(chatRequest{
		Model:       model,
		Messages:    msgs,
		Stream:      true,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       req.Tools,
		StreamOpts:  &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		l.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+l.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := l.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		// Read a bounded snippet: the body may be an HTML error page, and the
		// message is surfaced to operators, so it must never carry the request
		// text back out.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("llm: %s returned HTTP %d: %s",
			l.BaseURL, resp.StatusCode, summarizeError(snippet))
	}

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
			if !send(provider.LLMDelta{Usage: &provider.TokenUsage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
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
				for _, pc := range calls {
					if pc.name == "" {
						continue
					}
					if !send(provider.LLMDelta{ToolCall: &provider.ToolCall{
						ID:        pc.id,
						Name:      pc.name,
						Arguments: pc.args.String(),
					}}) {
						return
					}
				}
				calls = map[int]*partialCall{}
			}
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		send(provider.LLMDelta{Err: fmt.Errorf("llm: stream read: %w", err)})
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
