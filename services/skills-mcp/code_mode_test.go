package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	codemode "github.com/thehumanworks/codemode-go"
)

func codeRPC(f fixture, method string, params obj) obj {
	f.Helper()
	const version = "2026-07-28"
	params["_meta"] = obj{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         obj{"name": "code-mode-test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": obj{"extensions": obj{extension: obj{}}},
	}
	headers := map[string]string{"Mcp-Protocol-Version": version, "Mcp-Method": method}
	if name, ok := params["name"].(string); ok {
		headers["Mcp-Name"] = name
	}
	if uri, ok := params["uri"].(string); ok {
		headers["Mcp-Name"] = uri
	}
	status, data := f.request("POST", "/mcp/code", "runtime-private", obj{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, headers)
	var response obj
	if json.Unmarshal(data, &response) != nil || (status != 200 && response["error"] == nil) {
		f.Fatalf("code RPC %s: %d %s", method, status, data)
	}
	return response
}

func codeResult(f fixture, args obj) obj {
	f.Helper()
	response := codeRPC(f, "tools/call", obj{"name": "execute_code", "arguments": args})
	result, ok := response["result"].(obj)
	if !ok {
		f.Fatalf("code response: %v", response)
	}
	return result
}

func TestCodeModeDiscoveryAndSkill(t *testing.T) {
	f := newFixture(t, nil)
	if code, _ := f.request("POST", "/mcp/code", "admin-private", obj{}, nil); code != 401 {
		t.Fatalf("code mode auth: %d", code)
	}
	list := codeRPC(f, "tools/list", obj{})["result"].(obj)["tools"].([]any)
	if len(list) != 1 || list[0].(obj)["name"] != "execute_code" {
		t.Fatal(list)
	}
	if codeRPC(f, "tools/call", obj{"name": "quote_total", "arguments": obj{"call": "test"}})["error"] == nil {
		t.Fatal("raw tool is exposed on the code endpoint")
	}
	list = f.result("tools/list", obj{})["tools"].([]any)
	if len(list) != 1 || list[0].(obj)["name"] != "quote_total" {
		t.Fatal("original endpoint", list)
	}
	skill := codeRPC(f, "skills/get", obj{"uri": "skill://mcp-code-mode/runtime/SKILL.md"})["result"].(obj)["skill"].(obj)
	if skill["_meta"].(obj)["ai.liuchuanming.golive/code-mode"].(obj)["catalog_revision"] != float64(1) {
		t.Fatal(skill)
	}
	resource := codeRPC(f, "resources/read", obj{"uri": "skill://mcp-code-mode/runtime/SKILL.md"})["result"].(obj)
	if !strings.Contains(resource["contents"].([]any)[0].(obj)["text"].(string), "execute_code") {
		t.Fatal(resource)
	}
	for _, code := range []string{
		`return await search("quotation");`,
		`return await describe("tools.quote_total");`,
	} {
		r := codeResult(f, obj{"code": code})
		data, _ := json.Marshal(r)
		if r["isError"] == true || !strings.Contains(string(data), "quote_total") {
			t.Fatal(string(data))
		}
	}
	// Prompt routing remains available through the code endpoint.
	prompt := codeRPC(f, "prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "quote.requested", "state": "{}"}})
	if len(prompt["result"].(obj)["messages"].([]any)) != 1 {
		t.Fatal(prompt)
	}
}

func TestCodeModeChainsAndBindsContext(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var request obj
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.Header.Get("Authorization") != "Bearer crm-private" || request["call"] != "bound-call" || request["memory_version"] != float64(7) || request["memory"].(obj)["quote"].(obj)["交强险"] != true {
			t.Error("context or authorization lost", request)
		}
		n := calls.Add(1)
		if n == 2 && request["arguments"].(obj)["config"].(obj)["previous_total"] != float64(1100) {
			t.Error("chained result lost", request)
		}
		fmt.Fprintf(w, `{"status":"quote","total":%d,"private_details":"only in VM"}`, 1000+100*n)
	})
	r := codeResult(f, obj{
		"call": "bound-call", "memory_version": 7, "memory": obj{"quote": obj{"交强险": true}}, "catalog_revision": 1,
		"code": `
const first = await tools.quote_total({call: "forged", memory_version: 99, memory: {}});
const second = await tools.quote_total({config: {previous_total: first.total}});
console.log("completed", 2);
return {difference: second.total - first.total};`,
	})
	data, _ := json.Marshal(r)
	if r["isError"] == true || calls.Load() != 2 || strings.Contains(string(data), "private_details") {
		t.Fatal(string(data), calls.Load())
	}
	out := r["structuredContent"].(obj)
	if out["result"].(obj)["difference"] != float64(100) || out["calls"] != float64(2) {
		t.Fatal(out)
	}
}

func TestCodeModeValidationAndPartialFailure(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"status":"quote","total":1000}`)
	})
	for _, args := range []obj{
		{"code": `return await tools.quote_total({call:"script-cannot-supply-context"});`},
		{"code": `return await tools.quote_total({configs:"wrong-type"});`, "call": "fixture"},
		{"code": `return await tools.execute_code({code:"return 1"});`},
		{"code": `return 1`, "catalog_revision": 99},
		{"code": `return 1`, "memory_version": -1},
		{"code": `return 1`, "memory_version": 1.5},
		{"code": strings.Repeat(" ", 32<<10) + "return 1"},
		{"code": `return 1`, "memory": obj{"large": strings.Repeat("x", 64<<10)}},
	} {
		if r := codeResult(f, args); r["isError"] != true {
			t.Fatalf("invalid input succeeded: %v", r)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached CRM")
	}
	r := codeResult(f, obj{"call": "fixture", "code": `await tools.quote_total(); throw new Error("after first call");`})
	if r["isError"] != true || calls.Load() != 1 {
		t.Fatal(r, calls.Load())
	}
	if r["structuredContent"].(obj)["calls"] != float64(1) {
		t.Fatal("partial progress missing", r)
	}
}

func TestCodeModeSandboxAndLimits(t *testing.T) {
	codemode.Warm()
	f := newFixture(t, nil)
	r := codeResult(f, obj{"code": `
globalThis.marker = 42;
let blocked = false;
try { await import("qjs:std"); } catch { blocked = true; }
return {blocked, process: typeof process, fetch: typeof fetch, std: typeof std, os: typeof os, timer: typeof setTimeout};`})
	value := r["structuredContent"].(obj)["result"].(obj)
	if value["blocked"] != true {
		t.Fatal(value)
	}
	for _, name := range []string{"process", "fetch", "std", "os", "timer"} {
		if value[name] != "undefined" {
			t.Fatal(value)
		}
	}
	r = codeResult(f, obj{"code": `return typeof marker;`})
	if r["structuredContent"].(obj)["result"] != "undefined" {
		t.Fatal("VM state leaked", r)
	}
	for _, code := range []string{
		`while (true) {}`,
		`return new Uint8Array(128 * 1024 * 1024);`,
		`for (let i = 0; i < 17; i++) await search("quote");`,
	} {
		r = codeResult(f, obj{"code": code, "timeout": "200ms"})
		if r["isError"] != true {
			t.Fatal("limit not enforced", r)
		}
	}
	r = codeResult(f, obj{"code": `console.log("x".repeat(8000)); return "y".repeat(30000);`})
	if r["isError"] == true || r["structuredContent"].(obj)["truncated"] != true {
		t.Fatal(r)
	}
}

func TestCodeModeHotPublish(t *testing.T) {
	f := newFixture(t, nil)
	c := defaults()
	c.Quote.Enabled = false
	if _, err := f.s.publish(c); err != nil {
		t.Fatal(err)
	}
	r := codeResult(f, obj{"code": `return await search("quotation");`})
	if r["isError"] == true || r["structuredContent"].(obj)["result"].(obj)["total"] != float64(0) {
		t.Fatal(r)
	}
	if r = codeResult(f, obj{"call": "fixture", "code": `return await tools.quote_total({});`}); r["isError"] != true {
		t.Fatal("disabled tool executable", r)
	}
	if r = codeResult(f, obj{"catalog_revision": 1, "code": `return 1;`}); r["isError"] != true {
		t.Fatal("stale revision accepted", r)
	}
}

func TestCodeModeCancellationReachesCRM(t *testing.T) {
	codemode.Warm()
	started, cancelled := make(chan struct{}), make(chan struct{})
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	})
	// In-memory MCP transport gives the test an explicit cancellable caller.
	a, b := mcp.NewInMemoryTransports()
	ss, err := f.s.codeServer.Connect(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "cancel-test", Version: "1"}, nil).Connect(context.Background(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.CallTool(ctx, &mcp.CallToolParams{Name: "execute_code", Arguments: obj{"call": "fixture", "code": `return await tools.quote_total({});`}})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("CRM never started")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("CRM was not cancelled")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("caller did not finish")
	}
}

func TestCodeModeAdmissionAndRelease(t *testing.T) {
	var cleanups []func()
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()
	for range cap(codeModeSlots) {
		run, err := prepareCodeRun(context.Background(), obj{"code": "return 1"}, 1)
		if err != nil {
			t.Fatal(err)
		}
		cleanups = append(cleanups, run.Cleanup)
	}
	if _, err := prepareCodeRun(context.Background(), obj{"code": "return 1"}, 2); err == nil {
		t.Fatal("publication bypassed shared admission limit")
	}
	cleanups[0]()
	cleanups = cleanups[1:]
	run, err := prepareCodeRun(context.Background(), obj{"code": "return 1"}, 2)
	if err != nil {
		t.Fatal("capacity did not recover", err)
	}
	cleanups = append(cleanups, run.Cleanup)
}

func TestCodeModeDeadlineReachesCRM(t *testing.T) {
	codemode.Warm()
	cancelled := make(chan struct{})
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		close(cancelled)
	})
	result := codeResult(f, obj{"code": `return await tools.quote_total({});`, "call": "fixture", "timeout": "300ms"})
	if result["isError"] != true {
		t.Fatal(result)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("deadline did not cancel CRM")
	}
}

func TestCodeModeSkillCannotCollideWithPublishedSkill(t *testing.T) {
	f := newFixture(t, nil)
	catalog := defaults()
	catalog.Skills = append(catalog.Skills, Skill{
		Name: "mcp-code-mode", Description: "An operator-authored skill", Body: "Operator content", Enabled: true,
	})
	if _, err := f.s.publish(catalog); err != nil {
		t.Fatal(err)
	}
	for uri, expected := range map[string]string{
		"skill://mcp-code-mode/SKILL.md": "Operator content",
		codeModeSkillURI:                 "execute_code",
	} {
		resource := codeRPC(f, "resources/read", obj{"uri": uri})["result"].(obj)
		text := resource["contents"].([]any)[0].(obj)["text"].(string)
		if !strings.Contains(text, expected) {
			t.Fatal(uri, text)
		}
	}
}
