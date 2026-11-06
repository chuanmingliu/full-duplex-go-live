package crm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Several options at once, answered as one comparison.
//
// "三者险两百万、三百万和五百万各多少钱" used to be three calls to the tool,
// three acks and three readouts of the same template (the owner, 10:07 on
// 10-28). Now the model passes the alternatives as `configs` in one call:
// each is laid over the plan in the working memory (or the default plan),
// priced — from the cache when it is a neighbour priced ahead, prequote.go
// — and the answer is the current plan's quotation with a `variants` list
// beside it, each variant with its total and its difference from the
// current plan, so the readout can be organized around what changes rather
// than three full readings. The status stays "quote", so the posthook and
// the engine's memory handling apply as for one quotation; the readout
// template sees `variants` and reads the comparison (tools/quotespeech.go).

// Variant is one alternative, priced.
type Variant struct {
	// Label names what changes ("三者险300万"), for the readout.
	Label string `json:"label"`
	// Change is the alternative as the model gave it.
	Change map[string]any `json:"change"`
	// Selection is the whole plan it was priced on.
	Selection map[string]any `json:"selection,omitempty"`
	Total     float64        `json:"total"`
	// Lines retain the service's individual premiums. A total difference is
	// not a substitute for a coverage price when the caller asks for one.
	Lines []QuoteLine `json:"lines,omitempty"`
	// Diff is total − the current plan's total; DiffAbs its size, More
	// whether it costs more — for a template without arithmetic.
	Diff    float64 `json:"diff"`
	DiffAbs float64 `json:"diff_abs"`
	More    bool    `json:"more"`
	// Same marks a variant that is the current plan itself.
	Same bool `json:"same,omitempty"`
	// Skipped and Error say what could not be priced.
	Skipped []string `json:"skipped,omitempty"`
	Error   string   `json:"error,omitempty"`
	// Prequoted says it came from the cache.
	Prequoted bool `json:"prequoted,omitempty"`
}

// Compare prices the request's variants over its plan and answers the
// current plan's quotation with the variants beside it.
func (s *Service) Compare(ctx context.Context, req QuoteRequest) (map[string]any, error) {
	base := copyVars(req.Config)
	if !req.PlanInMemory || len(base) == 0 {
		if def := s.DefaultSelection(req.Call); len(def) > 0 {
			merged := copyVars(def)
			for k, v := range base {
				merged[k] = v
			}
			base = merged
		}
	}
	if len(base) == 0 {
		return nil, fmt.Errorf("no plan to compare against: the working memory has no quote and the call has no default")
	}
	for k, v := range s.LeadVars(req.Call) {
		if _, have := req.Vars[k]; !have {
			req.Vars[k] = v
		}
	}
	price := func(cfg map[string]any) (map[string]any, error) {
		r := QuoteRequest{Config: copyVars(cfg), Vars: copyVars(req.Vars), MemoryVersion: req.MemoryVersion, Call: req.Call}
		q, err := s.Quote(r)
		if err != nil {
			return nil, err
		}
		if n, isNeed := q.(*Need); isNeed && n != nil {
			return nil, fmt.Errorf("missing %s", missingNames(n))
		}
		raw, err := json.Marshal(q)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	began := time.Now()
	current, err := price(base)
	if err != nil {
		return nil, fmt.Errorf("the current plan could not be priced: %w", err)
	}
	baseTotal, _ := current["total"].(float64)
	baseLines, err := comparisonLines(current)
	if err != nil {
		return nil, err
	}
	var variants []Variant
	for _, change := range req.Variants {
		cfg := copyVars(base)
		for k, v := range change {
			cfg[k] = v
		}
		v := Variant{Label: labelOf(change), Change: change}
		same := true
		for k, val := range change {
			if cur, ok := base[k]; !ok || fmt.Sprint(cur) != fmt.Sprint(val) {
				same = false
			}
		}
		if same {
			v.Same, v.Total, v.Selection = true, baseTotal, selectedConfig(base)
			v.Lines = append([]QuoteLine(nil), baseLines...)
			if sk, ok := current["skipped"].([]any); ok {
				for _, x := range sk {
					v.Skipped = append(v.Skipped, fmt.Sprint(x))
				}
			}
			variants = append(variants, v)
			continue
		}
		out, err := price(cfg)
		if err != nil {
			v.Error = err.Error()
			variants = append(variants, v)
			continue
		}
		v.Total, _ = out["total"].(float64)
		v.Lines, err = comparisonLines(out)
		if err != nil {
			return nil, err
		}
		v.Diff = v.Total - baseTotal
		v.DiffAbs, v.More = v.Diff, v.Diff > 0
		if v.DiffAbs < 0 {
			v.DiffAbs = -v.DiffAbs
		}
		if sel, ok := out["selection"].(map[string]any); ok {
			v.Selection = sel
		}
		if sk, ok := out["skipped"].([]any); ok {
			for _, x := range sk {
				v.Skipped = append(v.Skipped, fmt.Sprint(x))
			}
		}
		v.Prequoted = out["prequoted"] == true || out["cached"] == true
		variants = append(variants, v)
	}
	current["compare"] = true
	current["variants"] = variants
	current["compare_ms"] = time.Since(began).Milliseconds()
	if req.MemoryVersion > 0 {
		current["memory_version"] = req.MemoryVersion
	}
	delete(current, "default")
	s.log.Info("crm: options compared", "call", req.Call, "variants", len(variants), "ms", time.Since(began).Milliseconds())
	return current, nil
}

// Each decoded slice belongs to this comparison, including cache hits;
// neither a caller editing its result nor a same-plan variant owns the
// cached quote's line storage.
func comparisonLines(quote map[string]any) ([]QuoteLine, error) {
	raw, err := json.Marshal(quote["lines"])
	if err != nil {
		return nil, fmt.Errorf("cannot retain quotation lines: %w", err)
	}
	var lines []QuoteLine
	if err := json.Unmarshal(raw, &lines); err != nil {
		return nil, fmt.Errorf("cannot retain quotation lines: %w", err)
	}
	return lines, nil
}

// labelOf names a change: "三者险300万", "驾乘险50万", "不要座位险", "加座位险".
func labelOf(change map[string]any) string {
	keys := make([]string, 0, len(change))
	for k := range change {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		switch v := change[k].(type) {
		case bool:
			if v {
				parts = append(parts, "加"+k)
			} else {
				parts = append(parts, "不要"+k)
			}
		case string:
			if selected(v) {
				parts = append(parts, k+v)
			} else {
				parts = append(parts, "不要"+k)
			}
		default:
			parts = append(parts, k+fmt.Sprint(v))
		}
	}
	return strings.Join(parts, "、")
}
