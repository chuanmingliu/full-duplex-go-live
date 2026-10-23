package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	b := QuoteBrief{Plan: cfg.Default, Coverages: p.Coverages, Options: p.Options}
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
	sb.WriteString("调用前必须先和客户问清并用 update_memory 记录以下信息，缺一不可：")
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
	sb.WriteString("\n一次只问一个问题；客户确实不知道的可记为 \"unknown\"。信息齐全后再调用本工具；不要在对话一开始、客户还没说方案时就调用。")
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
