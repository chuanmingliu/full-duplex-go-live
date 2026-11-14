package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

func validateSkillRelations(c Catalog) error {
	byName := map[string]Skill{}
	for _, sk := range c.Skills {
		byName[sk.Name] = sk
	}
	for _, sk := range c.Skills {
		if sk.Priority < -1000 || sk.Priority > 1000 || len(sk.Requires) > 16 || len(sk.Conflicts) > 16 {
			return errors.New("skill priority or relation limit exceeded")
		}
		for _, name := range append(append([]string{}, sk.Requires...), sk.Conflicts...) {
			if _, ok := byName[name]; !ok || name == sk.Name {
				return fmt.Errorf("invalid skill relation from %s to %s", sk.Name, name)
			}
		}
	}
	heights := map[string]int{}
	active := map[string]bool{}
	var visit func(string, int) (int, error)
	visit = func(name string, depth int) (int, error) {
		if depth > 64 {
			return 0, errors.New("skill dependency depth exceeds 64")
		}
		if active[name] {
			return 0, errors.New("cyclic skill dependencies")
		}
		if height, ok := heights[name]; ok {
			return height, nil
		}
		active[name] = true
		height := 0
		for _, dependency := range byName[name].Requires {
			child, err := visit(dependency, depth+1)
			if err != nil {
				return 0, err
			}
			height = max(height, child+1)
		}
		if height > 64 {
			return 0, errors.New("skill dependency depth exceeds 64")
		}
		delete(active, name)
		heights[name] = height
		return height, nil
	}
	for _, sk := range c.Skills {
		if _, err := visit(sk.Name, 0); err != nil {
			return err
		}
	}
	return nil
}

func (x *skillIndex) closure(ctx context.Context, uri string, state map[string]string) ([]int, string) {
	ids := []int{}
	seen := map[int]bool{}
	var visit func(string) string
	visit = func(uri string) string {
		if ctx.Err() != nil {
			return "cancelled"
		}
		id, ok := x.byURI[uri]
		if !ok || !x.eligible(id, state) {
			return "unavailable_dependency"
		}
		if seen[id] {
			return ""
		}
		seen[id] = true
		if len(seen) > 64 {
			return "dependency_limit"
		}
		for _, name := range x.items[id].skill.Requires {
			if reason := visit("skill://" + name + "/SKILL.md"); reason != "" {
				return reason
			}
		}
		ids = append(ids, id)
		return ""
	}
	reason := visit(uri)
	return ids, reason
}

func (x *skillIndex) conflicts(ids []int) bool {
	names := map[string]bool{}
	for _, id := range ids {
		names[x.items[id].skill.Name] = true
	}
	for _, id := range ids {
		for _, name := range x.items[id].skill.Conflicts {
			if names[name] {
				return true
			}
		}
	}
	return false
}

func (x *skillIndex) loaded(ids []int) skillReply {
	out := skillReply{Revision: x.revision, Status: "ok"}
	for _, id := range ids {
		item := x.items[id]
		out.Selected = append(out.Selected, item.skill.uri())
		out.Resources = append(out.Resources, loadedSkill{item.manifest, item.body, "text/markdown"})
		out.BodyBytes += len(item.body)
	}
	return out
}

func (x *skillIndex) resolve(ctx context.Context, in skillRequest) (skillReply, error) {
	budget, err := selectionLimit(in, true, x.revision)
	if err != nil {
		return skillReply{}, err
	}
	if len(in.URIs) == 0 {
		return skillReply{}, errors.New("provide candidate skill URIs")
	}
	limit := in.MaxSkills
	if limit == 0 {
		limit = 4
	}
	if limit < 1 || limit > 16 {
		return skillReply{}, errors.New("max_skills must be between 1 and 16")
	}
	// Higher priority roots win. Equal priorities retain the caller's ranking.
	uris := append([]string{}, in.URIs...)
	priority := func(uri string) int {
		if id, ok := x.byURI[uri]; ok {
			return x.items[id].skill.Priority
		}
		return 0
	}
	sort.SliceStable(uris, func(i, j int) bool { return priority(uris[i]) > priority(uris[j]) })
	out := skillReply{Revision: x.revision, Status: "ok"}
	selected := []int{}
	seenRoots := map[string]bool{}
	for _, uri := range uris {
		if err := ctx.Err(); err != nil {
			return skillReply{}, err
		}
		if seenRoots[uri] {
			continue
		}
		seenRoots[uri] = true
		closure, reason := x.closure(ctx, uri, in.State)
		next := append([]int{}, selected...)
		included := map[int]bool{}
		for _, id := range next {
			included[id] = true
		}
		for _, id := range closure {
			if !included[id] {
				next = append(next, id)
				included[id] = true
			}
		}
		if reason == "" && x.conflicts(next) {
			reason = "conflict"
		}
		if reason == "" && len(next) > limit {
			reason = "skill_limit"
		}
		if reason == "" && !replyFits(x.loaded(next), budget) {
			reason = "byte_budget"
		}
		if reason != "" {
			out.Rejected = append(out.Rejected, skillDecision{uri, reason})
			continue
		}
		selected = next
	}
	if err := ctx.Err(); err != nil {
		return skillReply{}, err
	}
	loaded := x.loaded(selected)
	out.Selected, out.BodyBytes = loaded.Selected, loaded.BodyBytes
	if len(out.Rejected) > 0 {
		out.Status = "partial"
	}
	if len(selected) == 0 {
		out.Status = "no_match"
	}
	// Diagnostic lists are bounded too. Truncation never changes selection.
	for !replyFits(out, budget) && len(out.Rejected) > 0 {
		out.Rejected = out.Rejected[:len(out.Rejected)-1]
		out.Truncated = true
	}
	return out, nil
}

func (x *skillIndex) load(ctx context.Context, in skillRequest) (skillReply, error) {
	out, err := x.resolve(ctx, in)
	if err != nil {
		return skillReply{}, err
	}
	if out.Status != "ok" {
		out.Status = "unresolved"
		out.Selected = nil
		out.BodyBytes = 0
		budget, _ := selectionLimit(in, true, x.revision)
		for !replyFits(out, budget) && len(out.Rejected) > 0 {
			out.Rejected = out.Rejected[:len(out.Rejected)-1]
			out.Truncated = true
		}
		return out, nil
	}
	ids := make([]int, 0, len(out.Selected))
	for _, uri := range out.Selected {
		ids = append(ids, x.byURI[uri])
	}
	return x.loaded(ids), nil
}
