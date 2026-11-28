package duplex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

type conceptsProbe struct {
	inner    provider.LLM
	mu       sync.Mutex
	injected bool
}

func (p *conceptsProbe) Name() string { return p.inner.Name() }
func (p *conceptsProbe) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "vehicle_insurance_concepts") && strings.Contains(message.Content, "保障对象") {
			p.mu.Lock()
			p.injected = true
			p.mu.Unlock()
		}
	}
	return p.inner.Stream(ctx, req)
}

// Opt-in probe against the configured catalog, with synthetic turns and the
// saved instructions. Business actions remain intercepted by the test executor.
func TestVehicleConceptsRealModel(t *testing.T) {
	path := os.Getenv("SKILLS_CONCEPT_PROMPT_FILE")
	if path == "" {
		t.Skip("opt-in concepts probe")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("saved prompt unavailable")
	}
	var saved struct {
		Instructions string `json:"instructions"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Instructions == "" {
		t.Fatal("saved instructions missing")
	}
	llm, err := provider.OpenLLM(os.Getenv("SKILLS_REAL_PROVIDER"))
	if err != nil {
		t.Fatal("provider unavailable")
	}
	cfg := skillsmcp.Config{URL: os.Getenv("SKILLS_SIM_URL"), TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE"), TimeoutMS: 150}
	rows := []realSkillRow{}
	for _, input := range []string{"交强险一般保障什么？", "车损险和三者险有什么区别？", "保额和保费有什么区别？"} {
		for _, mode := range []string{"baseline", "concepts"} {
			selected := cfg
			if mode == "baseline" {
				selected = skillsmcp.Config{}
			}
			probe := &conceptsProbe{inner: llm}
			session := newSkillSessionWithPrompt(t, probe, selected, saved.Instructions)
			row := session.turn(t, input, mode, input)
			probe.mu.Lock()
			injected := probe.injected
			probe.mu.Unlock()
			row.Checks["guidance_injected"] = injected == (mode == "concepts")
			row.Checks["selection"] = skillLoaded(row) == (mode == "concepts")
			row.Checks["original_prompt"] = len(row.Trace) > 0 && row.Trace[0].OriginalPrompt
			row.Checks["no_quotation"] = len(row.Quotes) == 0
			rows = append(rows, row)
			session.e.Close()
			t.Logf("CONCEPTS mode=%s text_ms=%.1f mock_audio_ms=%.1f checks=%v", mode, row.TextMS, row.AudioMS, row.Checks)
		}
	}
	output, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(os.Getenv("SKILLS_REAL_REPORT"), output, 0600); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for name, passed := range row.Checks {
			if !passed {
				t.Errorf("%s/%s: %s", row.Mode, row.Scenario, name)
			}
		}
	}
}
