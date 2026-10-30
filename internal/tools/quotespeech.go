package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/jinja"
)

// QuoteSpeech is what the assistant says around a quotation, edited on the
// lead service's panel (`quote-speech`) rather than built in — the owner's
// request of 10-24: a preset line the moment the premium is being worked
// out (the prehook), a preset line the moment it is ready (the posthook),
// and a template that reads the figures out, before the model gets a word.
//
//	prehook         said when the tool is submitted and a computation is
//	                under way (not for a `need`)
//	posthook        the first words of the reply that delivers a result of
//	                posthook_status
//	template        Jinja2 over the result (total, lines[].coverage/choice/
//	                premium, skipped, currency); rendered and said after the
//	                posthook; empty leaves the figures to the model
//	posthook_status the result status the posthook and template are for
type QuoteSpeech struct {
	Version        int    `json:"version,omitempty"`
	Prehook        string `json:"prehook"`
	Posthook       string `json:"posthook"`
	Template       string `json:"template,omitempty"`
	PosthookStatus string `json:"posthook_status,omitempty"`
	// CompareTemplate reads a result that compares several options
	// (`variants`: label, total, diff, diff_abs, more, same, error) —
	// organized around what changes and by how much, instead of the
	// template read three times (the owner, 10:07 on 10-28). Empty: the
	// built-in DefaultCompareTemplate.
	CompareTemplate string `json:"compare_template,omitempty"`

	tmpl *jinja.Template
	cmp  *jinja.Template
}

// DefaultCompareTemplate reads a comparison: the current plan's total, then
// each option by what it changes and the difference.
const DefaultCompareTemplate = `现在这个方案一共{{ total | round | int }}元。{% for v in variants %}{% if v.error %}{{ v.label }}暂时算不了；{% elif v.same %}{{ v.label }}就是现在的方案；{% else %}{{ v.label }}一共{{ v.total | round | int }}元，{% if v.more %}多{% else %}少{% endif %}{{ v.diff_abs | round | int }}元{% if not loop.last %}；{% else %}。{% endif %}{% endif %}{% endfor %}`

// QuoteSpeechName is the config's name on the lead service.
const QuoteSpeechName = "quote-speech"

// DefaultQuotePosthook only announces that a result is ready. A caller who
// just heard an opener can receive the result directly without this preamble.
const DefaultQuotePosthook = "保费给您计算好了，"

// DefaultQuoteSpeech is the seed: the owner's two lines of 10-23 and a
// plain reading of the lines and the total.
const DefaultQuoteSpeech = `{
  "prehook": "这会儿正在给您计算保费，请您稍等。",
  "posthook": "` + DefaultQuotePosthook + `",
  "posthook_status": "quote",
  "template": "{% for l in lines %}{{ l.coverage }}{% if l.choice %}{{ l.choice }}{% endif %}{{ l.premium | round | int }}元{% if not loop.last %}，{% endif %}{% endfor %}，合计{{ total | round | int }}元。{% if skipped %}{{ skipped | join('、') }}暂时算不了。{% endif %}"
}`

// ParseQuoteSpeech reads and checks the config: the template must parse.
func ParseQuoteSpeech(raw []byte) (QuoteSpeech, error) {
	var qs QuoteSpeech
	if err := json.Unmarshal(raw, &qs); err != nil {
		return qs, err
	}
	qs.Prehook, qs.Posthook = strings.TrimSpace(qs.Prehook), strings.TrimSpace(qs.Posthook)
	if qs.Prehook == "" && qs.Posthook == "" && strings.TrimSpace(qs.Template) == "" {
		return qs, errors.New("quote speech: nothing to say (prehook, posthook and template all empty)")
	}
	if strings.TrimSpace(qs.Template) != "" {
		t, err := jinja.Parse(qs.Template)
		if err != nil {
			return qs, fmt.Errorf("quote speech: template: %w", err)
		}
		qs.tmpl = t
		// A dry run on a plausible result catches a filter or a name the
		// renderer does not have before a call does.
		if _, err := qs.render(map[string]any{"total": 1234.5, "currency": "CNY",
			"lines":   []any{map[string]any{"coverage": "交强险", "premium": 950.0}, map[string]any{"coverage": "三者险", "choice": "200万", "premium": 284.5}},
			"skipped": []any{}}); err != nil {
			return qs, fmt.Errorf("quote speech: template does not render: %w", err)
		}
	}
	cmpSrc := strings.TrimSpace(qs.CompareTemplate)
	if cmpSrc == "" {
		cmpSrc = DefaultCompareTemplate
	}
	c, err := jinja.Parse(cmpSrc)
	if err != nil {
		return qs, fmt.Errorf("quote speech: compare_template: %w", err)
	}
	qs.cmp = c
	if _, err := c.Render(map[string]any{"total": 10299.8, "variants": []any{
		map[string]any{"label": "三者险300万", "total": 10524.8, "diff": 225.0, "diff_abs": 225.0, "more": true},
		map[string]any{"label": "三者险200万", "total": 10299.8, "diff": 0.0, "diff_abs": 0.0, "same": true},
		map[string]any{"label": "驾乘险80万", "error": "x"}}}); err != nil {
		return qs, fmt.Errorf("quote speech: compare_template does not render: %w", err)
	}
	return qs, nil
}

func (qs QuoteSpeech) render(vars map[string]any) (string, error) {
	if qs.tmpl == nil {
		return "", nil
	}
	return qs.tmpl.Render(vars)
}

// Readout renders the template over a tool result, or "" when there is no
// template, the status is not the posthook's, or the result does not render
// (the model then reads the figures itself).
func (qs QuoteSpeech) Readout(output []byte) string {
	if !qs.For(output) {
		return ""
	}
	var vars map[string]any
	if json.Unmarshal(output, &vars) != nil {
		return ""
	}
	if _, isCompare := vars["variants"]; isCompare && vars["compare"] == true && qs.cmp != nil {
		out, err := qs.cmp.Render(vars)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	if qs.tmpl == nil {
		return ""
	}
	out, err := qs.render(vars)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// For says whether a result is one the posthook and template are for.
func (qs QuoteSpeech) For(output []byte) bool {
	return qs.PosthookStatus == "" || ResultStatus(output) == qs.PosthookStatus
}

// FetchQuoteSpeech reads the config from the lead service.
func FetchQuoteSpeech(base, token string, timeout time.Duration) (QuoteSpeech, error) {
	if base == "" {
		return QuoteSpeech{}, fmt.Errorf("no base url")
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/configs/"+QuoteSpeechName, nil)
	if err != nil {
		return QuoteSpeech{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return QuoteSpeech{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QuoteSpeech{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return QuoteSpeech{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env struct {
		Config struct {
			Version int             `json:"version"`
			Body    json.RawMessage `json:"body"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Config.Body) > 0 {
		qs, err := ParseQuoteSpeech(env.Config.Body)
		qs.Version = env.Config.Version
		return qs, err
	}
	return ParseQuoteSpeech(raw)
}
