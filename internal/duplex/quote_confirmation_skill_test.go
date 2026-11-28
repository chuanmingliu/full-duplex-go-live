package duplex

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

// Fixed selection isolates verified retrieval and rendering of the real skill
// text. The catalog's routing expressions have separate service regressions.
func quotationSkillFixture(t *testing.T, path string) skillsmcp.Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	lines := strings.Split(text, "\n")
	if len(lines) < 4 || lines[0] != "---" || lines[3] != "---" {
		t.Fatal("invalid skill frontmatter")
	}
	name := strings.TrimPrefix(lines[1], "name: ")
	description := strings.TrimPrefix(lines[2], "description: ")
	// The real MCP server emits JSON-quoted two-field frontmatter.
	text = fmt.Sprintf("---\nname: %q\ndescription: %q\n---\n%s", name, description, strings.Join(lines[4:], "\n"))
	raw = []byte(text)
	uri := "skill://" + name + "/SKILL.md"
	manifest := map[string]any{"uri": uri, "size": len(raw), "digest": fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}
	entry := map[string]any{"uri": uri, "frontmatter": map[string]string{"name": name, "description": description}, "resources": []any{manifest}, "_meta": map[string]any{"ai.liuchuanming.golive/routing": map[string]any{"events": []string{"quote.revised"}}}}
	return skillFixture(t, func(method string, result any) any {
		switch method {
		case "skills/list":
			return map[string]any{"skills": []any{entry}}
		case "skills/get":
			return map[string]any{"skill": entry}
		case "resources/read":
			return map[string]any{"contents": []any{map[string]any{"uri": uri, "mimeType": "text/markdown", "text": text}}}
		case "prompts/get":
			return map[string]any{"_meta": map[string]any{"ai.liuchuanming.golive/turn": map[string]any{"revision": 1, "auto_selection": true, "prompt_rendering": map[string]int{"version": 1}}}, "messages": []any{map[string]any{"content": map[string]any{"type": "resource_link", "uri": uri, "_meta": map[string]any{"ai.liuchuanming.golive/turn": manifest}}}}}
		}
		return result
	})
}

func TestQuotationSkillFixtureLoadsVerifiedContent(t *testing.T) {
	cfg := quotationSkillFixture(t, "../../services/skills-mcp/quotation/SKILL.md")
	client, err := skillsmcp.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	docs, version, err := client.PrepareForTurn(context.Background(), "三者险比较", nil)
	if err != nil || version != 1 || len(docs) != 1 || !strings.Contains(docs[0].Text, "follow-up quotation") {
		t.Fatalf("skill content not loaded: count=%d version=%d err=%v", len(docs), version, err)
	}
}
