package skillsmcp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

const RecallFunction = "recall_business_guidance"

type RecallRegistration struct {
	Revision       int               `json:"revision"`
	Enabled        bool              `json:"enabled"`
	Function       string            `json:"function"`
	SelectorPrompt string            `json:"selector_prompt"`
	Candidates     map[string]string `json:"candidates"`
}

func (c *Client) RecallRegistration(ctx context.Context) (RecallRegistration, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	var result struct {
		Meta map[string]RecallRegistration `json:"_meta"`
	}
	if err := c.rpc(ctx, "prompts/get", map[string]any{"name": "working-memory-recall", "arguments": map[string]string{}}, &result); err != nil {
		return RecallRegistration{}, err
	}
	cfg := result.Meta[routeKey]
	if cfg.Revision < 1 || (cfg.Enabled && (cfg.Function != RecallFunction || cfg.SelectorPrompt == "" || len(cfg.SelectorPrompt) > 4096 || len(cfg.Candidates) > 32)) {
		return RecallRegistration{}, errors.New("invalid working-memory recall registration")
	}
	return cfg, nil
}

// PrepareRecall uses the same MCP integrity checks as normal turn preparation,
// with a required catalog revision and an optional model-selected candidate list.
func (c *Client) PrepareRecall(ctx context.Context, revision int, ids []string, utterance, previous string, memory json.RawMessage) ([]Guidance, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	state, _ := json.Marshal(c.projection(memory))
	candidates, _ := json.Marshal(ids)
	args := map[string]string{
		"event": "auto", "utterance": utterance, "recent_context": previous, "state": string(state),
		"recall_revision": strconv.Itoa(revision), "recall_ids": string(candidates),
	}
	var result selection
	if err := c.rpc(ctx, "prompts/get", map[string]any{"name": "outbound-turn", "arguments": args}, &result); err != nil {
		return nil, err
	}
	meta := result.Meta[routeKey]
	if meta.Revision != revision || meta.Prepared == nil || !meta.AutoSelection || len(result.Messages) > 4 {
		return nil, errors.New("invalid working-memory recall preparation")
	}
	return verifyPrepared(result, meta.Prepared, nil)
}
