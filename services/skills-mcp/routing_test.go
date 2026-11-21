package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAutomaticSelection(t *testing.T) {
	c := defaults()
	cases := []struct {
		text, state string
		want        int
	}{
		{"保险从什么时候开始生效？", "consented", 0},
		{"什么是报价？", "consented", 0},
		{"报价是什么？", "consented", 0},
		{"报价单有哪些内容？", "consented", 0},
		{"保险理赔怎么申请？", "consented", 0},
		{"不要给我报价了", "consented", 0},
		{"请按现有方案给我报个价", "consented", 1},
		{"把三者险改成三百万，其他不变，请重新算一下。", "consented", 1},
		{"我已经打开微信了，在哪里找服务通知？", "consented", 1},
		{"微信已经打开了，我下一步该做什么？", "consented", 1},
		{"接下来呢？", "consented", 1},
		{"接下来呢？", "added", 0},
		{"怎么找服务通知？", "added", 0},
		{"怎么找服务通知？", "declined", 0},
		{"怎么找服务通知？", "unknown", 0},
		{"微信服务通知是什么？", "consented", 0},
		{"不想添加微信", "consented", 0},
	}
	for _, tc := range cases {
		t.Run(tc.text+tc.state, func(t *testing.T) {
			got, _, err := c.selectTurn(turnInput{Event: "auto", Utterance: tc.text, State: map[string]string{"wechat.status": tc.state}})
			if err != nil || len(got) != tc.want {
				t.Fatalf("matched %d, want %d: %v", len(got), tc.want, err)
			}
		})
	}
	c.Skills[1].Match = []string{`点哪里`}
	got, _, _ := c.selectTurn(turnInput{Event: "auto", Utterance: "点哪里", State: map[string]string{"wechat.status": "consented"}})
	if len(got) != 1 {
		t.Fatal("operator rule not applied")
	}
	c.Skills[1].Match = []string{"["}
	if c.validate() == nil {
		t.Fatal("invalid expression accepted")
	}
}

func TestRoutingMigrationPreservesEdits(t *testing.T) {
	c := defaults()
	c.Schema = 2
	c.Skills[0].Body = "operator content"
	c.Skills[0].Match = nil
	c.Skills[0].Enabled = false
	path := filepath.Join(t.TempDir(), "catalog.json")
	data, _ := json.Marshal(c)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(path, backend{})
	if err != nil {
		t.Fatal(err)
	}
	got := store.catalog
	if got.Schema != 3 || got.Revision != c.Revision+1 || got.Skills[0].Body != c.Skills[0].Body || got.Skills[0].Enabled || len(got.Skills[0].Match) == 0 {
		t.Fatalf("migration lost edits: %+v", got)
	}
}
