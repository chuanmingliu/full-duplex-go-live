package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamingGuardLiteralRulesAcrossChunkBoundaries(t *testing.T) {
	c, err := ParseStreamingGuard([]byte(`{"enabled":true,"rules":[
		{"match":"我不猜","replacement":"请您确认"},
		{"match":"我不猜。"},
		{"match":"[分析]"},
		{"match":"aab","replacement":"X"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	const input = "我没听清，[分析]我不猜。选五万？我不猜！aaab我不知道。"
	const want = "我没听清，选五万？请您确认！aX我不知道。"
	rs := []rune(input)
	for i := range rs {
		guard := c.Stream()
		got := guard.Push(string(rs[:i])) + guard.Push(string(rs[i:])) + guard.Flush()
		if got != want {
			t.Fatalf("split %d: got %q, want %q", i, got, want)
		}
	}
	guard := c.Stream()
	if got := guard.Push("保费是"); got != "保费是" {
		t.Fatalf("unrelated text was buffered: %q", got)
	}
	if got := guard.Push("我不"); got != "" {
		t.Fatalf("possible match leaked: %q", got)
	}
	if got := guard.Push("知道"); got != "我不知道" {
		t.Fatalf("rejected prefix not released: %q", got)
	}
	if got := guard.Push("我不猜") + guard.Flush(); got != "请您确认" {
		t.Fatalf("shorter rule did not match at end of response: %q", got)
	}
}

func TestFetchStreamingGuardUsesSavedVersionAndDisable(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/streaming-guard" || r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "unexpected request", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"config":{"version":7,"body":{"enabled":false,"rules":[{"match":"我不猜"}]}}}`)
	}))
	defer s.Close()
	c, err := FetchStreamingGuard(context.Background(), s.URL, "test")
	if err != nil || c.Version != 7 || c.Enabled {
		t.Fatalf("fetch = %+v, %v", c, err)
	}
	guard := c.Stream()
	if got := guard.Push("我不猜"); got != "我不猜" {
		t.Fatalf("disabled guard changed speech: %q", got)
	}
}
