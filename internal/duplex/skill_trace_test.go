package duplex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

func TestSkillTraceMeasuresOnlyExecutedStages(t *testing.T) {
	for _, tc := range []struct {
		text   string
		count  int
		broken bool
	}{{"你好", 2, false}, {"现在报价", 7, false}, {"现在报价", 6, true}} {
		cfg := skillFixture(t, func(method string, value any) any {
			if tc.broken && method == "resources/read" {
				value.(map[string]any)["contents"].([]any)[0].(map[string]any)["text"] = "changed"
			}
			return value
		})
		client, err := skillsmcp.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var trace skillsmcp.Trace
		docs, _, err := client.PrepareWithTrace(context.Background(), tc.text, nil, &trace)
		if (err != nil) != tc.broken || len(trace.Steps) != tc.count || trace.Revision != 1 {
			t.Fatalf("unexpected trace: %+v, %v", trace, err)
		}
		last := trace.Steps[len(trace.Steps)-1]
		if tc.broken && (last.Outcome != "failed" || last.Stage != "verify digest and frontmatter") {
			t.Fatalf("missing failed stage: %+v", last)
		}
		if len(docs) > 0 && (last.Stage != "prompts/get revision recheck" || trace.Steps[5].Digest != docs[0].Digest) {
			t.Fatal("verified skill identity missing")
		}
		raw, _ := json.Marshal(trace)
		if strings.Contains(string(raw), "Ask the caller") || strings.Contains(string(raw), tc.text) {
			t.Fatal("unexpected private text in trace")
		}
	}
}
