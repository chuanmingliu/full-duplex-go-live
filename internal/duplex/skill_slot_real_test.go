package duplex

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

// This opt-in comparison reads only the saved instructions field. Customers,
// recipients and live business tools are excluded. Keep detailed output private.
func TestSkillSlotRealModel(t *testing.T) {
	path := os.Getenv("SKILLS_SLOT_PROMPT_FILE")
	if path == "" {
		t.Skip("opt-in default-prompt slot comparison")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("saved prompt unavailable")
	}
	var saved struct {
		Instructions string `json:"instructions"`
	}
	if json.Unmarshal(data, &saved) != nil || saved.Instructions == "" {
		t.Fatal("saved instructions missing")
	}
	llm, err := provider.OpenLLM(os.Getenv("SKILLS_REAL_PROVIDER"))
	if err != nil {
		t.Fatal("simulation provider unavailable")
	}
	cfg := skillsmcp.Config{URL: os.Getenv("SKILLS_SIM_URL"), TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE"), TimeoutMS: 150}
	if cfg.URL != "http://127.0.0.1:18795/mcp" {
		t.Fatal("slot comparison requires isolated catalog")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	publish := func(version int) {
		t.Helper()
		url := "http://127.0.0.1:18795/admin/catalog"
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer simulation-admin-token")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var catalog map[string]any
		err = json.NewDecoder(res.Body).Decode(&catalog)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			t.Fatal("cannot read isolated catalog")
		}
		catalog["prompt_rendering"] = map[string]int{"version": version}
		raw, _ := json.Marshal(catalog)
		req, _ = http.NewRequest(http.MethodPut, url, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer simulation-admin-token")
		req.Header.Set("Content-Type", "application/json")
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal("cannot publish isolated renderer")
		}
	}
	n, _ := strconv.Atoi(os.Getenv("SKILLS_REAL_RUNS"))
	if n < 1 || n > 20 {
		t.Fatal("use 1–20 paired trials")
	}
	rows := []realSkillRow{}
	calculations := []map[string]any{}
	for _, scenario := range []struct{ name, input string }{
		{"normal", "请直接告诉我，保险从什么时候开始生效？"},
		{"wechat", "我已经同意加微信，现在没有找到服务通知，微信服务通知在哪里找？"},
		{"quotation", "已经确认交强险和车损险都保留，请把三者险改成三百万，其他不变，现在调用报价工具重新算一下。"},
	} {
		for i := 0; i < n; i++ {
			versions := []int{1, 2}
			if i%2 == 1 {
				versions = []int{2, 1}
			}
			for _, version := range versions {
				publish(version)
				s := newSkillSessionWithPrompt(t, llm, cfg, saved.Instructions)
				row := s.turn(t, scenario.name, fmt.Sprintf("v%d", version), scenario.input)
				row.Checks["expected_skill_selection"] = skillLoaded(row) == (scenario.name != "normal")
				row.Checks["default_prompt_preserved"] = len(row.Trace) > 0 && row.Trace[0].OriginalPrompt
				row.Checks["expected_slot"] = len(row.Trace) > 0 && row.Trace[0].Slot == (version == 2 && scenario.name != "normal")
				if scenario.name == "quotation" {
					row.Checks["quote_requested"] = len(row.Quotes) > 0
					if len(row.Quotes) > 0 {
						row.Checks["plan_preserved"] = strings.Contains(string(row.Quotes[0].Memory), `"交强险":true`) && strings.Contains(string(row.Quotes[0].Memory), `"车损险":true`) && strings.Contains(string(row.Quotes[0].Arguments), "300万")
					}
					select {
					case elapsed := <-s.quote.completed:
						calculations = append(calculations, map[string]any{"mode": row.Mode, "calculation_ms": float64(elapsed.Microseconds()) / 1000})
						row.Checks["calculation_completed"] = true
					case <-time.After(6 * time.Second):
						row.Checks["calculation_completed"] = false
					}
				}
				rows = append(rows, row)
				s.e.Close()
				t.Logf("SLOT_SIM scenario=%s mode=%s trial=%d first_audio_ms=%.3f checks=%v", scenario.name, row.Mode, i, row.AudioMS, row.Checks)
			}
		}
	}
	// Include fallback and completed-state checks with the same saved prompt.
	publish(2)
	for _, scenario := range []string{"fallback", "added"} {
		selected := cfg
		if scenario == "fallback" {
			selected.URL = "http://127.0.0.1:1/mcp"
		}
		s := newSkillSessionWithPrompt(t, llm, selected, saved.Instructions)
		if scenario == "added" {
			s.backendTrace("addV")
		}
		row := s.turn(t, scenario, "v2", "微信服务通知在哪里找？")
		row.Checks["no_loaded_skill"] = !skillLoaded(row)
		row.Checks["default_prompt_preserved"] = len(row.Trace) > 0 && row.Trace[0].OriginalPrompt
		if scenario == "added" {
			row.Checks["acknowledges_completion"] = acknowledgesAdded(row)
		}
		rows = append(rows, row)
		s.e.Close()
	}
	report := map[string]any{"prompt_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(saved.Instructions))), "boundary": "text input to mock audio", "rows": rows, "calculations": calculations}
	raw, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(os.Getenv("SKILLS_REAL_REPORT"), raw, 0600); err != nil {
		t.Fatal("cannot write private comparison report")
	}
	for _, row := range rows {
		for name, ok := range row.Checks {
			if !ok {
				t.Errorf("%s/%s failed %s", row.Scenario, row.Mode, name)
			}
		}
	}
}
