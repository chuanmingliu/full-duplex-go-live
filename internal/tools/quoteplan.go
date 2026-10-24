package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// A quotation is asked for only once the assistant has collected what it
// needs — the owner's rule of 10-23: the tool is not to be called on a bare
// "报个价", with the questions left to its `need` answers. So the plan's must
// fields are written into the tool's description at call start, read from
// the lead service's `quote-plans` config, and the model collects them
// first; `need` stays as the safety net.

// QuoteBrief is what a tool description needs of the default plan.
type QuoteBrief struct {
	Plan      string
	Coverages []string
	Options   map[string][]string
	Fields    []QuoteField
	// Default is the plan's default_selection: the quotation prepared for
	// the lead before the call, read out without a question.
	Default map[string]any
}

// QuoteField is one must field: its name, what to ask, and which coverages
// need it ("*" for all).
type QuoteField struct {
	Name     string
	Question string
	For      []string
	Source   string
}

// ParseQuoteBrief reads the config's default plan.
func ParseQuoteBrief(raw []byte) (QuoteBrief, error) {
	var cfg struct {
		Default string `json:"default"`
		Plans   map[string]struct {
			Coverages []string            `json:"coverages"`
			Options   map[string][]string `json:"options"`
			Default   map[string]any      `json:"default_selection"`
			Fields    []struct {
				Name     string   `json:"name"`
				Question string   `json:"question"`
				For      []string `json:"for"`
				Source   string   `json:"source"`
			} `json:"fields"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return QuoteBrief{}, err
	}
	p, ok := cfg.Plans[cfg.Default]
	if !ok {
		return QuoteBrief{}, fmt.Errorf("no default plan %q", cfg.Default)
	}
	b := QuoteBrief{Plan: cfg.Default, Coverages: p.Coverages, Options: p.Options, Default: p.Default}
	for _, f := range p.Fields {
		b.Fields = append(b.Fields, QuoteField{Name: f.Name, Question: f.Question, For: f.For, Source: f.Source})
	}
	return b, nil
}

// Text is the brief in the model's language, for the tool's description:
// what to settle with the customer before calling. Fields the placeholder
// rules do not read (source "api") are left out when source is "rules".
func (b QuoteBrief) Text(source string) string {
	var sb strings.Builder
	if len(b.Default) > 0 {
		names := make([]string, 0, len(b.Default))
		for c := range b.Default {
			names = append(names, c)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, c := range names {
			if s, ok := b.Default[c].(string); ok && s != "" && s != "true" {
				parts = append(parts, c+s)
			} else {
				parts = append(parts, c)
			}
		}
		fmt.Fprintf(&sb, "默认方案（%s）的保费在通话前已为这位客户算好：客户问价格、要报价时，直接调用本工具，不带 config，不要问车牌、车价、车龄等任何信息，系统会读出默认方案的报价。\n", strings.Join(parts, "、"))
		sb.WriteString("只有当客户想换方案（加减险种、改档位）做第二次报价时，才按下面的规则：")
	}
	sb.WriteString("先和客户问清并用 update_memory 记录以下信息（客户资料里已有的不必再问），再调用：")
	fmt.Fprintf(&sb, "\n- quote：客户想投的险种方案（可选：%s", strings.Join(b.Coverages, "、"))
	var tiers []string
	for _, c := range b.Coverages {
		if o := b.Options[c]; len(o) > 0 {
			tiers = append(tiers, fmt.Sprintf("%s档位 %s", c, strings.Join(o, "/")))
		}
	}
	if len(tiers) > 0 {
		sb.WriteString("；" + strings.Join(tiers, "，"))
	}
	sb.WriteString("）")
	for _, f := range b.Fields {
		if source != "" && f.Source != "" && f.Source != "any" && f.Source != source {
			continue
		}
		need := "所有方案"
		if len(f.For) > 0 && f.For[0] != "*" {
			need = "选了" + strings.Join(f.For, "、") + "时"
		}
		fmt.Fprintf(&sb, "\n- %s：%s（%s需要）", f.Name, strings.TrimSpace(f.Question), need)
	}
	sb.WriteString("\n一次只问一个问题；客户确实不知道的可记为 \"unknown\"。")
	if len(b.Default) == 0 {
		sb.WriteString("信息齐全后再调用本工具；不要在对话一开始、客户还没说方案时就调用。")
	}
	return sb.String()
}

// FetchQuoteBrief reads `quote-plans` from the lead service.
func FetchQuoteBrief(base, token string, timeout time.Duration) (QuoteBrief, error) {
	if base == "" {
		return QuoteBrief{}, fmt.Errorf("no base url")
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/configs/quote-plans", nil)
	if err != nil {
		return QuoteBrief{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return QuoteBrief{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QuoteBrief{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return QuoteBrief{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env struct {
		Config struct {
			Body json.RawMessage `json:"body"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Config.Body) > 0 {
		return ParseQuoteBrief(env.Config.Body)
	}
	return ParseQuoteBrief(raw)
}
