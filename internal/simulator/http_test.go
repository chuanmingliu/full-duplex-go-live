package simulator

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPBoundariesAndIdempotency(t *testing.T) {
	c, _, _ := testController(t)
	h := c.Handler()
	raw, _ := json.Marshal(sampleScenario())
	request := func(method, path, token, key string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/sim/v1/runs", "wrong", "key", raw); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/sim/v1/runs/unknown", "test-token", "", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/sim/v1/runs", "test-token", "key", []byte(strings.Repeat("x", MaxScenarioBytes+1))); w.Code != 413 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/sim/v1/runs", "test-token", "", raw); w.Code != 400 {
		t.Fatal(w.Code)
	}
	first := request("POST", "/sim/v1/runs", "test-token", "key", raw)
	second := request("POST", "/sim/v1/runs", "test-token", "key", raw)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("%d %d", first.Code, second.Code)
	}
	s := sampleScenario()
	s.Greeting = "different"
	changed, _ := json.Marshal(s)
	if w := request("POST", "/sim/v1/runs", "test-token", "key", changed); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/v1/opening", "test-token", "", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
