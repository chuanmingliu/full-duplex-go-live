package skillsmcp

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const RecallFunction = "recall_business_guidance"

type RecallRegistration struct {
	Revision       int               `json:"revision"`
	Enabled        bool              `json:"enabled"`
	Function       string            `json:"function"`
	SelectorPrompt string            `json:"selector_prompt"`
	Candidates     map[string]string `json:"candidates"`
	rules          []recallRule
	refusal        *regexp.Regexp
}

type recallRule struct {
	include, exclude         []*regexp.Regexp
	orderMatch, orderExclude *regexp.Regexp
	guidance                 Guidance
}

type recallRoutes struct {
	Refusal string `json:"refusal"`
	Rules   []struct {
		Match        []string `json:"match"`
		Exclude      []string `json:"exclude"`
		OrderMatch   string   `json:"order_match"`
		OrderExclude string   `json:"order_exclude"`
		Resource     struct {
			Manifest Entry  `json:"manifest"`
			MIME     string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"resource"`
	} `json:"rules"`
}

func compileRecallPatterns(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) > 32 {
		return nil, errors.New("too many recall patterns")
	}
	result := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		if len(pattern) == 0 || len(pattern) > 1024 {
			return nil, errors.New("invalid recall pattern length")
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		result = append(result, compiled)
	}
	return result, nil
}

func (cfg *RecallRegistration) setRoutes(routes recallRoutes) error {
	if len(routes.Rules) == 0 {
		return nil // older catalogs can still prepare guidance asynchronously
	}
	if len(routes.Rules) > 32 {
		return errors.New("too many recall rules")
	}
	veto, err := compileRecallPatterns([]string{routes.Refusal})
	if err != nil {
		return err
	}
	cfg.refusal = veto[0]
	for _, rule := range routes.Rules {
		r := rule.Resource
		if len(r.Manifest.Resources) != 1 {
			return errors.New("invalid recall manifest")
		}
		if _, ok := cfg.Candidates[r.Manifest.Frontmatter["name"]]; !ok {
			return errors.New("unexpected direct recall candidate")
		}
		guide, err := verifyGuidance(r.Manifest.Resources[0], r.Manifest, r.Manifest.URI, r.MIME, r.Text)
		if err != nil {
			return err
		}
		match, err := compileRecallPatterns(rule.Match)
		if err != nil {
			return err
		}
		exclude, err := compileRecallPatterns(rule.Exclude)
		if err != nil {
			return err
		}
		compiled := recallRule{include: match, exclude: exclude, guidance: guide}
		if rule.OrderMatch != "" {
			patterns, err := compileRecallPatterns([]string{rule.OrderMatch, rule.OrderExclude})
			if err != nil || r.Manifest.Frontmatter["name"] != "wechat-service-notification" {
				return errors.New("invalid checkout recall rule")
			}
			compiled.orderMatch, compiled.orderExclude = patterns[0], patterns[1]
		}
		cfg.rules = append(cfg.rules, compiled)
	}
	return nil
}

// DirectRecall evaluates the installed revision against current turn text and
// projected state. No transport or model operation occurs here.
func (c *Client) DirectRecall(cfg RecallRegistration, raw, routed string, memory json.RawMessage) []Guidance {
	if len(cfg.rules) == 0 || cfg.refusal.MatchString(raw) {
		return nil
	}
	state := c.projection(memory)
	var guides []Guidance
	bytesLeft := 16 << 10
	for _, rule := range cfg.rules {
		checkout := rule.orderMatch != nil && rule.orderMatch.MatchString(strings.TrimSpace(raw)) && !rule.orderExclude.MatchString(raw)
		if (!checkout && !slices.ContainsFunc(rule.include, func(re *regexp.Regexp) bool { return re.MatchString(routed) })) ||
			slices.ContainsFunc(rule.exclude, func(re *regexp.Regexp) bool { return re.MatchString(raw) }) {
			continue
		}
		if checkout && (state["wechat.status"] == "declined" || state["wechat.status"] == "added") {
			continue
		}
		eligible := true
		for key, allowed := range rule.guidance.State {
			value := state[key]
			if checkout && key == "wechat.status" && (value == "" || value == "unknown") {
				continue
			}
			if !slices.Contains(allowed, value) {
				eligible = false
				break
			}
		}
		if eligible && len(rule.guidance.Text) <= bytesLeft {
			guides = append(guides, rule.guidance)
			bytesLeft -= len(rule.guidance.Text)
			if len(guides) == 4 {
				break
			}
		}
	}
	return guides
}

func (c *Client) RecallRegistration(ctx context.Context) (RecallRegistration, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	var result struct {
		Meta map[string]struct {
			RecallRegistration
			recallRoutes
		} `json:"_meta"`
	}
	if err := c.rpc(ctx, "prompts/get", map[string]any{"name": "working-memory-recall", "arguments": map[string]string{"route_snapshot": "1"}}, &result); err != nil {
		return RecallRegistration{}, err
	}
	wire := result.Meta[routeKey]
	cfg := wire.RecallRegistration
	if cfg.Revision < 1 || (cfg.Enabled && (cfg.Function != RecallFunction || cfg.SelectorPrompt == "" || len(cfg.SelectorPrompt) > 4096 || len(cfg.Candidates) > 32)) {
		return RecallRegistration{}, errors.New("invalid working-memory recall registration")
	}
	if cfg.Enabled {
		if err := cfg.setRoutes(wire.recallRoutes); err != nil {
			return RecallRegistration{}, err
		}
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
