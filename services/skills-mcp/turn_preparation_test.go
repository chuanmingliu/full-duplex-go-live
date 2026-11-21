package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func preparedRequest(t *testing.T, f fixture, text string, state map[string]string, recent ...string) map[string]any {
	t.Helper()
	rawState, _ := json.Marshal(state)
	const version = "2026-07-28"
	params := obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": text, "state": string(rawState)}, "_meta": obj{"io.modelcontextprotocol/protocolVersion": version, "io.modelcontextprotocol/clientInfo": obj{"name": "integration-test", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": obj{"extensions": obj{turnPreparationExtension: obj{"version": "1"}}}}}
	if len(recent) > 0 {
		params["arguments"].(obj)["recent_context"] = recent[0]
	}
	code, data := f.request("POST", "/mcp", "runtime-private", obj{"jsonrpc": "2.0", "id": 1, "method": "prompts/get", "params": params}, map[string]string{"Mcp-Protocol-Version": version, "Mcp-Method": "prompts/get", "Mcp-Name": "outbound-turn"})
	var reply obj
	if json.Unmarshal(data, &reply) != nil || code != 200 || reply["error"] != nil {
		t.Fatalf("prepared request: %d %s", code, data)
	}
	return reply["result"].(map[string]any)["_meta"].(map[string]any)[slotKey].(map[string]any)
}

func TestPreparedTurnNegotiation(t *testing.T) {
	f := newFixture(t, nil)
	meta := preparedRequest(t, f, "请给我报价", map[string]string{})
	data, _ := json.Marshal(meta["prepared"])
	var got preparedTurn
	json.Unmarshal(data, &got)
	if got.Version != 1 || got.Retrieval != "rules" || len(got.Resources) != 1 || got.Resources[0].Manifest.URI != defaults().Skills[0].uri() {
		t.Fatal(string(data))
	}
	legacy := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": "请给我报价", "state": "{}"}})
	if legacy["_meta"].(map[string]any)[slotKey].(map[string]any)["prepared"] != nil {
		t.Fatal("legacy client received inline bodies")
	}
	for _, text := range []string{"你好", "不要给我报价"} {
		meta = preparedRequest(t, f, text, map[string]string{})
		raw, _ := json.Marshal(meta["prepared"])
		json.Unmarshal(raw, &got)
		if len(got.Resources) != 0 {
			t.Fatal("unexpected guidance", text)
		}
	}
}

func TestPreparedTurnSemanticAndState(t *testing.T) {
	_, client := newPoolTestEndpoint(t, "test", false)
	c := embeddingCatalog()
	c.Skills[0].Exclude = []string{"never"}
	x := newSkillIndex(c)
	if err := x.prepareEmbeddings(context.Background(), client, nil); err != nil {
		t.Fatal(err)
	}
	s := &store{catalog: c, index: x}
	s.server, s.codeServer = serversWithIndex(c, backend{}, x)
	server := httptest.NewServer(handler(s, "admin-private", "runtime-private"))
	defer server.Close()
	f := fixture{t, s, server.URL}
	meta := preparedRequest(t, f, "insurance", map[string]string{})
	raw, _ := json.Marshal(meta["prepared"])
	var got preparedTurn
	json.Unmarshal(raw, &got)
	if got.Retrieval != "semantic" || len(got.Resources) != 1 || got.Resources[0].Manifest.URI != c.Skills[0].uri() {
		t.Fatal(string(raw))
	}
	meta = preparedRequest(t, f, "never insurance", map[string]string{})
	raw, _ = json.Marshal(meta["prepared"])
	json.Unmarshal(raw, &got)
	if len(got.Resources) != 0 {
		t.Fatal("exclusion bypass", string(raw))
	}
}

func TestCodeModeBindsSkillState(t *testing.T) {
	f := newFixture(t, nil)
	c := f.s.catalog
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	if _, err := f.s.publish(c); err != nil {
		t.Fatal(err)
	}
	code := `const found=await tools.skills_search({event:"wechat.notification_help",state:{"wechat.status":"consented"}}); return found.matches?.length || 0;`
	for _, allowed := range []bool{false, true} {
		args := obj{"code": code}
		if allowed {
			args["state"] = obj{"wechat.status": "consented"}
		}
		result := codeResult(f, args)
		content, ok := result["structuredContent"].(obj)
		if !ok || result["isError"] == true {
			t.Fatal(result)
		}
		expected := float64(0)
		if allowed {
			expected = 1
		}
		if content["result"] != expected {
			t.Fatal("state binding failed", result)
		}
	}
}

func TestPreparedTurnSlowEmbeddingFallback(t *testing.T) {
	endpoint, client := newPoolTestEndpoint(t, "test", false)
	c := embeddingCatalog()
	index := newSkillIndex(c)
	if err := index.prepareEmbeddings(context.Background(), client, nil); err != nil {
		t.Fatal(err)
	}
	endpoint.delay.Store(int64(time.Second))
	started := time.Now()
	out, err := index.prepareTurn(context.Background(), turnInput{Event: "auto", Utterance: "insurance", State: map[string]string{}}, nil, nil)
	if err != nil || out.Retrieval != "lexical" || out.FallbackReason != "embedding_unavailable" || time.Since(started) > 500*time.Millisecond {
		t.Fatal(out, err)
	}
}

func TestContextualRecallThroughMCPAndPreview(t *testing.T) {
	endpoint, client := newPoolTestEndpoint(t, "test", false)
	c := embeddingCatalog()
	c.Skills = []Skill{{Name: "place-order", Description: "Complete an insurance purchase", Body: "Use the product page.", Enabled: true, Events: []string{"order.requested"}, Match: []string{"下单"}, Exclude: []string{"不买|不想下单"}},
		{Name: "other-quotation", Description: "Calculate insurance purchase premiums", Body: "Do not substitute an unrelated workflow.", Enabled: true, Events: []string{"quote.requested"}, Priority: -1}}
	x := newSkillIndex(c)
	if err := x.prepareEmbeddings(context.Background(), client, nil); err != nil {
		t.Fatal(err)
	}
	s := &store{catalog: c, index: x}
	s.server, s.codeServer = serversWithIndex(c, backend{}, x)
	server := httptest.NewServer(handler(s, "admin-private", "runtime-private"))
	defer server.Close()
	f := fixture{t, s, server.URL}
	for _, tc := range []struct {
		text, previous, retrieval string
		count                     int
	}{
		{"我就按这。", "", "semantic", 0},
		{"我就按这。", "Continue with the insurance purchase.", "semantic", 1},
		{"我暂时不想。", "Continue with the insurance purchase.", "rules", 0},
		{"你好。", "", "rules", 0},
		{"我暂时不想下单。", "Continue with the insurance purchase.", "rules", 0},
		{"怎么下单？", "irrelevant previous turn", "rules", 1},
	} {
		meta := preparedRequest(t, f, tc.text, map[string]string{}, tc.previous)
		encoded, _ := json.Marshal(meta["prepared"])
		var got preparedTurn
		if err := json.Unmarshal(encoded, &got); err != nil || got.Retrieval != tc.retrieval || len(got.Resources) != tc.count {
			t.Fatalf("%q: %s (%v)", tc.text, encoded, err)
		}
		if tc.count == 1 && got.Resources[0].Manifest.URI != c.Skills[0].uri() {
			t.Fatalf("selected the wrong skill for %q: %s", tc.text, encoded)
		}
		status, body := f.request("POST", "/admin/preview", "admin-private", obj{"event": "auto", "utterance": tc.text, "recent_context": tc.previous}, nil)
		var preview struct {
			Retrieval string
			Skills    []Skill
		}
		if err := json.Unmarshal(body, &preview); err != nil || status != 200 || preview.Retrieval != tc.retrieval || len(preview.Skills) != tc.count {
			t.Fatalf("preview %q: %d %s (%v)", tc.text, status, body, err)
		}
	}
	endpoint.fail.Store(true)
	out, err := x.prepareTurn(context.Background(), turnInput{Event: "auto", Utterance: "我就按这。", RecentContext: "insurance purchase"}, nil, nil)
	if err != nil || out.Retrieval != "lexical" || len(out.Resources) != 0 {
		t.Fatalf("lexical fallback should use only the current turn: %+v (%v)", out, err)
	}
}
