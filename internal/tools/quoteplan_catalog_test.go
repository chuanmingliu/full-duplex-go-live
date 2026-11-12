package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/motorcatalog"
)

func TestQuoteBriefRendersCompleteMedicalCatalog(t *testing.T) {
	coverages := []string{}
	options := map[string][]string{}
	details := map[string]motorcatalog.Detail{}
	for _, entry := range motorcatalog.Entries() {
		coverages = append(coverages, entry.Name)
		options[entry.Name] = entry.Options
		details[entry.Name] = entry.Detail
	}
	raw, err := json.Marshal(map[string]any{"default": "motor-full", "plans": map[string]any{
		"motor-full": map[string]any{"coverages": coverages, "options": options, "coverage_details": details},
	}})
	if err != nil {
		t.Fatal(err)
	}
	brief, err := ParseQuoteBrief(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := brief.Text("")
	for _, expected := range []string{
		"医保外用药责任险-三者共11档，完整列表：共享保额、10万、15万、20万、30万、50万、100万、150万、200万、300万、400万",
		"医保外用药责任险-司机共6档，完整列表：共享保额、1万、2万、5万、10万、20万",
		"医保外用药责任险-乘客共6档，完整列表：共享保额、1万、2万、5万、10万、20万",
		"医保外用药责任险-三者依附于三者险",
		"医保外用药责任险-司机依附于司机责任险",
		"医保外用药责任险-乘客依附于乘客责任险",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("missing configured catalog content %q", expected)
		}
	}
	// A configuration change must propagate, rather than retaining the built-in tiers.
	brief.Options["医保外用药责任险-三者"] = []string{"共享保额", "15万"}
	text = brief.Text("")
	if !strings.Contains(text, "医保外用药责任险-三者共2档，完整列表：共享保额、15万") || strings.Contains(text, "医保外用药责任险-三者共2档，完整列表：共享保额、10万") {
		t.Fatal("rendered tiers did not follow the active configuration")
	}
}

func TestQuoteBriefDoesNotInventLegacyMedicalTiers(t *testing.T) {
	brief, err := ParseQuoteBrief([]byte(`{"default":"standard","plans":{"standard":{"coverages":["三者险"],"options":{"三者险":["100万","200万","300万","500万"]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	text := brief.Text("")
	if strings.Contains(text, "医保外用药责任险-三者共") {
		t.Fatal("injected unconfigured medical options")
	}
	if !strings.Contains(text, "未配置险种或档位时，明确说当前配置未提供") {
		t.Fatal("legacy plans lack an unknown-options fallback")
	}
}
