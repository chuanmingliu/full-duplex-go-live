package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type obj = map[string]any
type fixture struct {
	*testing.T
	s   *store
	url string
}

func newFixture(t *testing.T, crm http.HandlerFunc) fixture {
	crmServer := httptest.NewServer(crm)
	t.Cleanup(crmServer.Close)
	s, err := openStore(filepath.Join(t.TempDir(), "catalog.json"), backend{crmServer.URL, "crm-private", crmServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	svr := httptest.NewServer(handler(s, "admin-private", "runtime-private"))
	t.Cleanup(svr.Close)
	return fixture{t, s, svr.URL}
}
func (f fixture) request(method, path, token string, params any, headers map[string]string) (int, []byte) {
	f.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		f.Fatal(err)
	}
	r, err := http.NewRequest(method, f.url+path, bytes.NewReader(body))
	if err != nil {
		f.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		f.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		f.Fatal(err)
	}
	return res.StatusCode, data
}
func (f fixture) rpc(method string, p obj) obj {
	f.Helper()
	const v = "2026-07-28"
	p["_meta"] = obj{"io.modelcontextprotocol/protocolVersion": v, "io.modelcontextprotocol/clientInfo": obj{"name": "test", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": obj{"extensions": obj{extension: obj{}}}}
	headers := map[string]string{"Mcp-Protocol-Version": v, "Mcp-Method": method}
	if method == "tools/call" || method == "prompts/get" {
		headers["Mcp-Name"] = p["name"].(string)
	}
	if method == "resources/read" {
		headers["Mcp-Name"] = p["uri"].(string)
	}
	code, data := f.request("POST", "/mcp", "runtime-private", obj{"jsonrpc": "2.0", "id": 1, "method": method, "params": p}, headers)
	var out obj
	if json.Unmarshal(data, &out) != nil || (code != 200 && out["error"] == nil) {
		f.Fatalf("RPC %s: %d %s", method, code, data)
	}
	return out
}
func (f fixture) result(method string, p obj) obj {
	f.Helper()
	out := f.rpc(method, p)
	r, ok := out["result"].(obj)
	if !ok {
		f.Fatalf("%s: %v", method, out)
	}
	return r
}
func TestDiscoveryAndPublish(t *testing.T) {
	f := newFixture(t, nil)
	uri := defaults().Skills[0].uri()
	disc := f.result("server/discover", obj{})
	if _, ok := disc["capabilities"].(obj)["extensions"].(obj)[extension]; !ok {
		t.Fatal(disc)
	}
	list := f.result("skills/list", obj{})
	ent := f.result("skills/get", obj{"uri": uri})["skill"].(obj)
	a, _ := json.Marshal(list["skills"].([]any)[0])
	b, _ := json.Marshal(ent)
	if !bytes.Equal(a, b) || list["resultType"] != "complete" {
		t.Fatal(list)
	}
	read := f.result("resources/read", obj{"uri": uri})["contents"].([]any)[0].(obj)["text"].(string)
	manifest := ent["resources"].([]any)[0].(obj)
	if manifest["digest"] != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(read))) || manifest["size"] != float64(len(read)) {
		t.Fatal(manifest)
	}
	var index struct{ Skills []struct{ URL string } }
	indexData := f.result("resources/read", obj{"uri": "skill://index.json"})["contents"].([]any)[0].(obj)["text"].(string)
	if json.Unmarshal([]byte(indexData), &index) != nil || len(index.Skills) != 2 || index.Skills[0].URL != uri {
		t.Fatal(index)
	}
	c := defaults()
	c.Skills[0].Description = "Ask about a revised plan"
	c.Quote.Description = "Reprice the confirmed selection"
	if code, data := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatalf("%d %s", code, data)
	}
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 409 {
		t.Fatal(code)
	}
	if got := f.result("tools/list", obj{})["tools"].([]any)[0].(obj)["description"]; got != c.Quote.Description {
		t.Fatal(got)
	}
	ent = f.result("skills/get", obj{"uri": uri})["skill"].(obj)
	if ent["frontmatter"].(obj)["description"] != c.Skills[0].Description {
		t.Fatal(ent)
	}
	saved, err := openStore(f.s.path, f.s.backend)
	if err != nil || saved.catalog.Revision != 2 || saved.catalog.Skills[0].Description != c.Skills[0].Description {
		t.Fatal("restart", err)
	}
	c.Revision = 2
	c.Quote.Enabled = false
	if code, data := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatalf("%d %s", code, data)
	}
	if len(f.result("skills/list", obj{})["skills"].([]any)) != 1 || len(f.result("tools/list", obj{})["tools"].([]any)) != 0 {
		t.Fatal("disabled catalog still visible")
	}
	if got := f.rpc("skills/get", obj{"uri": uri})["error"].(obj)["code"]; got != float64(-32602) {
		t.Fatal(got)
	}
	if f.rpc("resources/read", obj{"uri": uri})["error"] == nil {
		t.Fatal("disabled skill readable")
	}
}
func TestQuoteEnvelope(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer crm-private" || r.Method != "POST" {
			t.Error("CRM auth or method")
		}
		var v obj
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Error(err)
			return
		}
		if v["tool"] != "quote_total" || v["call"] != "fixture" || v["memory_version"] != float64(7) {
			t.Error("lost call context")
		}
		if v["memory"].(obj)["quote"].(obj)["交强险"] != true || v["arguments"].(obj)["config"].(obj)["三者险"] != "300万" || len(v["arguments"].(obj)["configs"].([]any)) != 2 {
			t.Error("lost quotation context")
		}
		io.WriteString(w, `{"status":"quote","total":1250,"currency":"CNY","memory_version":7}`)
	})
	args := obj{"call": "fixture", "memory_version": 7, "memory": obj{"quote": obj{"交强险": true}}, "config": obj{"三者险": "300万"}, "configs": []obj{{"三者险": "200万"}, {"三者险": "500万"}}}
	result := f.result("tools/call", obj{"name": "quote_total", "arguments": args})
	if result["structuredContent"].(obj)["total"] != float64(1250) {
		t.Fatal(result)
	}
}
func TestAuthErrorsAndInvalidSettings(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private upstream details", 502) })
	if code, _ := f.request("GET", "/admin/catalog", "runtime-private", nil, nil); code != 401 {
		t.Fatal(code)
	}
	if code, _ := f.request("POST", "/mcp", "admin-private", nil, nil); code != 401 {
		t.Fatal(code)
	}
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", defaults(), map[string]string{"Origin": "https://other.example"}); code != 403 {
		t.Fatal(code)
	}
	c := defaults()
	c.Skills[0].Name = "../escape"
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 400 {
		t.Fatal(code)
	}
	r := f.result("tools/call", obj{"name": "quote_total", "arguments": obj{"call": "test"}})
	msg, _ := json.Marshal(r)
	if r["isError"] != true || bytes.Contains(msg, []byte("private upstream details")) {
		t.Fatal(r)
	}
}

func TestProgressiveTurnSelectionAndHotEdit(t *testing.T) {
	f := newFixture(t, nil)
	selectTurn := func(event, state string) obj {
		return f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": event, "state": state}})
	}
	stages := []struct{ event, state, skill string }{
		{"conversation.normal", "{}", ""},
		{"wechat.notification_help", "{}", ""},
		{"wechat.add_requested", `{"wechat.status":"consented"}`, "wechat-service-notification"},
		{"wechat.notification_help", `{"wechat.status":"awaiting_confirmation"}`, "wechat-service-notification"},
		{"wechat.notification_help", `{"wechat.status":"added"}`, ""},
		{"wechat.add_requested", `{"wechat.status":"declined"}`, ""},
		{"quote.revised", "{}", "motor-quotation"},
	}
	for _, x := range stages {
		out := selectTurn(x.event, x.state)
		msgs := out["messages"].([]any)
		if x.skill == "" {
			if len(msgs) != 0 {
				t.Fatal(x, msgs)
			}
			continue
		}
		if len(msgs) != 1 {
			t.Fatal(x, msgs)
		}
		link := msgs[0].(obj)["content"].(obj)
		if link["type"] != "resource_link" || link["name"] != x.skill || link["text"] != nil {
			t.Fatal(link)
		}
	}
	c := defaults()
	c.Skills[1].Body = "Changed workflow"
	c.Skills[1].Events = []string{"wechat.new_flow"}
	if code, body := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if len(selectTurn("wechat.notification_help", stages[3].state)["messages"].([]any)) != 0 {
		t.Fatal("stale route")
	}
	result := selectTurn("wechat.new_flow", stages[3].state)
	link := result["messages"].([]any)[0].(obj)["content"].(obj)
	file := f.result("resources/read", obj{"uri": link["uri"]})["contents"].([]any)[0].(obj)["text"].(string)
	if !bytes.Contains([]byte(file), []byte("Changed workflow")) || link["_meta"].(obj)[slotKey].(obj)["digest"] != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(file))) {
		t.Fatal("stale content")
	}
}
