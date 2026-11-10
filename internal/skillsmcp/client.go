// Package skillsmcp retrieves operator-published business guidance through MCP.
package skillsmcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const ToolName = "load_business_skill"
const routeKey = "ai.liuchuanming.golive/turn"
const bindingKey = "ai.liuchuanming.golive/routing"
const protocol = "2026-07-28"

type Config struct {
	URL       string   `json:"url"`
	TokenFile string   `json:"token_file"`
	TimeoutMS int      `json:"timeout_ms"`
	StateKeys []string `json:"state_keys"`
}
type Client struct {
	cfg   Config
	token string
	http  *http.Client
}
type File struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}
type Binding struct {
	Events []string            `json:"events"`
	State  map[string][]string `json:"state"`
	Tool   string              `json:"tool"`
}
type Entry struct {
	URI         string             `json:"uri"`
	Frontmatter map[string]string  `json:"frontmatter"`
	Resources   []File             `json:"resources"`
	Meta        map[string]Binding `json:"_meta"`
}
type Guidance struct {
	State  map[string][]string `json:"required_state,omitempty"`
	URI    string              `json:"uri"`
	Digest string              `json:"digest"`
	Text   string              `json:"text,omitempty"`
}

func New(cfg Config) (*Client, error) {
	token, err := os.ReadFile(cfg.TokenFile)
	if err != nil || len(bytes.TrimSpace(token)) < 16 {
		return nil, errors.New("skills credential unavailable")
	}
	if cfg.StateKeys == nil {
		cfg.StateKeys = []string{"wechat.status"}
	}
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = 150
	}
	return &Client{cfg: cfg, token: strings.TrimSpace(string(token)), http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Duration(c.cfg.TimeoutMS)*time.Millisecond)
}
func (c *Client) rpc(ctx context.Context, method string, params map[string]any, out any) error {
	params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": protocol, "io.modelcontextprotocol/clientInfo": map[string]string{"name": "xiaocui-active-call", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", protocol)
	req.Header.Set("Mcp-Method", method)
	if method == "prompts/get" {
		req.Header.Set("Mcp-Name", params["name"].(string))
	}
	if method == "resources/read" {
		req.Header.Set("Mcp-Name", params["uri"].(string))
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("skills service unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("skills service rejected request")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (512<<10)+1))
	if err != nil || len(data) > 512<<10 {
		return errors.New("skills response exceeds limit")
	}
	var msg struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &msg) != nil || len(msg.Result) == 0 || len(msg.Error) > 0 {
		return errors.New("invalid skills response")
	}
	return json.Unmarshal(msg.Result, out)
}
func (c *Client) Catalog(ctx context.Context) ([]Entry, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	var result struct {
		Skills []Entry `json:"skills"`
	}
	if err := c.rpc(ctx, "skills/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	if len(result.Skills) > 64 {
		return nil, errors.New("too many skills")
	}
	return result.Skills, nil
}
func (c *Client) projection(memory json.RawMessage) map[string]string {
	var values map[string]any
	_ = json.Unmarshal(memory, &values)
	out := map[string]string{}
	for _, path := range c.cfg.StateKeys {
		var value any = values
		for _, part := range strings.Split(path, ".") {
			object, ok := value.(map[string]any)
			if !ok {
				value = nil
				break
			}
			value = object[part]
		}
		if text, ok := value.(string); ok {
			out[path] = text
		}
	}
	return out
}

type selection struct {
	Meta map[string]struct {
		Revision        int  `json:"revision"`
		AutoSelection   bool `json:"auto_selection"`
		PromptRendering *struct {
			Version int `json:"version"`
		} `json:"prompt_rendering"`
	} `json:"_meta"`
	Messages []struct {
		Content struct {
			Type string                     `json:"type"`
			URI  string                     `json:"uri"`
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"content"`
	} `json:"messages"`
}

func (c *Client) selectSkills(ctx context.Context, event string, utterance string, memory json.RawMessage) (selection, error) {
	state, _ := json.Marshal(c.projection(memory))
	var out selection
	err := c.rpc(ctx, "prompts/get", map[string]any{"name": "outbound-turn", "arguments": map[string]string{"event": event, "utterance": utterance, "state": string(state)}}, &out)
	return out, err
}
func (c *Client) Load(ctx context.Context, event string, memory json.RawMessage) ([]Guidance, error) {
	return c.load(ctx, event, "", memory, nil)
}

func (c *Client) LoadForTurn(ctx context.Context, utterance string, memory json.RawMessage) ([]Guidance, error) {
	return c.load(ctx, "auto", utterance, memory, nil)
}

// PrepareForTurn retrieves guidance and its catalog-owned rendering mode in one
// revision-consistent operation. Existing servers select version 1 by omission.
func (c *Client) PrepareForTurn(ctx context.Context, utterance string, memory json.RawMessage) ([]Guidance, int, error) {
	version := 1
	guides, err := c.load(ctx, "auto", utterance, memory, &version)
	return guides, version, err
}

func (c *Client) load(ctx context.Context, event, utterance string, memory json.RawMessage, renderingVersion *int) ([]Guidance, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	selected, err := c.selectSkills(ctx, event, utterance, memory)
	if err != nil {
		return nil, err
	}
	if event == "auto" && !selected.Meta[routeKey].AutoSelection {
		return nil, errors.New("skills service lacks automatic selection")
	}
	if selected.Meta[routeKey].Revision < 1 {
		return nil, errors.New("missing skill catalog revision")
	}
	version := 1
	if mode := selected.Meta[routeKey].PromptRendering; mode != nil {
		version = mode.Version
	}
	if version != 1 && version != 2 {
		return nil, errors.New("unsupported prompt rendering version")
	}
	if renderingVersion != nil {
		*renderingVersion = version
	}
	if len(selected.Messages) > 4 {
		return nil, errors.New("too many matching skills")
	}
	out := []Guidance{}
	for _, message := range selected.Messages {
		link := message.Content
		if link.Type != "resource_link" || !strings.HasPrefix(link.URI, "skill://") {
			return nil, errors.New("invalid skill link")
		}
		var expected File
		if json.Unmarshal(link.Meta[routeKey], &expected) != nil || expected.URI != link.URI {
			return nil, errors.New("invalid skill manifest")
		}
		var result struct {
			Skill Entry `json:"skill"`
		}
		if err = c.rpc(ctx, "skills/get", map[string]any{"uri": link.URI}, &result); err != nil {
			return nil, err
		}
		if len(result.Skill.Resources) != 1 || result.Skill.Resources[0] != expected || result.Skill.URI != link.URI {
			return nil, errors.New("skill changed during retrieval")
		}
		var resource struct {
			Contents []struct {
				URI  string `json:"uri"`
				MIME string `json:"mimeType"`
				Text string `json:"text"`
			} `json:"contents"`
		}
		if err = c.rpc(ctx, "resources/read", map[string]any{"uri": link.URI}, &resource); err != nil {
			return nil, err
		}
		if len(resource.Contents) != 1 {
			return nil, errors.New("invalid skill content")
		}
		file := resource.Contents[0]
		if file.URI != link.URI || file.MIME != "text/markdown" || len(file.Text) != expected.Size || fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(file.Text))) != expected.Digest {
			return nil, errors.New("skill integrity check failed")
		}
		// The maintained server emits exactly two JSON-quoted YAML fields.
		head, body, ok := strings.Cut(file.Text, "\n---\n")
		lines := strings.Split(head, "\n")
		if !ok || len(lines) != 3 || lines[0] != "---" {
			return nil, errors.New("unsupported skill frontmatter")
		}
		front := map[string]string{}
		for _, line := range lines[1:] {
			key, value, ok := strings.Cut(line, ": ")
			var text string
			if !ok || json.Unmarshal([]byte(value), &text) != nil {
				return nil, errors.New("invalid skill frontmatter")
			}
			front[key] = text
		}
		if len(front) != 2 || front["name"] != result.Skill.Frontmatter["name"] || front["description"] != result.Skill.Frontmatter["description"] {
			return nil, errors.New("skill frontmatter mismatch")
		}
		out = append(out, Guidance{URI: link.URI, Digest: expected.Digest, Text: strings.TrimSpace(body), State: result.Skill.Meta[bindingKey].State})
	}
	if len(out) == 0 {
		return out, nil
	}
	latest, err := c.selectSkills(ctx, event, utterance, memory)
	if err != nil || latest.Meta[routeKey].Revision != selected.Meta[routeKey].Revision {
		return nil, errors.New("skill catalog changed during retrieval")
	}
	return out, nil
}

// Definition contains only discovery metadata. Full instructions are loaded on demand.
func Definition(entries []Entry) json.RawMessage {
	events := []string{}
	var desc strings.Builder
	desc.WriteString("Load current business guidance before detailed workflow steps. Choose an event by the user's meaning. First use update_memory for confirmed state, including wechat.status when appropriate. Guidance applies only to this user turn. On unavailable or no_match use the original prompt. ")
	for _, entry := range entries {
		binding := entry.Meta[bindingKey]
		if len(binding.Events) == 0 {
			continue
		}
		events = append(events, binding.Events...)
		conditions, _ := json.Marshal(binding.State)
		fmt.Fprintf(&desc, "%s: %s. Events %s. Required memory %s. ", entry.Frontmatter["name"], entry.Frontmatter["description"], strings.Join(binding.Events, ", "), conditions)
	}
	if len(events) == 0 {
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": ToolName, "description": desc.String(), "parameters": map[string]any{"type": "object", "properties": map[string]any{"event": map[string]any{"type": "string", "enum": events}}, "required": []string{"event"}, "additionalProperties": false}}})
	return raw
}
