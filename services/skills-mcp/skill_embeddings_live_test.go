package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Opt-in integration against the deployed embedding model. All catalog content
// and queries are synthetic. The service under test is an isolated MCP fixture.
func TestEmbeddingLiveEndpoint(t *testing.T) {
	if os.Getenv("GOLIVE_TEST_EMBEDDINGS") != "1" {
		t.Skip("set GOLIVE_TEST_EMBEDDINGS=1 and SKILLS_EMBEDDING_* for a real endpoint")
	}
	client, err := embeddingClientFromEnv()
	if err != nil || client == nil {
		t.Fatalf("embedding configuration: %v", err)
	}
	c := liveEmbeddingCatalog()
	path := filepath.Join(t.TempDir(), "catalog.json")
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := openStoreWithEmbeddings(path, backend{}, client)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler(s, "admin-private", "runtime-private"))
	defer server.Close()
	f := fixture{t, s, server.URL}
	queries := []struct{ text, name string }{
		{"How much would it cost to insure my car?", "quote"},
		{"我想给车买保险，帮我算一下保费。", "quote"},
		{"Where can I find the service notification in WeChat?", "wechat"},
		{"微信里那条服务通知消息在哪儿找？", "wechat"},
		{"I forgot my password and cannot log in.", "password"},
		{"我忘记密码了，账号登不进去。", "password"},
		{"Where is my package and when will it arrive?", "delivery"},
		{"我的包裹现在到哪里了，什么时候送到？", "delivery"},
	}
	for _, query := range queries {
		out := selectionResult(t, f.rpc("skills/search", obj{"context": query.text, "search_mode": "semantic", "limit": 1}))
		if out.Retrieval != "semantic" || len(out.Matches) != 1 || out.Matches[0].Name != query.name {
			t.Fatalf("%s: %+v", query.text, out)
		}
		t.Logf("expected=%s actual=%s similarity=%.4f", query.name, out.Matches[0].Name, *out.Matches[0].Similarity)
	}
	result := codeResult(f, obj{"code": `const found = await tools.skills_search({context:"我想给车买保险，帮我算一下保费。",search_mode:"semantic",limit:1});
const selected = await tools.skills_resolve({revision:found.revision,uris:found.matches.map(s=>s.uri)});
return await tools.skills_load({revision:found.revision,uris:selected.selected});`})
	if result["isError"] == true {
		t.Fatal(result)
	}
	structured := result["structuredContent"].(obj)
	if structured["calls"] != float64(3) || structured["result"].(obj)["status"] != "ok" {
		t.Fatal(result)
	}
	t.Log("real model -> semantic search -> JavaScript resolve -> verified skill load passed")
}

func liveEmbeddingCatalog() Catalog {
	c := defaults()
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	c.Skills = []Skill{
		{Name: "quote", Description: "Calculate motor insurance premiums and compare coverage options.", Body: "Use quote_total to calculate prices.", Enabled: true},
		{Name: "wechat", Description: "帮助客户添加微信，以及查找微信中的服务通知消息。", Body: "Guide the customer to the service notification.", Enabled: true},
		{Name: "password", Description: "Reset forgotten account passwords and recover access to locked accounts.", Body: "Start password recovery.", Enabled: true},
		{Name: "delivery", Description: "根据订单查询物流运输状态、快递单号和预计送达时间。", Body: "Check the tracking number.", Enabled: true},
	}
	return c
}
