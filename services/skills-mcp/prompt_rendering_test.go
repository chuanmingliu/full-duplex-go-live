package main

import "testing"

func TestPromptRenderingPublication(t *testing.T) {
	f := newFixture(t, nil)
	read := func() float64 {
		result := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "quote.revised"}})
		return result["_meta"].(obj)[slotKey].(obj)["prompt_rendering"].(obj)["version"].(float64)
	}
	if read() != 1 {
		t.Fatal("legacy catalog must default to v1")
	}
	c := f.s.catalog
	c.PromptRendering = &PromptRendering{Version: 2}
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatal(code)
	}
	if read() != 2 {
		t.Fatal("published v2 not selected")
	}
	reopened, err := openStore(f.s.path, f.s.backend)
	if err != nil || reopened.catalog.renderingVersion() != 2 {
		t.Fatal("v2 was not persisted", err)
	}
	c = f.s.catalog
	c.PromptRendering = &PromptRendering{Version: 3}
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 400 {
		t.Fatal(code)
	}
	if read() != 2 {
		t.Fatal("invalid publication changed the mode")
	}
	c.PromptRendering = &PromptRendering{Version: 1}
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatal(code)
	}
	if read() != 1 {
		t.Fatal("v1 rollback was not applied")
	}
}
