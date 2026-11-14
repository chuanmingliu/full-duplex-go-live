package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

type demoStep struct {
	Title    string `json:"title"`
	Method   string `json:"method"`
	From     string `json:"from"`
	To       string `json:"to"`
	Note     string `json:"note"`
	Display  string `json:"display"`
	Request  any    `json:"request"`
	Response any    `json:"response,omitempty"`
}

// TestCodeModeWalkthrough runs one continuous scenario through the HTTP MCP
// endpoint. Only the CRM is a fixture. The launcher saves this evidence as a
// step-through demo; the test also runs without the launcher in the normal suite.
func TestCodeModeWalkthrough(t *testing.T) {
	backendCalls := make(chan demoStep, 1)
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var request obj
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture request", 400)
			return
		}
		if r.Header.Get("Authorization") != "Bearer crm-private" || request["call"] != "demo-code-mode" || request["memory_version"] != float64(7) {
			t.Error("host context or CRM authentication lost")
		}
		memory := request["memory"].(obj)
		if memory["quote"].(obj)["交强险"] != true {
			t.Error("confirmed base plan lost")
		}
		configs := request["arguments"].(obj)["configs"].([]any)
		variants := []obj{}
		prices := map[string]int{"200万": 2850, "300万": 3010}
		for _, config := range configs {
			tier := config.(obj)["三者险"].(string)
			total, ok := prices[tier]
			if !ok {
				t.Error("unexpected demo tier", tier)
			}
			variants = append(variants, obj{
				"label": "三者险" + tier, "change": config,
				"selection": obj{"交强险": true, "三者险": tier},
				"total":     total, "diff": total - 2850,
			})
		}
		response := obj{"status": "quote", "currency": "CNY", "total": 2850,
			"memory_version": 7, "variants": variants,
			"fixture_detail": "Synthetic backend detail retained inside the VM."}
		backendCalls <- demoStep{
			Title: "Execute the existing quotation handler", Method: "POST /api/tools/quote",
			From: "Go host", To: "Synthetic CRM", Request: request, Response: response,
			Note:    "The host adds call identity and confirmed memory, then authenticates the CRM request.",
			Display: demoJSON(response),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})
	steps := []demoStep{}
	rpc := func(method string, params obj) obj {
		t.Helper()
		response := codeRPC(f, method, params)
		result, ok := response["result"].(obj)
		if !ok || result["isError"] == true {
			t.Fatalf("demo %s failed: %v", method, response)
		}
		return result
	}
	record := func(title, method, note string, params obj) obj {
		t.Helper()
		result := rpc(method, params)
		steps = append(steps, demoStep{Title: title, Method: method, From: "MCP client", To: "Skills MCP",
			Note: note, Request: params, Response: result, Display: demoJSON(result)})
		return result
	}
	topTools := record("Discover the code-mode interface", "tools/list",
		"The model receives one execution tool, execute_code.", obj{})
	if tools := topTools["tools"].([]any); len(tools) != 1 || tools[0].(obj)["name"] != "execute_code" {
		t.Fatal(topTools)
	}
	steps[len(steps)-1].Display = "execute_code({code, call?, memory?, memory_version?, catalog_revision?, timeout?})"
	discovery := record("Discover available skills", "skills/list",
		"Business guidance and code-mode instructions share the skills discovery protocol.", obj{})
	names := []string{}
	for _, item := range discovery["skills"].([]any) {
		names = append(names, item.(obj)["uri"].(string))
	}
	steps[len(steps)-1].Display = strings.Join(names, "\n")
	loadSkill := func(uri, title string) obj {
		t.Helper()
		manifest := rpc("skills/get", obj{"uri": uri})["skill"].(obj)
		resource := rpc("resources/read", obj{"uri": uri})
		body := resource["contents"].([]any)[0].(obj)["text"].(string)
		file := manifest["resources"].([]any)[0].(obj)
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body)))
		if file["uri"] != uri || file["digest"] != digest || file["size"] != float64(len(body)) {
			t.Fatal("skill integrity verification failed", uri)
		}
		steps = append(steps, demoStep{Title: title, Method: "skills/get → resources/read",
			From: "MCP client", To: "Skills MCP", Request: obj{"uri": uri},
			Response: obj{"manifest": manifest, "resource": resource}, Display: body,
			Note: fmt.Sprintf("Verified SHA-256 and %d UTF-8 bytes before using the guidance.", len(body))})
		return manifest
	}
	codeSkill := loadSkill(codeModeSkillURI, "Load and verify code-mode instructions")
	revision := codeSkill["_meta"].(obj)["ai.liuchuanming.golive/code-mode"].(obj)["catalog_revision"]
	utterance := "帮我对比200万和300万三者险报价，其他保障保持不变。"
	selection := record("Select guidance for the caller's request", "prompts/get",
		"The existing outbound-turn router matches the request to the motor-quotation skill.",
		obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": utterance, "state": "{}"}})
	messages := selection["messages"].([]any)
	if len(messages) != 1 {
		t.Fatal("expected quotation guidance", selection)
	}
	uri := messages[0].(obj)["content"].(obj)["uri"].(string)
	quoteSkill := loadSkill(uri, "Load and verify the quotation procedure")
	if quoteSkill["frontmatter"].(obj)["name"] != "motor-quotation" {
		t.Fatal(quoteSkill)
	}
	for _, discovery := range []struct{ title, note, code string }{
		{"Find the quotation tool", "search runs inside the sandbox and returns matching tool metadata.", `return await search("quotation");`},
		{"Inspect the callable signature", "describe returns types derived from the registered tool's JSON schemas.", `return await describe("tools.quote_total");`},
	} {
		result := record(discovery.title, "tools/call", discovery.note,
			obj{"name": "execute_code", "arguments": obj{"code": discovery.code}})
		if !strings.Contains(demoJSON(result), "quote_total") {
			t.Fatal("quotation tool missing from discovery", result)
		}
		steps[len(steps)-1].Display = discovery.code + "\n\n" + demoJSON(result["structuredContent"].(obj)["result"])
	}
	code := `const quote = await tools.quote_total({
  configs: [{"三者险": "200万"}, {"三者险": "300万"}]
});
if (quote.status === "need") return quote;
const options = quote.variants.map(v => ({
  coverage: v.label,
  total: v.total,
  error: v.error ?? null
}));
const priced = options.filter(v => v.error === null);
return {
  currency: quote.currency,
  options,
  difference: priced.length === 2 ? priced[1].total - priced[0].total : null
};`
	args := obj{"call": "demo-code-mode", "memory_version": 7,
		"memory":           obj{"quote": obj{"交强险": true, "三者险": "200万"}},
		"catalog_revision": revision, "code": code}
	steps = append(steps, demoStep{Title: "Submit the comparison program", Method: "tools/call execute_code",
		From: "MCP client", To: "JavaScript VM", Request: args, Display: code,
		Note: "The prewritten demo follows the skill's configs comparison procedure and preserves the selected plan."})
	result := rpc("tools/call", obj{"name": "execute_code", "arguments": args})
	select {
	case call := <-backendCalls:
		steps = append(steps, call)
	default:
		t.Fatal("quotation handler did not reach the fixture")
	}
	out := result["structuredContent"].(obj)
	if out["calls"] != float64(1) || out["result"].(obj)["difference"] != float64(160) || strings.Contains(demoJSON(result), "fixture_detail") {
		t.Fatal("unexpected model-visible result", result)
	}
	steps = append(steps, demoStep{Title: "Return the compact result to the model", Method: "execute_code result",
		From: "JavaScript VM", To: "MCP client", Request: obj{"return": "currency, options, difference"},
		Response: result, Display: demoJSON(out),
		Note: "The sandbox computes the ¥160 difference and returns the selected fields."})
	trace := obj{"scenario": utterance, "synthetic": true, "model": "Prewritten JavaScript; model generation is simulated",
		"steps": steps, "result": out, "verified": true}
	for i, step := range steps {
		t.Logf("%02d  %s  [%s]", i+1, step.Title, step.Method)
	}
	t.Log("Verified result", demoJSON(out))
	if path := os.Getenv("GOLIVE_CODE_MODE_DEMO_TRACE"); path != "" {
		if err := os.WriteFile(path, []byte(demoJSON(trace)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func demoJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(data)
}
