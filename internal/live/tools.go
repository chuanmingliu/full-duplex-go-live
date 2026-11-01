package live

import (
	"encoding/json"
	"fmt"
)

// A gpt-live-1 session describes its backend functions in the Responses form,
// with the function's fields at the top level:
//
//	{"type":"function","name":"lookup","description":…,"parameters":{…}}
//
// The backends golive drives speak chat completions, which nests them:
//
//	{"type":"function","function":{"name":"lookup","description":…,"parameters":{…}}}
//
// The two are one rename apart and silently incompatible: a chat endpoint
// handed the flat form either rejects the request or, worse, accepts it and
// never calls anything. Everything in this file is that rename, done once at
// the session boundary so the engine only ever sees the form its providers
// take.

// ChatTools converts Responses-form tool definitions for a chat-completions
// backend. Definitions already in the nested form pass through, so a client
// written against either convention works. Tools that are not functions cannot
// be run by such a backend and are returned by name in dropped rather than
// forwarded.
func ChatTools(tools []json.RawMessage) (chat []json.RawMessage, dropped []string) {
	for i, raw := range tools {
		var def struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
			Strict      *bool           `json:"strict"`
			Function    json.RawMessage `json:"function"`
		}
		if err := json.Unmarshal(raw, &def); err != nil {
			dropped = append(dropped, fmt.Sprintf("tools[%d] (not a JSON object)", i))
			continue
		}
		if def.Type != "function" {
			name := def.Type
			if name == "" {
				name = fmt.Sprintf("tools[%d] (no type)", i)
			}
			dropped = append(dropped, name)
			continue
		}
		if len(def.Function) > 0 {
			chat = append(chat, raw) // already nested
			continue
		}
		if def.Name == "" {
			dropped = append(dropped, fmt.Sprintf("tools[%d] (function with no name)", i))
			continue
		}
		fn := map[string]any{"name": def.Name}
		if def.Description != "" {
			fn["description"] = def.Description
		}
		if len(def.Parameters) > 0 {
			fn["parameters"] = def.Parameters
		}
		if def.Strict != nil {
			fn["strict"] = *def.Strict
		}
		out, err := json.Marshal(map[string]any{"type": "function", "function": fn})
		if err != nil {
			dropped = append(dropped, def.Name)
			continue
		}
		chat = append(chat, out)
	}
	return chat, dropped
}

// ChatToolChoice converts a Responses tool_choice. The string forms are the
// same in both conventions; a named function is {"type":"function","name":…}
// in one and {"type":"function","function":{"name":…}} in the other. Anything
// unrecognized becomes nil, which leaves the backend's default in force.
func ChatToolChoice(choice any) any {
	switch v := choice.(type) {
	case nil:
		return nil
	case string:
		switch v {
		case "auto", "required", "none":
			return v
		}
		return nil
	case map[string]any:
		if v["type"] != "function" {
			return nil
		}
		if _, nested := v["function"]; nested {
			return v
		}
		name, _ := v["name"].(string)
		if name == "" {
			return nil
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}
	default:
		return nil
	}
}
