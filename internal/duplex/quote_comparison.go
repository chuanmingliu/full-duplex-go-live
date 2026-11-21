package duplex

import (
	"encoding/json"
	"fmt"

	"github.com/chuanmingliu/golive/internal/provider"
)

// This engine-local shorthand is expanded before either HTTP or MCP dispatch.
// Older CRM servers continue to receive their existing configs contract.
func quoteComparisonParameters(raw json.RawMessage) json.RawMessage {
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil || schema == nil {
		return raw
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		schema["properties"] = props
	}
	props["compare_coverage"] = map[string]any{"type": "string", "description": "Compare every configured tier of this one coverage; no config/configs. Explicit subsets must use configs."}
	out, err := json.Marshal(schema)
	if err != nil {
		return raw
	}
	return out
}

func (e *Engine) expandQuoteComparison(call provider.ToolCall) (provider.ToolCall, error) {
	if call.Name != "quote_total" {
		return call, nil
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return call, nil
	}
	raw, present := args["compare_coverage"]
	if !present {
		return call, nil
	}
	var name string
	if json.Unmarshal(raw, &name) != nil || name == "" {
		return call, fmt.Errorf("compare_coverage requires a configured coverage name")
	}
	if _, exists := args["config"]; exists {
		return call, fmt.Errorf("compare_coverage cannot be combined with config")
	}
	if _, exists := args["configs"]; exists {
		return call, fmt.Errorf("compare_coverage cannot be combined with configs")
	}
	schema := e.quoteSchema()
	if canonical, ok := schema.Aliases[name]; ok {
		name = canonical
	}
	options := schema.Options[name]
	if len(options) == 0 {
		return call, fmt.Errorf("no configured tiers for compare_coverage; consult the active catalog")
	}
	// Bound expansion independently of model output or operator catalog size.
	if len(options) > 16 {
		return call, fmt.Errorf("too many configured tiers; request explicit subsets")
	}
	configs := make([]map[string]any, 0, len(options))
	for _, option := range options {
		configs = append(configs, map[string]any{name: option})
	}
	delete(args, "compare_coverage")
	args["configs"], _ = json.Marshal(configs)
	out, err := json.Marshal(args)
	if err != nil {
		return call, err
	}
	call.Arguments = string(out)
	return call, nil
}
