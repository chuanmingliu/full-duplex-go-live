package speko

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client delegation uses the same Speko account through its separate LLM
// endpoint when the Router cannot admit a managed Responses delegation.
// It has no business tools and never claims an external action succeeded.
type backendResult struct {
	id, text string
	err      error
	elapsed  time.Duration
}
type backendMessage struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []backendText `json:"content"`
}
type backendText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type clientBackend struct {
	history      []backendMessage
	seen         map[string]bool
	result       chan backendResult
	cancel       context.CancelFunc
	active       string
	instructions string
}

func newClientBackend(instructions string) *clientBackend {
	return &clientBackend{instructions: instructions, seen: map[string]bool{}, result: make(chan backendResult, 1)}
}
func (b *clientBackend) close() {
	if b.cancel != nil {
		b.cancel()
	}
}
func (b *clientBackend) add(role, text string) {
	if text == "" {
		return
	}
	if n := len(b.history); n > 0 && b.history[n-1].Role == role {
		b.history[n-1].Content[0].Text += text
	} else {
		b.history = append(b.history, backendMessage{"message", role, []backendText{{"text", text}}})
	}
	// Keep recent context bounded; never grow with an unbounded provider stream.
	total := 0
	for i := len(b.history) - 1; i >= 0; i-- {
		total += len(b.history[i].Content[0].Text)
		if total > 48<<10 {
			b.history = b.history[i+1:]
			break
		}
	}
}
func (b *clientBackend) start(ctx context.Context, s *Service, id string) error {
	if id == "" || len(id) > 256 {
		return errors.New("invalid client delegation ID")
	}
	if b.seen[id] {
		return nil
	}
	if len(b.seen) >= 256 {
		return errors.New("too many client delegations")
	}
	b.seen[id] = true
	b.close()
	b.active = id
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	b.cancel = cancel
	// Deep copy: transcript updates keep arriving while HTTP is in flight.
	data, _ := json.Marshal(b.history)
	var history []backendMessage
	_ = json.Unmarshal(data, &history)
	go func() {
		defer cancel()
		started := time.Now()
		text, err := s.clientResponse(work, b.instructions, history)
		select {
		case b.result <- backendResult{id, text, err, time.Since(started)}:
		case <-work.Done():
			// Parent cancellation needs no response; an expired backend still must
			// release the spoken task rather than leaving the caller waiting.
			if ctx.Err() == nil {
				select {
				case b.result <- backendResult{id, "", work.Err(), time.Since(started)}:
				case <-ctx.Done():
				}
			}
		}
	}()
	return nil
}
func (s *Service) clientResponse(ctx context.Context, instructions string, history []backendMessage) (string, error) {
	u, _ := url.Parse(s.cfg.URL)
	u.Path = "/v1/llm/responses"
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	system := instructions + "\nYou assist a live voice conversation. Return a concise answer to the latest caller request. Transcript fragments may be incomplete. You have no external tools: do not claim to book, send, save, look up private records, or perform actions. If information is missing, ask for it."
	input := append([]backendMessage{{"message", "system", []backendText{{"text", system}}}}, history...)
	request := map[string]any{"routing": map[string]string{"mode": "explicit", "provider": "openai", "model": s.cfg.ResponsesModel}, "input": input, "max_output_tokens": 256, "stream": false}
	// The token limit includes hidden reasoning. An unconstrained reasoning
	// default can exhaust it before producing a complete spoken answer.
	if s.cfg.ResponsesReasoning != "provider_default" {
		request["reasoning_effort"] = s.cfg.ResponsesReasoning
	}
	body, _ := json.Marshal(request)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	req.Header.Set("Idempotency-Key", rand.Text())
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("Speko backend request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("Speko backend rejected the request")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10+1))
	if err != nil || len(data) > 128<<10 {
		return "", errors.New("invalid Speko backend response")
	}
	var response struct {
		Output     []backendMessage
		StopReason string `json:"stop_reason"`
	}
	if json.Unmarshal(data, &response) != nil || response.StopReason != "stop" {
		return "", errors.New("Speko backend response was incomplete")
	}
	var text strings.Builder
	for _, item := range response.Output {
		if item.Type == "message" && item.Role == "assistant" {
			for _, part := range item.Content {
				if part.Type == "text" {
					text.WriteString(part.Text)
				}
			}
		}
	}
	out := strings.TrimSpace(text.String())
	if out == "" || len(out) > 4096 {
		return "", errors.New("invalid Speko backend text")
	}
	return out, nil
}
