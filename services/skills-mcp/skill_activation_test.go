package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func activationCatalog(count int) Catalog {
	c := defaults()
	c.Skills = nil
	for i := 0; i < count; i++ {
		c.Skills = append(c.Skills, Skill{Name: fmt.Sprintf("skill-%d", i), Description: "insurance guidance", Body: "Instructions", Enabled: true})
	}
	return c
}

func TestSkillActivationSettings(t *testing.T) {
	threshold := func(n int) *int { return &n }
	for _, tt := range []struct {
		name    string
		count   int
		config  *SkillSelectionConfig
		enabled bool
	}{
		{"legacy-small-catalog", 2, nil, false},
		{"below-default", 49, nil, false},
		{"at-default", 50, nil, false},
		{"above-default", 51, nil, true},
		{"always-empty", 0, &SkillSelectionConfig{Mode: "always"}, true},
		{"off-large", 100, &SkillSelectionConfig{Mode: "off"}, false},
		{"custom-at", 3, &SkillSelectionConfig{Threshold: threshold(3)}, false},
		{"custom-above", 4, &SkillSelectionConfig{Threshold: threshold(3)}, true},
		{"zero-empty", 0, &SkillSelectionConfig{Threshold: threshold(0)}, false},
		{"zero-one", 1, &SkillSelectionConfig{Threshold: threshold(0)}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := activationCatalog(tt.count)
			c.SkillSelection = tt.config
			if err := c.validate(); err != nil {
				t.Fatal(err)
			}
			state := c.selectionState()
			if state.enabled != tt.enabled || state.available != tt.count {
				t.Fatal(state)
			}
		})
	}
	c := activationCatalog(52)
	c.Skills[0].Enabled = false
	c.Skills[1].Tool = "quote_total"
	c.Quote.Enabled = false
	if state := c.selectionState(); state.available != 50 || state.enabled {
		t.Fatal(state)
	}
	// State guards are evaluated for each request, after the catalog-level gate.
	c.Quote.Enabled = true
	c.Skills[2].State = map[string][]string{"consent.status": {"yes"}}
	if state := c.selectionState(); state.available != 51 || !state.enabled {
		t.Fatal(state)
	}
	for _, config := range []*SkillSelectionConfig{{Mode: "invalid"}, {Threshold: threshold(-1)}, {Threshold: threshold(100001)}} {
		c.SkillSelection = config
		if err := c.validate(); err == nil {
			t.Fatal("invalid activation setting accepted")
		}
	}
}

func assertSkillActivation(t *testing.T, f fixture, enabled bool, count int) {
	t.Helper()
	for _, rpc := range []func(string, obj) obj{f.rpc, func(method string, args obj) obj { return codeRPC(f, method, args) }} {
		discovery := rpc("server/discover", obj{})["result"].(obj)
		capability := discovery["capabilities"].(obj)["extensions"].(obj)[skillSelectionExtension].(obj)
		if capability["enabled"] != enabled || capability["available_skills"] != float64(count) {
			t.Fatal(capability)
		}
		methods := capability["methods"].([]any)
		if enabled && len(methods) != 4 || !enabled && len(methods) != 0 {
			t.Fatal(capability)
		}
		for _, method := range []string{"skills/search", "skills/describe", "skills/resolve", "skills/load"} {
			response := rpc(method, obj{"query": "insurance", "revision": f.s.catalog.Revision, "uris": []string{f.s.catalog.Skills[0].uri()}})
			if enabled {
				if response["error"] != nil {
					t.Fatal(response)
				}
			} else if response["error"] == nil || response["error"].(obj)["code"] != float64(-32601) {
				t.Fatal(response)
			}
		}
		if response := rpc("skills/list", obj{}); response["error"] != nil {
			t.Fatal(response)
		}
	}
	for _, uri := range []string{"codemode://tools/typescript", codeModeSkillURI} {
		response := codeRPC(f, "resources/read", obj{"uri": uri})
		data, _ := json.Marshal(response)
		if strings.Contains(string(data), "skills_search") != enabled {
			t.Fatal(uri, string(data))
		}
	}
	probe := codeResult(f, obj{"code": `return typeof tools.skills_search;`})
	if enabled {
		if probe["structuredContent"].(obj)["result"] != "function" {
			t.Fatal(probe)
		}
	} else if probe["isError"] != true || probe["structuredContent"].(obj)["calls"] != float64(0) {
		t.Fatal("disabled wrapper remained callable", probe)
	}
	// Business code execution is independently usable for every gate state.
	quote := codeResult(f, obj{"code": `return typeof tools.quote_total;`})
	if quote["structuredContent"].(obj)["result"] != "function" {
		t.Fatal(quote)
	}
}

func TestSkillActivationPublicationAndDiscovery(t *testing.T) {
	f := newFixture(t, nil)
	for _, count := range []int{50, 51, 50} {
		c := activationCatalog(count)
		c.Revision = f.s.catalog.Revision
		status, body := f.request("PUT", "/admin/catalog", "admin-private", c, nil)
		if status != 200 {
			t.Fatal(status, string(body))
		}
		assertSkillActivation(t, f, count > 50, count)
	}
	c := f.s.catalog
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	if _, err := f.s.publish(c); err != nil {
		t.Fatal(err)
	}
	assertSkillActivation(t, f, true, 50)
	reopened, err := openStore(f.s.path, f.s.backend)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.index.selection.enabled || reopened.index.selection.mode != "always" {
		t.Fatal(reopened.index.selection)
	}
	c = f.s.catalog
	c.SkillSelection = &SkillSelectionConfig{Mode: "off"}
	if _, err := f.s.publish(c); err != nil {
		t.Fatal(err)
	}
	assertSkillActivation(t, f, false, 50)
}

func TestSkillActivationSkipsEmbeddingPreparation(t *testing.T) {
	f, client := newEmbeddingFixture(t)
	s, err := openStoreWithEmbeddings(filepath.Join(t.TempDir(), "catalog.json"), backend{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if f.count() != 0 || s.index.semantic != nil {
		t.Fatal("inactive startup prepared embeddings")
	}
	c := activationCatalog(51)
	c.Revision = s.catalog.Revision
	if _, err := s.publish(c); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 || s.index.semantic == nil {
		t.Fatal("active publication did not prepare embeddings")
	}
	c = s.catalog
	c.Skills = c.Skills[:50]
	if _, err := s.publish(c); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 || s.index.semantic != nil {
		t.Fatal("inactive publication prepared embeddings")
	}
}
