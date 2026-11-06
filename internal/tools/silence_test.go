package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSilenceReactionValidation(t *testing.T) {
	valid, err := ParseSilenceReaction([]byte(DefaultSilenceReaction))
	if err != nil || !valid.Enabled || len(valid.Rounds) != 2 || valid.Rounds[0].MinWaitMS != 8000 {
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
		`{"enabled":true,"min_wait_ms":1000,"max_wait_ms":1000,"max_attempts":4,"templates":["您好"]}`,
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

func TestSilenceReactionOrderedRoundsAndLegacy(t *testing.T) {
	c, err := ParseSilenceReaction([]byte(DefaultSilenceReaction))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r, ok := c.Round(i)
		if !ok || r.Templates[0] != c.Rounds[i].Templates[0] {
			t.Fatal("round order lost")
		}
	}
	if _, ok := c.Round(2); ok {
		t.Fatal("exhausted rounds cycled")
	}
	if _, ok := c.Round(-1); ok {
		t.Fatal("negative round accepted")
	}
	legacy, err := ParseSilenceReaction([]byte(`{"enabled":true,"min_wait_ms":4000,"max_wait_ms":6000,"max_attempts":2,"templates":[" 您在吗？ "]}`))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r, ok := legacy.Round(i)
		if !ok || r.MinWaitMS != 4000 || r.Templates[0] != "您在吗？" {
			t.Fatal("legacy config changed")
		}
	}
	if _, ok := legacy.Round(2); ok {
		t.Fatal("legacy budget ignored")
	}
}

func TestSilenceReactionRejectsAmbiguousOrRepeatingRounds(t *testing.T) {
	for name, change := range map[string]func(*SilenceReaction){
		"mixed":                    func(c *SilenceReaction) { c.MaxAttempts = 2 },
		"duplicate between rounds": func(c *SilenceReaction) { c.Rounds[1].Templates = []string{" 您好，您还在听吗？ "} },
		"duplicate within round": func(c *SilenceReaction) {
			c.Rounds[0].Templates = append(c.Rounds[0].Templates, c.Rounds[0].Templates[0])
		},
		"too many rounds":      func(c *SilenceReaction) { c.Rounds = append(c.Rounds, c.Rounds...) },
		"empty pool":           func(c *SilenceReaction) { c.Rounds[1].Templates = nil },
		"invalid later timing": func(c *SilenceReaction) { c.Rounds[1].MaxWaitMS = 999 },
		"too long":             func(c *SilenceReaction) { c.Rounds[1].Templates = []string{strings.Repeat("好", 121)} },
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := ParseSilenceReaction([]byte(DefaultSilenceReaction))
			change(&c)
			b, _ := json.Marshal(c)
			if _, err := ParseSilenceReaction(b); err == nil {
				t.Fatal("invalid rounds accepted")
			}
		})
	}
}
