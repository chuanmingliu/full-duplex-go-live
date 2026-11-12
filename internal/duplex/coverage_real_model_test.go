package duplex

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/motorcatalog"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
	"github.com/chuanmingliu/golive/internal/tools"
)

// Optional full-model check, separate from acoustic/PBX measurements. Reports
// stay private; callers export only numeric timings and boolean checks.
func TestCoverageRealModelOptions(t *testing.T) {
	report := os.Getenv("COVERAGE_REAL_REPORT")
	if report == "" {
		t.Skip("opt-in model coverage test")
	}
	llm, err := provider.OpenLLM(os.Getenv("SKILLS_REAL_PROVIDER"))
	if err != nil {
		t.Fatal("model unavailable")
	}
	prompt := skillsSyntheticPrompt
	if path := os.Getenv("COVERAGE_PROMPT_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Instructions string `json:"instructions"`
		}
		if json.Unmarshal(raw, &saved) != nil || saved.Instructions == "" {
			t.Fatal("saved prompt missing")
		}
		prompt = saved.Instructions
	}
	cfg := skillsmcp.Config{URL: os.Getenv("SKILLS_SIM_URL"), TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE"), TimeoutMS: 400}
	b := tools.QuoteBrief{Plan: "motor-full", Details: map[string]motorcatalog.Detail{}, Options: map[string][]string{}}
	for _, entry := range motorcatalog.Entries() {
		b.Coverages = append(b.Coverages, entry.Name)
		b.Details[entry.Name] = entry.Detail
		b.Options[entry.Name] = entry.Options
	}
	rows := []realSkillRow{}
	cases := append(motorcatalog.Entries(), motorcatalog.Entry{Name: "shared-cost"}, motorcatalog.Entry{Name: "unconfigured-product"})
	for _, entry := range cases {
		s := newSkillSessionWithPrompt(t, llm, cfg, prompt)
		ready := make(chan struct{})
		s.e.post(func() { s.e.tools.quoteBrief = &b; s.e.opts.Cfg.MaxOutputTokens = 768; close(ready) })
		<-ready
		question := entry.Name + "有哪些全部可选项？请完整列出，每个数字都带单位；只介绍选项，不要报价或修改方案。"
		if entry.Name == "shared-cost" {
			question = "三者医保外用药大家一般选哪？共享保额是不是免费？只解释，不报价。"
		}
		if entry.Name == "unconfigured-product" {
			question = "驾乘险和司机乘客责任险有什么区别？现在这个方案能加驾乘险吗？只解释，不报价。"
		}
		if entry.Name == "假日三者翻倍险" {
			question += "周末也包括吗？有没有调休规则？"
		}
		before, _ := s.e.MemorySnapshot()
		row := s.turn(t, entry.Name, "skills", question)
		row.Checks["original_prompt_preserved"] = len(row.Trace) > 0
		for _, step := range row.Trace {
			row.Checks["original_prompt_preserved"] = row.Checks["original_prompt_preserved"] && step.OriginalPrompt
		}
		row.Checks["memory_unchanged"] = string(before) == string(row.Memory)
		text := normalizeCoverageNumbers(rowText(row))
		row.Checks["concept_skill_loaded"] = skillLoaded(row)
		row.Checks["no_quotation"] = len(row.Quotes) == 0
		if entry.Name == "shared-cost" {
			row.Checks["shared_cost_requires_quote"] = regexp.MustCompile(`(是否收费|收不收费|收费与否|具体.*(费用|保费)|要不要.*费|以.*报价|看.*报价)`).MatchString(text)
			row.Checks["no_automatic_charge_claim"] = !regexp.MustCompile(`不是免费|肯定要收费|必须额外收费|保费还是要算进去`).MatchString(text)
			row.Checks["no_population_claim"] = !regexp.MustCompile(`(大多数|多数客户|大家一般都|大部分客户)[^。；]{0,12}(选|买)`).MatchString(text)
		}
		if entry.Name == "unconfigured-product" {
			row.Checks["unconfigured_product_unavailable"] = coverageProductUnavailable(text)
			row.Checks["no_unconfigured_product_offer"] = !regexp.MustCompile(`(可以|帮您|给您|为您)[^。；]{0,15}(加上驾乘|加驾乘|驾乘险.*(算|报))`).MatchString(text)
		}
		for _, option := range entry.Options {
			present := strings.Contains(text, option)
			if option == "共享保额" {
				present = strings.Contains(text, "共享")
			}
			if option == "代缴" {
				present = strings.Contains(text, "代缴")
			}
			if option != "共享保额" && option != "代缴" {
				present = regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(option) + `([^0-9]|$)`).MatchString(text)
			}
			row.Checks["option_"+option] = present
		}
		if entry.Name == "假日三者翻倍险" {
			row.Checks["weekends_included"] = regexp.MustCompile(`(含|包括|包含)[^。；]*(周末|双休日)|(周末|双休日)[^。；]*(包括|包含|翻倍|在范围|享受)`).MatchString(text)
			row.Checks["unconfigured_holiday_rules_not_asserted"] = !strings.Contains(text, "不享受翻倍") && (strings.Contains(text, "没法确认") || strings.Contains(text, "无法确认") || strings.Contains(text, "未提供") || strings.Contains(text, "未说明") || strings.Contains(text, "没有说明") || strings.Contains(text, "没明确") || strings.Contains(text, "未明确") || strings.Contains(text, "没注明") || strings.Contains(text, "没有注明"))
		}
		if entry.Kind == "boolean" {
			row.Checks["boolean_options"] = coverageBooleanOptions(text)
		}
		rows = append(rows, row)
		s.e.Close()
		for check, ok := range row.Checks {
			if !ok {
				t.Errorf("%s failed %s", entry.Name, check)
			}
		}
		t.Logf("COVERAGE_MODEL name=%s first_text_ms=%.2f checks=%v", entry.Name, row.TextMS, row.Checks)
	}
	raw, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(report, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func normalizeCoverageNumbers(text string) string {
	// A model may format a currency amount as 2,000元. Keep list separators
	// intact and normalize only grouped digits immediately followed by a unit.
	grouped := regexp.MustCompile(`[0-9]{1,3}(,[0-9]{3})+(元|万)`)
	text = grouped.ReplaceAllStringFunc(text, func(s string) string { return strings.ReplaceAll(s, ",", "") })
	replacements := []string{
		"四百万", "400万", "三百万", "300万", "二百万", "200万", "两百万", "200万", "一百五十万", "150万", "一百万", "100万",
		"五十万", "50万", "三十万", "30万", "二十万", "20万", "十五万", "15万", "十万", "10万", "五万", "5万", "四万", "4万", "三万", "3万", "二万", "2万", "两万", "2万", "一万", "1万",
		"百分之二十", "20%", "百分之十五", "15%", "百分之十", "10%", "百分之五", "5%",
		"五千", "5000", "两千", "2000", "二千", "2000", "一万元", "1万", "二万元", "2万", "％", "%",
	}
	return strings.NewReplacer(replacements...).Replace(strings.ReplaceAll(text, " ", ""))
}

func TestNormalizeCoverageGroupedAmounts(t *testing.T) {
	got := normalizeCoverageNumbers("2,000元、5,000元、1万、2万；50万,100万,150万")
	if got != "2000元、5000元、1万、2万；50万,100万,150万" {
		t.Fatal(got)
	}
}

func coverageBooleanOptions(text string) bool {
	return (strings.Contains(text, "投保") && strings.Contains(text, "不投保")) ||
		(strings.Contains(text, "购买") && strings.Contains(text, "不购买")) ||
		strings.Contains(text, "保或不保") || strings.Contains(text, "保和不保")
}

func coverageProductUnavailable(text string) bool {
	return regexp.MustCompile(`(没|未|不)[^。；]{0,18}(提供|配置|支持|包含|能加|能直接加)|没有驾乘险`).MatchString(text)
}

func TestCoverageNaturalAnswerChecks(t *testing.T) {
	for _, text := range []string{"可投保或不投保", "购买、不购买", "保或不保两种", "保和不保都可以"} {
		if !coverageBooleanOptions(text) {
			t.Fatal(text)
		}
	}
	if coverageBooleanOptions("可以买车损险") {
		t.Fatal("accepted missing opt-out")
	}
	for _, text := range []string{"方案没有提供驾乘险", "您现在的方案里没有驾乘险，具体档位无法确认"} {
		if !coverageProductUnavailable(text) {
			t.Fatal(text)
		}
	}
	if coverageProductUnavailable("可以加上驾乘险") {
		t.Fatal("accepted unsupported offer")
	}
}
