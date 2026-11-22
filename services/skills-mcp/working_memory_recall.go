package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const recallFunction = "recall_business_guidance"
const recallPromptName = "working-memory-recall"

var trailingRecallRefusal = regexp.MustCompile(`(?:^|[，。；,;… ]+)(?:我)?(?:先|暂时|现在|还是)?(?:不想|不要|不用|不办|不需要|不算|不买)(?:了)?[。！？!? ]*$`)

type workingMemoryRecall struct {
	Enabled        bool     `json:"enabled"`
	Function       string   `json:"function"`
	SelectorPrompt string   `json:"selector_prompt"`
	Candidates     []string `json:"candidates"`
}

func (c Catalog) validateWorkingMemoryRecall() error {
	cfg := c.WorkingMemoryRecall
	if cfg == nil {
		return nil
	}
	if cfg.Function != recallFunction || len(cfg.SelectorPrompt) > 4096 || len(cfg.Candidates) > 32 {
		return errors.New("invalid working-memory recall registration")
	}
	if cfg.Enabled && (strings.TrimSpace(cfg.SelectorPrompt) == "" || len(cfg.Candidates) == 0) {
		return errors.New("enabled working-memory recall requires a selector prompt and candidates")
	}
	known := map[string]bool{"tool:quote_total": true}
	for _, skill := range c.Skills {
		known[skill.Name] = true
	}
	for _, name := range cfg.Candidates {
		if !known[name] {
			return errors.New("unknown or duplicate working-memory candidate: " + name)
		}
		delete(known, name)
	}
	return nil
}

// A bounded snapshot makes direct rules available without turn-time I/O.
type recallRule struct {
	Match    []string    `json:"match"`
	Exclude  []string    `json:"exclude,omitempty"`
	Resource loadedSkill `json:"resource"`
}

func (x *skillIndex) recallRules(cfg *workingMemoryRecall) []recallRule {
	// Skills with dependency or conflict edges still use the normal resolver.
	related := make(map[string]bool)
	for _, item := range x.items {
		skill := item.skill
		for _, conflict := range skill.Conflicts {
			related[skill.Name], related[conflict] = true, true
		}
	}
	var rules []recallRule
	for _, name := range cfg.Candidates {
		i, ok := x.byURI[Skill{Name: name}.uri()]
		if !ok {
			continue
		}
		item := x.items[i]
		sk := item.skill
		if !item.available || len(sk.Events) == 0 || len(sk.Requires) > 0 || related[name] || len(sk.Match) == 0 {
			continue
		}
		runtimeRule := recallRule{Match: sk.Match, Exclude: sk.Exclude, Resource: loadedSkill{item.manifest, item.body, "text/markdown"}}
		candidate := append(rules, runtimeRule)
		if encoded, err := json.Marshal(candidate); err == nil && len(encoded) <= 240<<10 {
			rules = candidate
		}
	}
	return rules
}

func registerWorkingMemoryRecall(server *mcp.Server, c Catalog, index *skillIndex) {
	var rules []recallRule
	if cfg := c.WorkingMemoryRecall; cfg != nil && cfg.Enabled {
		rules = index.recallRules(cfg)
	}
	server.AddPrompt(&mcp.Prompt{Name: recallPromptName, Description: "Discover the published GoLive working-memory recall function and candidate descriptions.", Arguments: []*mcp.PromptArgument{
		{Name: "route_snapshot", Description: "Set to 1 to preload revision-bound direct rules for nonblocking routing."},
	}}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		meta := map[string]any{"revision": c.Revision, "enabled": false}
		if cfg := c.WorkingMemoryRecall; cfg != nil && cfg.Enabled {
			allowed := map[string]string{}
			for _, skill := range c.Skills {
				if c.available(skill) && len(skill.Events) > 0 {
					allowed[skill.Name] = skill.Description
				}
			}
			if c.Quote.Enabled {
				allowed["tool:quote_total"] = c.Quote.Description
			}
			candidates := map[string]string{}
			for _, name := range cfg.Candidates {
				if description, ok := allowed[name]; ok {
					candidates[name] = description
				}
			}
			meta["enabled"], meta["function"], meta["selector_prompt"], meta["candidates"] = true, cfg.Function, cfg.SelectorPrompt, candidates
			if req.Params.Arguments["route_snapshot"] == "1" {
				meta["rules"] = rules
				meta["refusal"] = trailingRecallRefusal.String()
			}
		}
		return &mcp.GetPromptResult{Meta: mcp.Meta{slotKey: meta}, Messages: []*mcp.PromptMessage{}}, nil
	})
}

// A non-nil selection marks background preparation, even with no model picks.
func (c Catalog) recallSelection(args map[string]string, negotiated bool) ([]string, error) {
	raw := args["recall_revision"]
	if raw == "" && args["recall_ids"] == "" {
		return nil, nil
	}
	revision, err := strconv.Atoi(raw)
	cfg := c.WorkingMemoryRecall
	if err != nil || revision != c.Revision || cfg == nil || !cfg.Enabled || args["event"] != "auto" || !negotiated {
		return nil, errors.New("invalid or superseded working-memory recall selection")
	}
	var ids []string
	if candidates := args["recall_ids"]; len(candidates) > 512 || (candidates != "" && json.Unmarshal([]byte(candidates), &ids) != nil) || len(ids) > 4 {
		return nil, errors.New("invalid working-memory candidate IDs")
	}
	for _, id := range ids {
		if !slices.Contains(cfg.Candidates, id) {
			return nil, errors.New("unregistered working-memory candidate")
		}
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

func (x *skillIndex) mergeWorkingMemoryRecall(ctx context.Context, cfg *workingMemoryRecall, in turnInput, prepared *preparedTurn, names []string) error {
	if nonWorkflowTurn.MatchString(strings.TrimSpace(in.Utterance)) || prepared.FallbackReason == "rule_excluded" {
		return nil
	}
	uris := make([]string, 0, len(prepared.Resources)+len(names))
	for _, resource := range prepared.Resources {
		uris = append(uris, resource.Manifest.URI)
	}
	addSkill := func(name string, tool string) {
		uri := Skill{Name: name}.uri()
		i, ok := x.byURI[uri]
		if !ok || (tool != "" && x.items[i].skill.Tool != tool) || len(x.items[i].skill.Events) == 0 {
			return
		}
		closure, reason := x.closure(ctx, uri, in.State)
		if reason == "" && !slices.ContainsFunc(closure, func(id int) bool {
			return x.items[id].skill.excludesText(in.Utterance)
		}) {
			uris = append(uris, uri)
		}
	}
	for _, name := range names {
		if name == "tool:quote_total" {
			// The host already registers this schema and its confirmation policy.
			for _, candidate := range cfg.Candidates {
				addSkill(candidate, "quote_total")
			}
		} else {
			addSkill(name, "")
		}
	}
	if len(uris) == 0 {
		return nil
	}
	resolved, err := x.resolve(ctx, skillRequest{URIs: uris, Revision: x.revision, State: in.State, MaxSkills: 4, MaxBytes: maxSelectionBytes})
	if err != nil {
		return err
	}
	ids := make([]int, 0, len(resolved.Selected))
	for _, uri := range resolved.Selected {
		ids = append(ids, x.byURI[uri])
	}
	prepared.Resources = x.loaded(ids).Resources
	return nil
}
