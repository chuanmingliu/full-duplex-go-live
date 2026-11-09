package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventResponsePublication(t *testing.T) {
	f := newFixture(t, nil)
	read := func() struct {
		Revision int
		Event    string
		EventResponse
	} { result := f.result("resources/read", obj{"uri": "golive://event-responses/addV"}); text := result["contents"].([]any)[0].(obj)["text"].(string); var response struct {
		Revision int
		Event    string
		EventResponse
	}; if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatal(err)
	}; return response }
	if read().Enabled {
		t.Fatal("legacy catalog enabled callback speech")
	}
	c := f.s.catalog
	c.EventResponses = map[string]EventResponse{"addV": {Enabled: true, Templates: []string{"微信已经添加成功了。"}}}
	code, data := f.request("PUT", "/admin/catalog", "runtime-private", c, nil)
	if code != 401 {
		t.Fatalf("runtime credential edited catalog: %d", code)
	}
	code, data = f.request("PUT", "/admin/catalog", "admin-private", c, nil)
	if code != 200 {
		t.Fatalf("publish: %d %s", code, data)
	}
	got := read()
	if !got.Enabled || got.Event != "addV" || got.Revision != c.Revision+1 || got.Templates[0] != c.EventResponses["addV"].Templates[0] {
		t.Fatalf("published binding missing: %+v", got)
	}
	code, _ = f.request("PUT", "/admin/catalog", "admin-private", c, nil)
	if code != 409 {
		t.Fatalf("lost update accepted: %d", code)
	}
	reopened, err := openStore(f.s.path, f.s.backend)
	if err != nil || !reopened.catalog.EventResponses["addV"].Enabled {
		t.Fatal("binding did not persist", err)
	}
	c = f.s.catalog
	c.EventResponses = map[string]EventResponse{"addV": {Templates: []string{"修改后的确认。"}}}
	if _, err := f.s.publish(c); err != nil {
		t.Fatal(err)
	}
	if read().Enabled {
		t.Fatal("disable was not live")
	}
}

func TestEventResponseValidation(t *testing.T) {
	cases := []map[string]EventResponse{
		{"unknown": {}}, {"addV": {Enabled: true}},
		{"addV": {Templates: []string{""}}}, {"addV": {Templates: []string{" a"}}},
		{"addV": {Templates: []string{"a\nb"}}}, {"addV": {Templates: []string{"a", "a"}}},
		{"addV": {Templates: []string{strings.Repeat("中", 301)}}},
	}
	for _, binding := range cases {
		if validateEventResponses(binding) == nil {
			t.Fatalf("accepted invalid binding: %+v", binding)
		}
	}
	if err := validateEventResponses(map[string]EventResponse{"addV": {Enabled: true, Templates: []string{strings.Repeat("中", 300)}}}); err != nil {
		t.Fatal(err)
	}
}
