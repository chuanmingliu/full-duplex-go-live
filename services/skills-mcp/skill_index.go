package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const skillSelectionExtension = "ai.liuchuanming.golive/skill-selection"
const maxSelectionBytes = 16 << 10

// An index belongs to one immutable catalog revision and is shared by endpoints.
type skillIndex struct {
	selection        skillSelectionState
	revision         int
	semantic         *semanticIndex
	pool             *semanticPool
	provider         string
	providerRevision string
	items            []indexedSkill
	byURI            map[string]int
	terms            map[string][]int
	events           map[string][]int
}

type indexedSkill struct {
	skill     Skill
	manifest  entry
	body      string
	available bool
}

type skillRequest struct {
	ExclusionText     string            `json:"-"`
	RespectExclusions bool              `json:"-"`
	Context           string            `json:"context,omitempty" jsonschema:"Relevant conversation context or summary, up to 8192 UTF-8 bytes."`
	SearchMode        string            `json:"search_mode,omitempty" jsonschema:"auto (default), semantic, or lexical. Auto uses embeddings when configured and reports lexical fallback."`
	MinSimilarity     *float64          `json:"min_similarity,omitempty" jsonschema:"Minimum cosine similarity for semantic retrieval, from -1 to 1. Default 0.3; calibrate for your model."`
	Query             string            `json:"query,omitempty" jsonschema:"Search text. Embedding search compares this text and context with skill descriptions."`
	Event             string            `json:"event,omitempty" jsonschema:"Optional exact business event filter."`
	State             map[string]string `json:"state,omitempty" jsonschema:"Host-provided confirmed state used to enforce skill guards."`
	URIs              []string          `json:"uris,omitempty" jsonschema:"Up to 50 skill URIs, in preference order for equal priorities."`
	Revision          int               `json:"revision,omitempty" jsonschema:"Expected catalog revision. Required for describe, resolve and load."`
	Limit             int               `json:"limit,omitempty" jsonschema:"Search result count, default 20 and maximum 50."`
	MaxSkills         int               `json:"max_skills,omitempty" jsonschema:"Selected skill count including dependencies, default 4 and maximum 16."`
	MaxBytes          int               `json:"max_bytes,omitempty" jsonschema:"Maximum encoded JSON reply bytes, default 12288 and range 1024 to 16384."`
}

type skillSummary struct {
	Similarity           *float64 `json:"similarity,omitempty"`
	URI                  string   `json:"uri"`
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	DescriptionTruncated bool     `json:"description_truncated,omitempty"`
	Priority             int      `json:"priority"`
	Bytes                int      `json:"bytes"`
}

type skillDecision struct {
	URI    string `json:"uri"`
	Reason string `json:"reason"`
}

type loadedSkill struct {
	Manifest entry  `json:"manifest"`
	Text     string `json:"text"`
	MIMEType string `json:"mimeType"`
}

type skillReply struct {
	EmbeddingRevision string          `json:"embedding_revision,omitempty"`
	EmbeddingProvider string          `json:"embedding_provider,omitempty"`
	Retrieval         string          `json:"retrieval,omitempty"`
	FallbackReason    string          `json:"fallback_reason,omitempty"`
	Revision          int             `json:"revision"`
	Status            string          `json:"status"`
	Matches           []skillSummary  `json:"matches,omitempty"`
	Manifests         []entry         `json:"manifests,omitempty"`
	Selected          []string        `json:"selected,omitempty"`
	Rejected          []skillDecision `json:"rejected,omitempty"`
	Resources         []loadedSkill   `json:"resources,omitempty"`
	Truncated         bool            `json:"truncated,omitempty"`
	BodyBytes         int             `json:"body_bytes,omitempty"`
}

func skillTerms(text string) []string {
	set := map[string]bool{}
	var word []rune
	var previousHan rune
	flush := func() {
		if len(word) > 0 {
			set[string(word)] = true
			word = nil
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) {
			flush()
			set[string(r)] = true
			if previousHan != 0 {
				set[string([]rune{previousHan, r})] = true
			}
			previousHan = r
		} else {
			previousHan = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				word = append(word, r)
			} else {
				flush()
			}
		}
	}
	flush()
	terms := make([]string, 0, len(set))
	for term := range set {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

func newSkillIndex(c Catalog) *skillIndex {
	x := &skillIndex{selection: c.selectionState(), revision: c.Revision, byURI: map[string]int{}, terms: map[string][]int{}, events: map[string][]int{}}
	for i, sk := range c.Skills {
		x.items = append(x.items, indexedSkill{sk, sk.entry(), sk.markdown(), c.available(sk)})
		x.byURI[sk.uri()] = i
		for _, term := range skillTerms(sk.Name + " " + sk.Description + " " + strings.Join(sk.Events, " ")) {
			x.terms[term] = append(x.terms[term], i)
		}
		seen := map[string]bool{}
		for _, event := range sk.Events {
			if !seen[event] {
				x.events[event] = append(x.events[event], i)
				seen[event] = true
			}
		}
	}
	return x
}

func (x *skillIndex) eligible(i int, state map[string]string) bool {
	item := x.items[i]
	if !item.available {
		return false
	}
	for field, allowed := range item.skill.State {
		found := false
		for _, value := range allowed {
			if state[field] == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func selectionLimit(in skillRequest, requireRevision bool, revision int) (int, error) {
	if (requireRevision && in.Revision == 0) || (in.Revision != 0 && in.Revision != revision) {
		return 0, errors.New("catalog revision missing or changed; search again")
	}
	if len(in.Context) > 8192 || len(in.Query) > 2048 || len(in.URIs) > 50 || len(in.State) > 16 || len(in.Event) > 96 {
		return 0, errors.New("selection input exceeds limits")
	}
	if in.Event != "" && !signalName.MatchString(in.Event) {
		return 0, errors.New("invalid business event")
	}
	for field, value := range in.State {
		if len(field) > 96 || len(value) > 128 {
			return 0, errors.New("state exceeds limits")
		}
	}
	for _, uri := range in.URIs {
		if len(uri) > 100 {
			return 0, errors.New("invalid skill URI")
		}
	}
	budget := in.MaxBytes
	if budget == 0 {
		budget = 12 << 10
	}
	if budget < 1024 || budget > maxSelectionBytes {
		return 0, errors.New("max_bytes must be between 1024 and 16384")
	}
	return budget, nil
}

func replyFits(reply skillReply, budget int) bool {
	body, _ := json.Marshal(reply)
	return len(body) <= budget
}

func (x *skillIndex) search(ctx context.Context, in skillRequest) (skillReply, error) {
	if err := ctx.Err(); err != nil {
		return skillReply{}, err
	}
	budget, err := selectionLimit(in, false, x.revision)
	if err != nil {
		return skillReply{}, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return skillReply{}, errors.New("limit must be between 1 and 50")
	}
	if in.SearchMode != "" && in.SearchMode != "auto" && in.SearchMode != "semantic" && in.SearchMode != "lexical" {
		return skillReply{}, errors.New("search_mode must be auto, semantic or lexical")
	}
	if in.MinSimilarity != nil && (*in.MinSimilarity < -1 || *in.MinSimilarity > 1 || math.IsNaN(*in.MinSimilarity) || math.IsInf(*in.MinSimilarity, 0)) {
		return skillReply{}, errors.New("min_similarity must be between -1 and 1")
	}
	text := strings.TrimSpace(in.Query + "\n" + in.Context)
	fallback := ""
	if in.SearchMode != "lexical" && text != "" {
		if x.semantic != nil || x.pool != nil {
			out, err := x.semanticSearch(ctx, in, text, limit, budget)
			if err == nil {
				return out, nil
			}
			if ctx.Err() != nil {
				return skillReply{}, ctx.Err()
			}
			if in.SearchMode == "semantic" {
				return skillReply{}, err
			}
			fallback = "embedding_unavailable"
		} else {
			if in.SearchMode == "semantic" {
				return skillReply{}, errors.New("semantic search is not configured")
			}
			fallback = "embedding_not_configured"
		}
	}
	if in.SearchMode == "semantic" && text == "" {
		return skillReply{}, errors.New("semantic search requires query or context")
	}
	terms := skillTerms(text)
	if len(terms) == 0 && in.Event == "" {
		return skillReply{}, errors.New("provide query or event")
	}
	scores := map[int]int{}
	for _, term := range terms {
		for _, id := range x.terms[term] {
			scores[id]++
		}
	}
	if len(terms) == 0 {
		for _, id := range x.events[in.Event] {
			scores[id] = 1
		}
	}
	ids := []int{}
	for id := range scores {
		if err := ctx.Err(); err != nil {
			return skillReply{}, err
		}
		if !x.searchEligible(id, in) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ids[i], ids[j]
		if scores[a] != scores[b] {
			return scores[a] > scores[b]
		}
		if x.items[a].skill.Priority != x.items[b].skill.Priority {
			return x.items[a].skill.Priority > x.items[b].skill.Priority
		}
		return a < b
	})
	return x.searchReply(ids, nil, limit, budget, len(ids), "lexical", fallback), nil
}

func (x *skillIndex) searchEligible(id int, in skillRequest) bool {
	text := in.ExclusionText
	if text == "" {
		text = in.Query + "\n" + in.Context
	}
	if !x.eligible(id, in.State) || (in.RespectExclusions && x.items[id].skill.excludesText(text)) {
		return false
	}
	if in.Event == "" {
		return true
	}
	for _, event := range x.items[id].skill.Events {
		if event == in.Event {
			return true
		}
	}
	return false
}

func (x *skillIndex) searchReply(ids []int, similarities map[int]float64, limit, budget, total int, retrieval, fallback string) skillReply {
	out := skillReply{Revision: x.revision, Status: "ok", Retrieval: retrieval, FallbackReason: fallback}
	if retrieval == "semantic" {
		out.EmbeddingProvider = x.provider
		out.EmbeddingRevision = x.providerRevision
	}
	for pos, id := range ids {
		if len(out.Matches) == limit {
			out.Truncated = true
			break
		}
		item := x.items[id]
		desc := item.skill.Description
		if len(desc) > 256 {
			desc = desc[:256]
			for !utf8.ValidString(desc) {
				desc = desc[:len(desc)-1]
			}
		}
		out.Matches = append(out.Matches, skillSummary{URI: item.skill.uri(), Name: item.skill.Name, Description: desc, DescriptionTruncated: desc != item.skill.Description, Priority: item.skill.Priority, Bytes: len(item.body)})
		if score, ok := similarities[id]; ok {
			out.Matches[len(out.Matches)-1].Similarity = &score
		}
		out.Truncated = pos < total-1
		if !replyFits(out, budget) {
			out.Matches = out.Matches[:len(out.Matches)-1]
			out.Truncated = true
			break
		}
	}
	if len(ids) == 0 {
		out.Status = "no_match"
	}
	return out
}

func (x *skillIndex) describe(ctx context.Context, in skillRequest) (skillReply, error) {
	budget, err := selectionLimit(in, true, x.revision)
	if err != nil {
		return skillReply{}, err
	}
	if len(in.URIs) == 0 {
		return skillReply{}, errors.New("provide skill URIs")
	}
	out := skillReply{Revision: x.revision, Status: "ok"}
	for _, uri := range in.URIs {
		if err := ctx.Err(); err != nil {
			return skillReply{}, err
		}
		id, ok := x.byURI[uri]
		if !ok || !x.eligible(id, in.State) {
			return skillReply{}, errors.New("skill unavailable for the supplied state")
		}
		out.Manifests = append(out.Manifests, x.items[id].manifest)
		if !replyFits(out, budget) {
			return skillReply{}, errors.New("description budget exceeded; request fewer URIs")
		}
	}
	return out, nil
}
