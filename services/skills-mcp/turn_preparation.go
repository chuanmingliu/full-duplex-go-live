package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var errPreparedTooLarge = errors.New("prepared skills exceed host response budget")

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
		if len(uris) == 0 && in.Event == "auto" {
			fullText := in.Utterance
			if len(in.Utterance) > 8192 {
				in.Utterance = in.Utterance[len(in.Utterance)-8192:]
				for !utf8.ValidString(in.Utterance) {
					in.Utterance = in.Utterance[1:]
				}
				out.ContextTruncated = true
			}
			// Leave headroom in the host's 150 ms preparation deadline for
			// resolving, transferring and validating the selected bodies.
			searchCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			found, err := x.search(searchCtx, skillRequest{Context: in.Utterance, State: in.State, Limit: 1, MaxBytes: 4096, RespectExclusions: true, ExclusionText: fullText})
			cancel()
			if err != nil && ctx.Err() == nil {
				found, err = x.search(ctx, skillRequest{Context: in.Utterance, State: in.State, SearchMode: "lexical", Limit: 1, MaxBytes: 4096, RespectExclusions: true, ExclusionText: fullText})
				found.FallbackReason = "embedding_unavailable"
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
