package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSilenceReactionValidation(t *testing.T) {
	valid, err := ParseSilenceReaction([]byte(DefaultSilenceReaction))
	if err != nil || !valid.Enabled || valid.MinWaitMS != 8000 {
		t.Fatalf("default: %+v %v", valid, err)
	}
	for _, raw := range []string{
		`{"enabled":false}`,
		`{"enabled":true,"min_wait_ms":1000,"max_wait_ms":1000,"max_attempts":1,"templates":[" 您好？ "]}`,
	} {
		if _, err := ParseSilenceReaction([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{
		`{`, `{"enabled":true}`,
		strings.Replace(DefaultSilenceReaction, "8000", "999", 1),
		strings.Replace(DefaultSilenceReaction, "12000", "7000", 1),
		strings.Replace(DefaultSilenceReaction, "12000", "120001", 1),
		strings.Replace(DefaultSilenceReaction, `"max_attempts": 2`, `"max_attempts": 4`, 1),
		`{"enabled":true,"min_wait_ms":1000,"max_wait_ms":1000,"max_attempts":1,"templates":[" "]}`,
		`{"enabled":true,"min_wait_ms":1000,"max_wait_ms":1000,"max_attempts":1,"templates":[]}`,
	} {
		if _, err := ParseSilenceReaction([]byte(raw)); err == nil {
			t.Errorf("accepted invalid config: %s", raw)
		}
	}
}

func TestFetchSilenceReactionAuthenticatesAndPinsVersion(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/silence-reaction" || r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "bad request", 400)
			return
		}
		fmt.Fprintf(w, `{"config":{"version":7,"body":%s}}`, DefaultSilenceReaction)
	}))
	defer s.Close()
	c, err := FetchSilenceReaction(s.URL, "test-token", time.Second)
	if err != nil || c.Version != 7 || !c.Enabled {
		t.Fatalf("fetch: %+v %v", c, err)
	}
	if _, err := FetchSilenceReaction(s.URL, "wrong-token", time.Second); err == nil {
		t.Fatal("HTTP failure accepted")
	}
}
