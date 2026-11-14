package duplex

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

func TestSkillSlotLiteralSubstitution(t *testing.T) {
	original := "Default prompt {{ carNum }} {{ skill_content }} {% if region %}"
	body := "Keep this literal {{ original_prompt }} and {{ customer }}."
	messages := []provider.Message{{Role: provider.RoleSystem, Content: original}, {Role: provider.RoleUser, Content: "latest request"}}
	if !fillSkillContentSlot(messages, []skillsmcp.Guidance{{Text: body}}) {
		t.Fatal("slot was not filled")
	}
	want := original + "\n\n<additional_skill_instructions>\n" + body + "\n</additional_skill_instructions>"
	if messages[0].Content != want || messages[1].Content != "latest request" {
		t.Fatal("literal prompt or user request changed")
	}
	if fillSkillContentSlot(nil, []skillsmcp.Guidance{{Text: body}}) {
		t.Fatal("missing system prompt accepted")
	}
	fresh := []provider.Message{{Role: provider.RoleSystem, Content: original}}
	if fillSkillContentSlot(fresh, nil) || fresh[0].Content != original {
		t.Fatal("empty selection changed the original prompt")
	}
	guides := []skillsmcp.Guidance{{Text: "first body"}, {Text: "second body"}}
	fillSkillContentSlot(fresh, guides)
	if !strings.Contains(fresh[0].Content, "first body\n\nsecond body") || guides[0].Text != "first body" || guides[1].Text != "second body" {
		t.Fatal("multiple skills lost ordering or changed the source guidance")
	}
}

func renderingFixture(t *testing.T, version int) skillsmcp.Config {
	return skillFixture(t, func(method string, result any) any {
		if method == "prompts/get" {
			result.(map[string]any)["_meta"].(map[string]any)["ai.liuchuanming.golive/turn"].(map[string]any)["prompt_rendering"] = map[string]int{"version": version}
		}
		return result
	})
}

func TestSkillSlotEngineScenarios(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		for _, event := range []string{"", "wechat.notification_help", "quote.revised"} {
			cfg := renderingFixture(t, version)
			_, requests := runSkillScenario(t, cfg, event, event == "quote.revised")
			first := requests[0]
			slot := strings.Contains(first.Messages[0].Content, "<additional_skill_instructions>")
			if slot != (version == 2 && event != "") {
				t.Fatalf("version %d event %s slot=%v", version, event, slot)
			}
			for _, m := range first.Messages {
				if m.Name != skillsmcp.ToolName {
					continue
				}
				var out skillOutput
				if err := json.Unmarshal([]byte(m.Content), &out); err != nil {
					t.Fatal(err)
				}
				if version == 3 && out.Status != "unavailable" {
					t.Fatal("unsupported renderer did not fall back")
				}
				if slot && (out.RenderingVersion != 2 || len(out.Guidance) != 1 || out.Guidance[0].Text != "") {
					t.Fatal("v2 duplicated the skill body or lost provenance")
				}
			}
		}
	}
}

func TestSkillSlotCatalogVersionRace(t *testing.T) {
	calls := 0
	cfg := skillFixture(t, func(method string, result any) any {
		if method == "prompts/get" {
			calls++
			meta := result.(map[string]any)["_meta"].(map[string]any)["ai.liuchuanming.golive/turn"].(map[string]any)
			meta["revision"] = calls
			meta["prompt_rendering"] = map[string]int{"version": 2}
		}
		return result
	})
	client, err := skillsmcp.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	guides, _, err := client.PrepareForTurn(context.Background(), "修改报价", json.RawMessage(`{}`))
	if err == nil || len(guides) != 0 {
		t.Fatal("publication race leaked guidance")
	}
}

func TestSkillSlotModeReloadedOnNextRequest(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	cfg := skillFixture(t, func(method string, result any) any {
		if method == "prompts/get" {
			meta := result.(map[string]any)["_meta"].(map[string]any)["ai.liuchuanming.golive/turn"].(map[string]any)
			meta["prompt_rendering"] = map[string]int32{"version": version.Load()}
		}
		return result
	})
	client, err := skillsmcp.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int32{1, 2, 1} {
		version.Store(want)
		guides, got, err := client.PrepareForTurn(context.Background(), "修改报价", nil)
		if err != nil || int32(got) != want || len(guides) != 1 {
			t.Fatal("rendering mode not refreshed", got, err)
		}
	}
}
