package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/motorcatalog"
)

// A quotation is asked for only once the assistant has collected what it
// needs — the owner's rule of 10-23: the tool is not to be called on a bare
// "报个价", with the questions left to its `need` answers. So the plan's must
// fields are written into the tool's description at call start, read from
// the lead service's `quote-plans` config, and the model collects them
// first; `need` stays as the safety net.

// QuoteBrief is a catalog revision with the default and per-plan prompt details.
type QuoteBrief struct {
	Version   int
	ByPlan    map[string]QuoteBrief
	Plans     map[string]QuoteSelectionSchema
	Details   map[string]motorcatalog.Detail
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

// ParseQuoteBrief retains every plan so rendering can follow memory.quote_plan.
func ParseQuoteBrief(raw []byte) (QuoteBrief, error) {
	var cfg struct {
		Default string `json:"default"`
		Plans   map[string]struct {
			Coverages []string                       `json:"coverages"`
			Aliases   map[string]string              `json:"coverage_aliases"`
			Details   map[string]motorcatalog.Detail `json:"coverage_details"`
			Options   map[string][]string            `json:"options"`
			Default   map[string]any                 `json:"default_selection"`
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
	b := QuoteBrief{Plan: cfg.Default, Coverages: p.Coverages, Options: p.Options, Default: p.Default, Details: p.Details, Plans: map[string]QuoteSelectionSchema{}, ByPlan: map[string]QuoteBrief{}}
	for name, plan := range cfg.Plans {
		b.Plans[name] = QuoteSelectionSchema{Aliases: plan.Aliases, Details: plan.Details, Options: plan.Options}
		child := QuoteBrief{Plan: name, Coverages: plan.Coverages, Options: plan.Options, Default: plan.Default, Details: plan.Details}
		for _, f := range plan.Fields {
			child.Fields = append(child.Fields, QuoteField{Name: f.Name, Question: f.Question, For: f.For, Source: f.Source})
		}
		b.ByPlan[name] = child
	}
	b.Fields = b.ByPlan[cfg.Default].Fields
	return b, nil
}

// QuoteRevisionInstructions is appended to saved tool descriptions as well as
// new profiles, so older profiles acquire the same revision/preview contract.
const QuoteRevisionInstructions = "客户明确修改当前方案时（如‘三者改成三百万，其他不变’），直接调用 quote_total，config 只包含确认改动，系统会立即更新工作记忆再计算，无须先调用 update_memory。客户只是假设、询价或比较时，即使只有一个备选，也使用 configs 数组；备选不会改写已确认方案。只有新建完整方案或补齐缺失事实时才使用 update_memory。计算失败仍保留已确认改动，金额须以匹配当前方案的成功结果为准。"

const QuoteCatalogGuidance = "保障选项说明必须以本次配置的可选险种和档位为准，不得用常见产品知识编造本方案支持的档位。原始话术或历史示例与本目录不一致时，以本目录为准。保障限制、除外责任、调休等细则也只能依据本目录或明确提供的条款；没有注明时说无法确认，不要自行添加或排除。已经列明的保障规则仍要明确回答，只对缺失的细则说明不确定，不要把整个问题一概说成无法确认。客户问‘有哪些/全部/还有其他吗’时，完整列出所问险种的已配置档位，不要只举几个例子后说‘只有这些’，也不要让客户去报价页面自行查看。先解释选项，再让客户选择；仅询问选项不调用报价或修改记忆。医保外用药等附加险须区分三者、司机、乘客及各自主险，范围不明确时先说明已配置的分类并确认范围。未配置险种或档位时，明确说当前配置未提供，不能确认可选项；不等于保险市场上不存在。\n"

const QuoteCatalogUnavailable = "当前活动报价方案目录不可用或方案名称无效。不能确认任何可选险种、档位或默认保障，不得把历史示例当作活动配置；请说明暂时无法确认。"

// Text is the brief in the model's language, for the tool's description:
// what to settle with the customer before calling. Fields the placeholder
// rules do not read (source "api") are left out when source is "rules".
func (b QuoteBrief) Text(source string) string {
	var sb strings.Builder
	sb.WriteString(QuoteCatalogGuidance)
	if len(b.Default) > 0 {
		names := make([]string, 0, len(b.Default))
		for c, v := range b.Default {
			if !QuoteSelected(v) {
				continue
			}
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
		if len(parts) > 0 {
			fmt.Fprintf(&sb, "默认方案（%s）的保费在通话前已为这位客户算好：客户问价格、要报价时，直接调用本工具，不带 config，不要问车牌、车价、车龄等任何信息，系统会读出默认方案的报价。\n", strings.Join(parts, "、"))
			sb.WriteString("只有当客户想换方案（加减险种、改档位）做第二次报价时，才按下面的规则：")
		} else {
			sb.WriteString("当前默认方案没有已选保障，请先确认投保选项，不要声称已有默认报价。")
		}
	}
	sb.WriteString("新建完整方案或缺少必要事实时，先问清并用 update_memory 记录以下信息（客户资料里已有的不必再问）；已有方案的明确局部改动直接用 config，假设方案用 configs：")
	fmt.Fprintf(&sb, "\n- quote：客户想投的险种方案（可选：%s", strings.Join(b.Coverages, "、"))
	var tiers []string
	for _, c := range b.Coverages {
		if o := b.Options[c]; len(o) > 0 {
			tiers = append(tiers, fmt.Sprintf("%s共%d档，完整列表：%s", c, len(o), strings.Join(o, "、")))
		} else if b.Details[c].Kind == "boolean" {
			tiers = append(tiers, c+"两种选择：投保、不投保（没有金额档位）")
		}
	}
	if len(tiers) > 0 {
		sb.WriteString("；完整选项逐项如下：\n" + strings.Join(tiers, "\n"))
	}
	sb.WriteString("）")
	for _, c := range b.Coverages {
		if d := b.Details[c]; d.Description != "" {
			fmt.Fprintf(&sb, "\n- %s：%s", c, d.Description)
		}
		if d := b.Details[c]; d.Parent != "" {
			fmt.Fprintf(&sb, "\n- %s依附于%s；未投保主险时先确认，不要自动加保。", c, d.Parent)
		}
	}
	if len(b.Details) > 0 {
		sb.WriteString("\n金额按列出的单位，百分比须保留%，共享保额须使用字符串‘共享保额’；司机与乘客分别选择，不用含糊的座位险代替。取消主险时要确认是否一并取消附加险。车船税用‘代缴’或 false。")
	}
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
			Body    json.RawMessage `json:"body"`
			Version int             `json:"version"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Config.Body) > 0 {
		b, err := ParseQuoteBrief(env.Config.Body)
		b.Version = env.Config.Version
		return b, err
	}
	return ParseQuoteBrief(raw)
}

// QuoteSelectionSchema is a snapshot of one configured plan for immediate
// memory revisions. CRM remains the authority for eligibility and pricing.
type QuoteSelectionSchema struct {
	Aliases map[string]string
	Details map[string]motorcatalog.Detail
	Options map[string][]string
}

func (s QuoteSelectionSchema) Normalize(m map[string]any) {
	for k, v := range m {
		if name, ok := s.Aliases[k]; ok && name != k {
			if _, exists := m[name]; !exists {
				m[name] = v
			}
			delete(m, k)
		}
	}
	for k, v := range m {
		if d, ok := s.Details[k]; ok {
			m[k] = motorcatalog.Normalize(v, d, s.Options[k])
		}
	}
}

// ForPlan uses the same explicit plan name as CRM. Unknown plans fail closed.
func (b QuoteBrief) ForPlan(name string) (QuoteBrief, bool) {
	if name == "" {
		name = b.Plan
	}
	if child, ok := b.ByPlan[name]; ok {
		child.Version = b.Version
		return child, true
	}
	if name == b.Plan {
		return b, true
	}
	return QuoteBrief{}, false
}

func QuoteSelected(v any) bool {
	if v == nil || v == false {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(v))) {
	case "", "false", "0", "不投保", "不要", "否", "无", "不代缴":
		return false
	}
	return true
}
