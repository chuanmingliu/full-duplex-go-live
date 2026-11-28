package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	codemode "github.com/thehumanworks/codemode-go"
)

type comparisonTransport struct {
	mu       sync.Mutex
	requests []obj
}

func (c *comparisonTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(body))
	var input obj
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}
	started := time.Now()
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	var output obj
	json.Unmarshal(data, &output)
	c.mu.Lock()
	c.requests = append(c.requests, obj{"request": input, "response": output, "http_status": response.StatusCode, "elapsed_ms": float64(time.Since(started).Microseconds()) / 1000})
	c.mu.Unlock()
	return response, nil
}

// Opt-in harness uses the actual two MCP endpoints and existing CRM adapter.
// The Python client owns model requests and independent answer verification.
func TestCodeModeComparison(t *testing.T) {
	script := os.Getenv("CODE_MODE_COMPARISON_SCRIPT")
	if script == "" {
		t.Skip("opt-in real model comparison")
	}
	token, err := os.ReadFile(os.Getenv("CODE_MODE_CRM_TOKEN_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	transport := &comparisonTransport{}
	b := backend{os.Getenv("CODE_MODE_CRM_URL"), strings.TrimSpace(string(token)), &http.Client{Transport: transport}}
	catalog, err := os.ReadFile(os.Getenv("CODE_MODE_CATALOG_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, catalog, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(path, b)
	if err != nil {
		t.Fatal(err)
	}
	codemode.Warm()
	mux := http.NewServeMux()
	mux.Handle("/demo/metrics", protect("runtime-private", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		json.NewEncoder(w).Encode(transport.requests)
	})))
	mux.Handle("/", handler(store, "admin-private", "runtime-private"))
	server := httptest.NewServer(mux)
	defer server.Close()
	command := exec.Command("python3", script)
	command.Env = append(os.Environ(), "CODE_MODE_MCP_URL="+server.URL)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
}
