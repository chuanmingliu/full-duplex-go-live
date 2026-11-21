package duplex

import (
	"encoding/json"
	"fmt"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

// applyQuoteRevision commits a confirmed change before the async request takes
// its snapshot. A configs array always represents exploration, including a
// single hypothetical option. Same-response comparisons are folded by the
// caller before reaching this function.
func (e *Engine) applyQuoteRevision(call provider.ToolCall) error {
	if call.Name != "quote_total" || e.tools.memory == nil {
		return nil
	}
	var args struct {
		Config  map[string]any   `json:"config"`
		Configs []map[string]any `json:"configs"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return fmt.Errorf("quote arguments must be a JSON object: %w", err)
	}
	if len(args.Configs) > 0 || len(args.Config) == 0 {
		return nil
	}
	for _, value := range args.Config {
		switch value.(type) {
		case bool, string:
		default:
			return fmt.Errorf("quote config values must be booleans or tier strings; use false to remove coverage")
		}
	}
	snapshot, before := e.memorySnapshot()
	var memory struct {
		Quote map[string]any `json:"quote"`
	}
	if err := json.Unmarshal(snapshot, &memory); err != nil {
		return fmt.Errorf("quote memory must contain a selection object: %w", err)
	}
	// Canonicalize existing alias keys as well as the incoming patch.
	delta := map[string]any{}
	current := map[string]any{}
	for k, v := range memory.Quote {
		current[k] = v
	}
	schema := e.quoteSchema()
	schema.Normalize(current)
	schema.Normalize(args.Config)
	for k := range memory.Quote {
		if _, ok := current[k]; !ok {
			delta[k] = nil
		}
	}
	for k, v := range current {
		delta[k] = v
	}
	for k, v := range args.Config {
		delta[k] = v
	}
	patch, err := json.Marshal(map[string]any{"quote": delta})
	if err != nil {
		return err
	}
	version, err := e.tools.memory.Patch(patch)
	if err != nil {
		return err
	}
	if len(memory.Quote) == 0 {
		e.tools.quoteBaseDefault = true
	}
	if version != before {
		e.deps.Emit(live.MemoryEvent{Envelope: live.Envelope{Type: live.ExtMemory}, Version: version, Patch: patch})
		e.log.Info("tools: quote revision committed", "version", version, "coverages", len(args.Config))
	}
	return nil
}

// The most recent delivered result may have filled in a default selection.
// Compare against that normalized selection when describing its prices later.
func quoteResultMatchesMemory(task *tools.Task, memory json.RawMessage) bool {
	var result struct {
		Selection map[string]any `json:"selection"`
	}
	if json.Unmarshal(task.Result.Output, &result) != nil || len(result.Selection) == 0 {
		return quoteInputsUnchanged(task, memory)
	}
	baseline := map[string]any{}
	if len(task.Request.Memory) > 0 && json.Unmarshal(task.Request.Memory, &baseline) != nil {
		return false
	}
	if baseline == nil {
		baseline = map[string]any{}
	}
	baseline["quote"] = result.Selection
	request := task.Request
	request.Memory, _ = json.Marshal(baseline)
	return quoteInputsUnchanged(&tools.Task{Tool: task.Tool, Request: request}, memory)
}

func (e *Engine) quoteSchema() tools.QuoteSelectionSchema {
	if e.tools == nil || e.tools.quoteBrief == nil {
		return tools.QuoteSelectionSchema{}
	}
	b := e.tools.quoteBrief
	name := b.Plan
	raw, _ := e.memorySnapshot()
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if p, ok := m["quote_plan"].(string); ok {
			name = p
		}
	}
	return b.Plans[name]
}
func (e *Engine) normalizeQuoteCall(c provider.ToolCall) provider.ToolCall {
	if c.Name != "quote_total" {
		return c
	}
	var args map[string]any
	if json.Unmarshal([]byte(c.Arguments), &args) != nil {
		return c
	}
	schema := e.quoteSchema()
	if len(schema.Aliases) == 0 && len(schema.Details) == 0 {
		return c
	}
	if m, ok := args["config"].(map[string]any); ok {
		schema.Normalize(m)
	}
	if list, ok := args["configs"].([]any); ok {
		for _, v := range list {
			if m, ok := v.(map[string]any); ok {
				schema.Normalize(m)
			}
		}
	}
	if raw, err := json.Marshal(args); err == nil {
		c.Arguments = string(raw)
	}
	return c
}

func (e *Engine) quotePlanVersion(tool string) int {
	if tool != "quote_total" {
		return 0
	}
	if e.tools != nil && e.tools.quoteBrief != nil {
		return e.tools.quoteBrief.Version
	}
	return 0
}
func (e *Engine) quoteCatalogText() string {
	if e.tools == nil || e.tools.quoteBrief == nil {
		return tools.QuoteCatalogGuidance + tools.QuoteCatalogUnavailable
	}
	raw, _ := e.memorySnapshot()
	var m struct {
		Plan string `json:"quote_plan"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &m) != nil {
		return tools.QuoteCatalogGuidance + tools.QuoteCatalogUnavailable
	}
	b, ok := e.tools.quoteBrief.ForPlan(m.Plan)
	if !ok {
		return tools.QuoteCatalogGuidance + tools.QuoteCatalogUnavailable
	}
	return b.Text("")
}
