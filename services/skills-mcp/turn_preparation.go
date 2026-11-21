package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var errPreparedTooLarge = errors.New("prepared skills exceed host response budget")

// These complete turns do not request a workflow. They must not inherit
// business intent solely from an older utterance.
var nonWorkflowTurn = regexp.MustCompile(`^(?:(?:您|你)好|喂|谢谢(?:再见)?|再见|(?:我)?(?:暂时|先|现在)?(?:不想|不要|不用|不买|不办|不需要)(?:了)?|嗯+|哦+)[，。！？、,.?! ]*$`)

const turnPreparationExtension = "ai.liuchuanming.golive/turn-preparation"

type preparedTurn struct {
	Version           int           `json:"version"`
	Resources         []loadedSkill `json:"resources"`
	Retrieval         string        `json:"retrieval"`
	EmbeddingProvider string        `json:"embedding_provider,omitempty"`
	EmbeddingRevision string        `json:"embedding_revision,omitempty"`
	ContextTruncated  bool          `json:"context_truncated,omitempty"`
	FallbackReason    string        `json:"fallback_reason,omitempty"`
}

func supportsPreparedTurn(req *mcp.GetPromptRequest) bool {
	caps := req.ClientCapabilities()
	if caps == nil {
		return false
	}
	settings, ok := caps.Extensions[turnPreparationExtension].(map[string]any)
	return ok && settings["version"] == "1"
}

// A negotiated response carries verified-load inputs from one immutable
// snapshot. Existing clients continue using the resource-link protocol.
func (x *skillIndex) prepareTurn(ctx context.Context, in turnInput, matched []Skill, matchErr error) (*preparedTurn, error) {
	if matchErr != nil {
		return nil, matchErr
	}
	out := &preparedTurn{Version: 1, Resources: []loadedSkill{}, Retrieval: "rules"}
	ids := []int{}
	for _, sk := range matched {
		ids = append(ids, x.byURI[sk.uri()])
	}
	if !x.selection.enabled {
		if len(ids) > 4 {
			return nil, errors.New("too many matching skills")
		}
		out.Resources = x.loaded(ids).Resources
	} else {
		uris := []string{}
		for _, id := range ids {
			uris = append(uris, x.items[id].skill.uri())
		}
		if len(uris) == 0 && in.Event == "auto" && strings.TrimSpace(in.Utterance) != "" {
			if nonWorkflowTurn.MatchString(strings.TrimSpace(in.Utterance)) {
				out.FallbackReason = "non_workflow_turn"
				return out, nil
			}

			for id, item := range x.items {
				skill := item.skill
				if x.eligible(id, in.State) && len(skill.Events) > 0 && skill.excludesText(in.Utterance) && skill.matchesPositiveRule(in.Utterance) {
					// An explicit rule veto is a direct routing decision. Do
					// not replace a declined workflow with a semantic neighbor.
					out.FallbackReason = "rule_excluded"
					return out, nil
				}
			}
			fullText := in.Utterance
			if len(in.Utterance) > 8192 {
				in.Utterance = in.Utterance[len(in.Utterance)-8192:]
				for !utf8.ValidString(in.Utterance) {
					in.Utterance = in.Utterance[1:]
				}
				out.ContextTruncated = true
			}
			query := in.Utterance
			if context := strings.TrimSpace(in.RecentContext); context != "" {
				query = "Previous caller turn: " + context + "\nCurrent caller turn: " + query
			}
			// Leave headroom in the host's 150 ms preparation deadline for
			// resolving, transferring and validating the selected bodies.
			searchCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			found, err := x.search(searchCtx, skillRequest{Context: query, SearchMode: "semantic", State: in.State, Limit: 1, MaxBytes: 4096, RespectExclusions: true, ExclusionText: fullText})
			cancel()
			if err != nil && ctx.Err() == nil {
				found, err = x.search(ctx, skillRequest{Context: in.Utterance, State: in.State, SearchMode: "lexical", Limit: 1, MaxBytes: 4096, RespectExclusions: true, ExclusionText: fullText})
				found.FallbackReason = "embedding_unavailable"
				if x.semantic == nil && x.pool == nil {
					found.FallbackReason = "embedding_not_configured"
				}
			}
			if err != nil {
				return nil, err
			}
			out.Retrieval = found.Retrieval
			out.EmbeddingProvider = found.EmbeddingProvider
			out.EmbeddingRevision = found.EmbeddingRevision
			out.FallbackReason = found.FallbackReason
			for _, item := range found.Matches {
				uris = append(uris, item.URI)
			}
		}
		if len(uris) > 0 {
			loaded, err := x.load(ctx, skillRequest{URIs: uris, Revision: x.revision, State: in.State, MaxSkills: 4, MaxBytes: maxSelectionBytes})
			if err != nil {
				return nil, err
			}
			if loaded.Status != "ok" {
				return nil, errors.New("selected skills cannot be loaded together")
			}
			out.Resources = loaded.Resources
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > 400<<10 {
		return nil, errPreparedTooLarge
	}
	return out, nil
}

// selectedSkills converts a prepared result back into routing objects for the
// MCP resource links and the operator preview.
func (x *skillIndex) selectedSkills(p *preparedTurn) ([]Skill, []string) {
	skills := make([]Skill, 0, len(p.Resources))
	tools := []string{}
	for _, resource := range p.Resources {
		skill := x.items[x.byURI[resource.Manifest.URI]].skill
		skills = append(skills, skill)
		if skill.Tool != "" && !slices.Contains(tools, skill.Tool) {
			tools = append(tools, skill.Tool)
		}
	}
	return skills, tools
}
