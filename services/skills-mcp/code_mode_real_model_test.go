package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// realModelTrace holds exact application request/response bodies. Authentication
// headers are never recorded. It is checkpointed after every completed step.
type realModelTrace struct {
	mu      sync.Mutex
	data    obj
	steps   []demoStep
	path    string
	secrets []string
}

func (r *realModelTrace) save() {
	r.data["steps"] = r.steps
	body := demoJSON(r.data)
	for _, secret := range r.secrets {
		if secret != "" {
			body = strings.ReplaceAll(body, secret, "[credential redacted]")
		}
	}
	if err := os.WriteFile(r.path, []byte(body+"\n"), 0600); err != nil {
		panic(err)
	}
}

func (r *realModelTrace) add(step demoStep) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step)
	r.save()
}

type tracedQuoteTransport struct {
	base    http.RoundTripper
	trace   *realModelTrace
	mu      sync.Mutex
	results []obj
}

func (q *tracedQuoteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(data))
	var input obj
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	started := time.Now()
	response, err := q.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	var result obj
	if json.Unmarshal(body, &result) != nil {
		result = obj{"raw": string(body)}
	}
	q.mu.Lock()
	q.results = append(q.results, result)
	q.mu.Unlock()
	q.trace.add(demoStep{Title: "Existing CRM executes quote_total", Method: "POST /api/tools/quote",
		From: "MCP quotation handler", To: "Isolated CRM",
		Note:    fmt.Sprintf("Actual CRM code with built-in demo pricing; HTTP %d in %d ms.", response.StatusCode, time.Since(started).Milliseconds()),
		Request: input, Response: result, Display: demoJSON(result)})
	return response, nil
}

func TestCodeModeRealModel(t *testing.T) {
	model := os.Getenv("CODE_MODE_REAL_MODEL")
	if model == "" {
		t.Skip("opt-in real model walkthrough")
	}
	key := os.Getenv("CODE_MODE_LLM_KEY")
	tracePath := os.Getenv("GOLIVE_CODE_MODE_DEMO_TRACE")
	if key == "" || tracePath == "" {
		t.Fatal("model credential and trace path are required")
	}
	crmToken, err := os.ReadFile(os.Getenv("CODE_MODE_CRM_TOKEN_FILE"))
	if err != nil {
		t.Fatal("read isolated CRM credential", err)
	}
	callID := "code-mode-real-" + time.Now().UTC().Format("20060102T150405")
	memory := obj{"quote": obj{"交强险": true, "三者险": "200万"},
		"vehiclePrice": 150000, "vehicleAge": 3, "ncd": 0, "seats": 5}
	utterance := "帮我对比200万和300万三者险报价，其他保障保持不变。请给出两种方案的总价和差价。"
	trace := &realModelTrace{path: tracePath, secrets: []string{key, strings.TrimSpace(string(crmToken))}, data: obj{
		"scenario": utterance, "model": model, "synthetic": true, "verified": false,
		"real_llm": true, "pricing": "existing CRM implementation with built-in demo rules",
		"run_label": model + " generated JavaScript · existing MCP and CRM code · synthetic inputs and demo pricing",
		"call":      callID, "memory": memory, "started_at": time.Now().UTC().Format(time.RFC3339),
	}}
	trace.save()
	transport := &tracedQuoteTransport{base: http.DefaultTransport, trace: trace}
	b := backend{os.Getenv("CODE_MODE_CRM_URL"), strings.TrimSpace(string(crmToken)), &http.Client{
		Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	catalogPath := filepath.Join(t.TempDir(), "catalog.json")
	data, err := os.ReadFile(os.Getenv("CODE_MODE_CATALOG_FILE"))
	if err != nil {
		t.Fatal("read published catalog", err)
	}
	if err := os.WriteFile(catalogPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(catalogPath, b)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler(store, "admin-private", "runtime-private"))
	defer server.Close()
	f := fixture{t, store, server.URL}
	rpc := func(title, method string, params obj) obj {
		t.Helper()
		started := time.Now()
		response := codeRPC(f, method, params)
		trace.add(demoStep{Title: title, Method: method, From: "MCP client", To: "Skills MCP",
			Note:    fmt.Sprintf("Actual MCP response in %d ms.", time.Since(started).Milliseconds()),
			Request: params, Response: response, Display: demoJSON(response)})
		result, ok := response["result"].(obj)
		if !ok {
			t.Fatalf("MCP request failed: %s", method)
		}
		return result
	}
	toolList := rpc("Host discovers the execution interface", "tools/list", obj{})
	rpc("Host discovers published skills", "skills/list", obj{})
	loadSkill := func(uri string) string {
		t.Helper()
		manifest := rpc("Host retrieves skill manifest", "skills/get", obj{"uri": uri})["skill"].(obj)
		resource := rpc("Host retrieves skill instructions", "resources/read", obj{"uri": uri})
		body := resource["contents"].([]any)[0].(obj)["text"].(string)
		file := manifest["resources"].([]any)[0].(obj)
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body)))
		if file["digest"] != digest || file["size"] != float64(len(body)) || file["uri"] != uri {
			t.Fatal("skill verification failed", uri)
		}
		trace.add(demoStep{Title: "Host verifies skill content", Method: "SHA-256 + UTF-8 byte count",
			From: "Host verifier", To: "Model context", Request: file,
			Response: obj{"verified": true, "digest": digest, "bytes": len(body)}, Display: body,
			Note: "These retrieved instructions are included verbatim in the model request."})
		return body
	}
	codeSkill := loadSkill(codeModeSkillURI)
	selection := rpc("Host selects the business skill", "prompts/get", obj{"name": "outbound-turn",
		"arguments": obj{"event": "auto", "utterance": utterance, "state": "{}"}})
	guidance := ""
	for _, item := range selection["messages"].([]any) {
		uri := item.(obj)["content"].(obj)["uri"].(string)
		guidance += "\n" + loadSkill(uri)
	}
	if guidance == "" {
		t.Fatal("no business skill selected")
	}
	functions := []obj{}
	for _, item := range toolList["tools"].([]any) {
		tool := item.(obj)
		functions = append(functions, obj{"type": "function", "function": obj{
			"name": tool["name"], "description": tool["description"], "parameters": tool["inputSchema"],
		}})
	}
	system := `You are demonstrating real MCP code mode. Use the provided execute_code tool to solve the user's request.
The host already discovered and verified the following MCP skills for this turn. Follow their business procedures.
First discover relevant business tools by calling search from JavaScript and return its result. In a subsequent tool call inspect the discovered function with describe and return its result. Then write your own JavaScript to execute the quotation workflow. Use the exact returned signatures. All JavaScript must be generated by you.
The host injects the call ID, memory, memory_version and catalog_revision into every execute_code request. Supply code and optionally timeout. Do not invent prices or input facts. The provided memory is complete synthetic demo data and the backend runs built-in demo pricing rules. A comparison should use one configs request. Once a successful quotation is available, reuse it instead of repeating a business call. Handle errors using actual returned information.
Finish with a concise Chinese answer giving the two premiums and their difference, identifying demo pricing. The code environment supports plain JavaScript, top-level await and return; it does not execute TypeScript annotations.

Verified code-mode skill
` + codeSkill + "\nVerified business skill\n" + guidance + "\nHost memory\n" + demoJSON(memory)
	messages := []obj{{"role": "system", "content": system}, {"role": "user", "content": utterance}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	httpClient := &http.Client{Timeout: 100 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	searched, described := false, false
	final := ""
	for turn := 1; turn <= 10; turn++ {
		body := obj{"model": model, "messages": messages, "tools": functions, "tool_choice": "auto",
			"temperature": 0.2, "max_tokens": 4000, "stream": false}
		requestBody, _ := json.Marshal(body)
		request, err := http.NewRequestWithContext(ctx, "POST", os.Getenv("CODE_MODE_LLM_URL"), bytes.NewReader(requestBody))
		if err != nil {
			t.Fatal("invalid model URL")
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+key)
		started := time.Now()
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatal("model request failed", err)
		}
		responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		response.Body.Close()
		if err != nil {
			t.Fatal("read model response", err)
		}
		var completion obj
		if json.Unmarshal(responseBody, &completion) != nil {
			t.Fatal("invalid model response JSON")
		}
		// Decode the serialized request to freeze the message slice at this turn.
		var frozen obj
		json.Unmarshal(requestBody, &frozen)
		trace.add(demoStep{Title: fmt.Sprintf("%s generates turn %d", model, turn), Method: "POST /chat/completions",
			From: "MCP host", To: model, Request: frozen, Response: completion, Display: demoJSON(completion),
			Note: fmt.Sprintf("Actual model response; HTTP %d; %d ms. Full request includes the verified skills.", response.StatusCode, time.Since(started).Milliseconds())})
		if response.StatusCode != 200 {
			t.Fatalf("model HTTP %d; see recorded response", response.StatusCode)
		}
		choices, ok := completion["choices"].([]any)
		if !ok || len(choices) == 0 {
			t.Fatal("model response has no choices")
		}
		message := choices[0].(obj)["message"].(obj)
		messages = append(messages, message)
		calls, _ := message["tool_calls"].([]any)
		if len(calls) == 0 {
			final, _ = message["content"].(string)
			break
		}
		for _, item := range calls {
			call := item.(obj)
			function := call["function"].(obj)
			if function["name"] != "execute_code" {
				t.Fatal("model selected unadvertised tool")
			}
			var args obj
			if json.Unmarshal([]byte(function["arguments"].(string)), &args) != nil || args == nil {
				messages = append(messages, obj{"role": "tool", "tool_call_id": call["id"], "content": "Invalid JSON arguments. Supply an object with a code string."})
				continue
			}
			code, _ := args["code"].(string)
			args["call"], args["memory"], args["memory_version"], args["catalog_revision"] = callID, memory, 7, store.catalog.Revision
			trace.add(demoStep{Title: "Dispatch model-generated JavaScript", Method: "tools/call execute_code",
				From: model, To: "JavaScript VM", Request: args, Display: code,
				Note: "JavaScript copied verbatim from the model response; call context is injected by the host."})
			result := rpc("Sandbox returns execution result", "tools/call", obj{"name": "execute_code", "arguments": args})
			if result["isError"] != true {
				searched = searched || strings.Contains(code, "search(")
				described = described || strings.Contains(code, "describe(")
			}
			messages = append(messages, obj{"role": "tool", "tool_call_id": call["id"], "content": demoJSON(result)})
			t.Logf("model turn %d executed code; tool error=%v", turn, result["isError"] == true)
		}
	}
	transport.mu.Lock()
	results := append([]obj(nil), transport.results...)
	transport.mu.Unlock()
	priced := false
	for _, result := range results {
		variants, _ := result["variants"].([]any)
		priced = priced || (result["status"] == "quote" && len(variants) == 2)
	}
	trace.add(demoStep{Title: "Model explains the quotation result", Method: "assistant final response",
		From: model, To: "User", Request: obj{"task": utterance}, Response: obj{"content": final}, Display: final,
		Note: "Actual model-authored answer based on the returned quotation data."})
	trace.mu.Lock()
	trace.data["final_answer"] = final
	trace.data["backend_calls"] = len(results)
	trace.data["verified"] = searched && described && priced && final != ""
	trace.data["checks"] = obj{"model_search": searched, "model_describe": described, "two_priced_variants": priced, "final_answer": final != ""}
	trace.save()
	trace.mu.Unlock()
	if !searched || !described || !priced || final == "" {
		t.Fatal("incomplete real-model demonstration; inspect checkpointed trace")
	}
	t.Logf("real-model walkthrough passed; model=%s; CRM calls=%d", model, len(results))
	t.Log(final)
}
