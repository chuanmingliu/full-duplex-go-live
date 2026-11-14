package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const slotKey = "ai.liuchuanming.golive/turn"

var signalName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

func (s Skill) validateBindings() error {
	if len(s.Match)+len(s.Exclude) > 32 {
		return errors.New("too many text routing rules")
	}
	for _, pattern := range append(append([]string{}, s.Match...), s.Exclude...) {
		if len(pattern) == 0 || len(pattern) > 1024 {
			return errors.New("invalid routing pattern length")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return errors.New("invalid routing regular expression")
		}
	}
	if s.Tool != "" && s.Tool != "quote_total" {
		return errors.New("unknown tool binding")
	}
	if len(s.Events) > 32 || len(s.State) > 16 {
		return errors.New("too many routing conditions")
	}
	for _, event := range s.Events {
		if len(event) > 96 || !signalName.MatchString(event) {
			return errors.New("events must be dotted lowercase identifiers")
		}
	}
	for field, values := range s.State {
		if len(field) > 96 || !signalName.MatchString(field) || len(values) == 0 || len(values) > 32 {
			return errors.New("invalid state guard")
		}
		for _, v := range values {
			if strings.TrimSpace(v) == "" || len(v) > 128 {
				return errors.New("invalid state value")
			}
		}
	}
	return nil
}
func (c Catalog) available(s Skill) bool { return s.Enabled && (s.Tool == "" || c.Quote.Enabled) }
func (s Skill) matches(event string, state map[string]string) bool {
	if !slices.Contains(s.Events, event) {
		return false
	}
	for key, allowed := range s.State {
		if !slices.Contains(allowed, state[key]) {
			return false
		}
	}
	return true
}

type turnInput struct {
	Event     string            `json:"event"`
	Utterance string            `json:"utterance"`
	State     map[string]string `json:"state"`
}

func (c Catalog) selectTurn(t turnInput) ([]Skill, []string, error) {
	if (t.Event != "auto" && !signalName.MatchString(t.Event)) || len(t.Event) > 96 || len(t.Utterance) > 24000 {
		return nil, nil, errors.New("invalid business event")
	}
	skills := []Skill{}
	tools := []string{}
	for _, sk := range c.Skills {
		event := t.Event
		if event == "auto" {
			if !sk.matchesText(t.Utterance) || len(sk.Events) == 0 {
				continue
			}
			event = sk.Events[0]
		}
		if !c.available(sk) || !sk.matches(event, t.State) {
			continue
		}
		skills = append(skills, sk)
		if sk.Tool != "" && !slices.Contains(tools, sk.Tool) {
			tools = append(tools, sk.Tool)
		}
	}
	return skills, tools, nil
}
func registerOutboundPrompt(server *mcp.Server, c Catalog, index *skillIndex) {
	server.AddPrompt(&mcp.Prompt{Name: "outbound-turn", Description: "Select skill references for the current outbound event and confirmed business state.", Arguments: []*mcp.PromptArgument{
		{Name: "event", Required: true, Description: "A normalized business event such as wechat.notification_help."},
		{Name: "utterance", Description: "Current user text for event auto; matched using operator-maintained regular expressions."},
		{Name: "state", Description: "JSON object of projected string state values, e.g. {\"wechat.status\":\"consented\"}."},
	}}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		args := req.Params.Arguments
		event := args["event"]
		state := map[string]string{}
		if raw := args["state"]; raw != "" && (len(raw) > 8192 || json.Unmarshal([]byte(raw), &state) != nil || state == nil) {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid projected state"}
		}
		messages := []*mcp.PromptMessage{}
		input := turnInput{Event: event, State: state, Utterance: args["utterance"]}
		skills, tools, err := c.selectTurn(input)
		var prepared *preparedTurn
		if supportsPreparedTurn(req) {
			prepared, err = index.prepareTurn(ctx, input, skills, err)
			if errors.Is(err, errPreparedTooLarge) {
				prepared, err = nil, nil
			}
			if err == nil && prepared != nil {
				skills = nil
				tools = nil
				for _, resource := range prepared.Resources {
					item := index.items[index.byURI[resource.Manifest.URI]].skill
					skills = append(skills, item)
					if item.Tool != "" && !slices.Contains(tools, item.Tool) {
						tools = append(tools, item.Tool)
					}
				}
			}
		}
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
		}
		for _, sk := range skills {
			link := &mcp.ResourceLink{URI: sk.uri(), Name: sk.Name, Description: sk.Description, MIMEType: "text/markdown", Meta: mcp.Meta{slotKey: sk.entry().Resources[0], "ai.liuchuanming.golive/tool": sk.Tool}}
			messages = append(messages, &mcp.PromptMessage{Role: "user", Content: link})

		}
		return &mcp.GetPromptResult{Meta: mcp.Meta{slotKey: map[string]any{"revision": c.Revision, "tools": tools, "auto_selection": true, "prompt_rendering": PromptRendering{Version: c.renderingVersion()}, "prepared": prepared}}, Messages: messages}, nil
	})
}

func (s Skill) excludesText(text string) bool {
	for _, pattern := range s.Exclude {
		if regexp.MustCompile(pattern).MatchString(text) {
			return true
		}
	}
	return false
}

func (s Skill) matchesText(text string) bool {
	if s.excludesText(text) {
		return false
	}

	for _, pattern := range s.Match {
		if regexp.MustCompile(pattern).MatchString(text) {
			return true
		}
	}
	return false
}
