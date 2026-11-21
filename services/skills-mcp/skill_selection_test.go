package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func selectionResult(t *testing.T, response obj) skillReply {
	t.Helper()
	result, ok := response["result"]
	if !ok {
		t.Fatalf("selection RPC failed: %v", response)
	}
	data, _ := json.Marshal(result)
	var out skillReply
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSkillSelectionCompanionEndToEnd(t *testing.T) {
	f := newSelectionFixture(t)
	for _, rpc := range []func(string, obj) obj{f.rpc, func(method string, params obj) obj { return codeRPC(f, method, params) }} {
		found := selectionResult(t, rpc("skills/search", obj{"query": "quotation"}))
		if len(found.Matches) != 1 || found.Matches[0].URI != "skill://motor-quotation/SKILL.md" {
			t.Fatal(found)
		}
		args := obj{"revision": found.Revision, "uris": []string{found.Matches[0].URI}}
		described := selectionResult(t, rpc("skills/describe", args))
		if len(described.Manifests) != 1 {
			t.Fatal(described)
		}
		resolved := selectionResult(t, rpc("skills/resolve", args))
		if resolved.Status != "ok" || len(resolved.Selected) != 1 {
			t.Fatal(resolved)
		}
		loaded := selectionResult(t, rpc("skills/load", args))
		if len(loaded.Resources) != 1 {
			t.Fatal(loaded)
		}
		resource := loaded.Resources[0]
		file := resource.Manifest.Resources[0]
		if file.Size != len(resource.Text) || file.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(resource.Text))) {
			t.Fatal("integrity mismatch")
		}
		legacy := f.result("resources/read", obj{"uri": file.URI})["contents"].([]any)[0].(obj)["text"]
		if legacy != resource.Text {
			t.Fatal("companion changed existing skill content")
		}
	}
	// Execute the whole selection sequence through the real JS sandbox.
	out := codeResult(f, obj{"code": `const found = await tools.skills_search({query:"quotation"});
const args = {revision:found.revision, uris:found.matches.map(s=>s.uri)};
const resolved = await tools.skills_resolve(args);
return await tools.skills_load({...args,uris:resolved.selected});`})
	if out["isError"] == true {
		t.Fatal(out)
	}
	value := out["structuredContent"].(obj)
	if value["calls"] != float64(3) || value["result"].(obj)["status"] != "ok" {
		t.Fatal(value)
	}
	// Existing direct business tools and automatic prompt selection stay available.
	if tools := f.result("tools/list", obj{})["tools"].([]any); len(tools) != 1 || tools[0].(obj)["name"] != "quote_total" {
		t.Fatal(tools)
	}
	if messages := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "quote.compare"}})["messages"].([]any); len(messages) != 1 {
		t.Fatal(messages)
	}
}

func selectionCatalog() Catalog {
	c := defaults()
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	c.Skills = []Skill{
		{Name: "base", Description: "Base guidance", Body: "base", Enabled: true},
		{Name: "special", Description: "Special guidance", Body: "special", Enabled: true, Priority: 20, Requires: []string{"base"}},
		{Name: "alternative", Description: "Alternative guidance", Body: "alternative", Enabled: true, Priority: 10, Conflicts: []string{"base"}},
		{Name: "guarded", Description: "Guarded guidance", Body: "guarded", Enabled: true, State: map[string][]string{"consent.status": {"yes"}}},
	}
	return c
}

func TestSkillResolutionConstraints(t *testing.T) {
	c := selectionCatalog()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	x := newSkillIndex(c)
	args := skillRequest{Revision: c.Revision, URIs: []string{c.Skills[2].uri(), c.Skills[1].uri(), c.Skills[1].uri()}}
	r, err := x.resolve(context.Background(), args)
	if err != nil || !reflect.DeepEqual(r.Selected, []string{c.Skills[0].uri(), c.Skills[1].uri()}) || len(r.Rejected) != 1 || r.Rejected[0].Reason != "conflict" {
		t.Fatal(r, err)
	}
	loaded, err := x.load(context.Background(), args)
	if err != nil || loaded.Status != "unresolved" || len(loaded.Resources) != 0 {
		t.Fatal(loaded, err)
	}
	args.URIs = []string{c.Skills[1].uri()}
	args.MaxSkills = 1
	r, err = x.resolve(context.Background(), args)
	if err != nil || len(r.Selected) != 0 || r.Rejected[0].Reason != "skill_limit" {
		t.Fatal(r, err)
	}
	args.MaxSkills = 4
	c.Skills[0].Enabled = false
	x = newSkillIndex(c)
	r, err = x.resolve(context.Background(), args)
	if err != nil || len(r.Selected) != 0 || r.Rejected[0].Reason != "unavailable_dependency" {
		t.Fatal(r, err)
	}
	guard := skillRequest{Query: "guarded", Revision: c.Revision, URIs: []string{c.Skills[3].uri()}}
	if r, err := x.search(context.Background(), guard); err != nil || len(r.Matches) != 0 {
		t.Fatal(r, err)
	}
	if _, err := x.describe(context.Background(), guard); err == nil {
		t.Fatal("describe bypassed state guard")
	}
	if r, err := x.load(context.Background(), guard); err != nil || r.Status != "unresolved" {
		t.Fatal(r, err)
	}
	guard.State = map[string]string{"consent.status": "yes"}
	if r, err := x.load(context.Background(), guard); err != nil || len(r.Resources) != 1 {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.search(ctx, guard); err == nil {
		t.Fatal("cancelled search succeeded")
	}
}

func TestSkillSelectionBudgetsAndRevision(t *testing.T) {
	c := selectionCatalog()
	c.Skills[0].Body = strings.Repeat("报价\n\"", 1000)
	x := newSkillIndex(c)
	args := skillRequest{Revision: c.Revision, URIs: []string{c.Skills[1].uri()}, MaxBytes: 1024}
	r, err := x.resolve(context.Background(), args)
	if err != nil || len(r.Selected) != 0 || r.Rejected[0].Reason != "byte_budget" || !replyFits(r, 1024) {
		t.Fatal(r, err)
	}
	args.URIs = nil
	for i := 0; i < 50; i++ {
		args.URIs = append(args.URIs, fmt.Sprintf("skill://%064d/SKILL.md", i))
	}
	r, err = x.load(context.Background(), args)
	if err != nil || !replyFits(r, 1024) || !r.Truncated {
		t.Fatal(r, err)
	}
	f := newSelectionFixture(t)
	before := selectionResult(t, f.rpc("skills/search", obj{"query": "quotation"}))
	updated := f.s.catalog
	updated.Skills = append([]Skill{}, updated.Skills...)
	updated.Skills[0].Body = "new published guidance"
	if _, err := f.s.publish(updated); err != nil {
		t.Fatal(err)
	}
	for _, rpc := range []func(string, obj) obj{f.rpc, func(method string, params obj) obj { return codeRPC(f, method, params) }} {
		if response := rpc("skills/load", obj{"revision": before.Revision, "uris": []string{before.Matches[0].URI}}); response["error"] == nil {
			t.Fatal("stale revision accepted")
		}
		after := selectionResult(t, rpc("skills/search", obj{"query": "quotation"}))
		loaded := selectionResult(t, rpc("skills/load", obj{"revision": after.Revision, "uris": []string{after.Matches[0].URI}}))
		if !strings.Contains(loaded.Resources[0].Text, "new published guidance") {
			t.Fatal(loaded)
		}
	}
}

func TestSkillRelationValidation(t *testing.T) {
	c := selectionCatalog()
	c.Skills[0].Requires = []string{"special"}
	if err := c.validate(); err == nil {
		t.Fatal("cycle accepted")
	}
	c = selectionCatalog()
	c.Skills[1].Requires = []string{"missing"}
	if err := c.validate(); err == nil {
		t.Fatal("missing dependency accepted")
	}
	c = selectionCatalog()
	c.Skills[0].Conflicts = []string{"base"}
	if err := c.validate(); err == nil {
		t.Fatal("self conflict accepted")
	}
}

func TestSkillDependencyDepth(t *testing.T) {
	c := defaults()
	c.Skills = nil
	for i := 0; i < 70; i++ {
		skill := Skill{Name: fmt.Sprintf("chain-%d", i), Description: "Chain", Body: "body", Enabled: true}
		if i > 0 {
			skill.Requires = []string{fmt.Sprintf("chain-%d", i-1)}
		}
		c.Skills = append(c.Skills, skill)
	}
	if err := c.validate(); err == nil {
		t.Fatal("deep cached dependency path accepted")
	}
}

func TestSkillSelectionLargeCatalog(t *testing.T) {
	c := defaults()
	c.Skills = nil
	for i := 0; i < 10000; i++ {
		c.Skills = append(c.Skills, Skill{Name: fmt.Sprintf("catalog-%05d", i), Description: "Catalog guidance " + strings.Repeat("中文", 200), Body: "PRIVATE BODY", Enabled: true, Events: []string{"catalog.item"}})
	}
	c.Skills[4321].Description = "Unique needle 专属报价"
	f := newFixture(t, nil)
	status, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil)
	if status != 200 {
		t.Fatal("large catalog publication failed", status)
	}
	for _, query := range []string{"needle", "专属报价"} {
		found := selectionResult(t, f.rpc("skills/search", obj{"query": query}))
		if len(found.Matches) != 1 || found.Matches[0].URI != c.Skills[4321].uri() {
			t.Fatal(found)
		}
	}
	found := selectionResult(t, f.rpc("skills/search", obj{"event": "catalog.item", "max_bytes": 1024, "limit": 50}))
	if !found.Truncated || len(found.Matches) == 0 || !replyFits(found, 1024) {
		t.Fatal(found)
	}
	encoded, _ := json.Marshal(found)
	if strings.Contains(string(encoded), "PRIVATE BODY") {
		t.Fatal("search loaded bodies")
	}
	t.Logf("Published 10,000 skills; bounded metadata reply %d bytes", len(encoded))
}

func newSelectionFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t, nil)
	c := f.s.catalog
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	if _, err := f.s.publish(c); err != nil {
		t.Fatal(err)
	}
	return f
}
