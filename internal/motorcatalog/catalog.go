// Package motorcatalog defines selection semantics shared by the voice agent,
// CRM and the Xiaocui test quotation backend. It contains no insurer prices.
package motorcatalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

//go:embed catalog.json
var files embed.FS

type Detail struct {
	Description string `json:"description,omitempty"`
	Label       string `json:"label,omitempty"`
	Group       string `json:"group,omitempty"`
	Kind        string `json:"kind"`
	Parent      string `json:"parent,omitempty"`
	InputUnit   string `json:"input_unit,omitempty"`
}
type Entry struct {
	Detail
	Name    string   `json:"name"`
	Code    string   `json:"code"`
	Options []string `json:"options,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

func Entries() []Entry {
	raw, _ := files.ReadFile("catalog.json")
	var out []Entry
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

var amount = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(万|[wW]|元)?$`)

// Amount accepts whole expressions only; e.g. "5%" is never five yuan.
func Amount(s string) (float64, bool) {
	m := amount.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if m[2] == "万" || strings.EqualFold(m[2], "w") {
		n *= 10000
	}
	return n, err == nil
}

// Normalize converts equivalent representations to the configured option.
// An unsupported choice is retained so the intake can ask for clarification.
func Normalize(v any, detail Detail, options []string) any {
	s := strings.TrimSpace(fmt.Sprint(v))
	switch strings.ToLower(s) {
	case "false", "不投保", "不要", "否", "无", "0", "不代缴":
		return false
	case "true", "投保", "是", "要":
		if detail.Kind == "boolean" {
			return true
		}
	}
	if detail.Kind == "enum" {
		if s == "true" && len(options) == 1 {
			return options[0]
		}
		return s
	}
	if detail.Kind == "boolean" {
		return v
	}
	if detail.Kind == "percent" {
		s = strings.ReplaceAll(s, "％", "%")
		for _, o := range options {
			if s == o {
				return o
			}
		}
		return v
	}
	if s == "共享保额" {
		return s
	}
	n, ok := Amount(s)
	if !ok {
		return v
	}
	for _, o := range options {
		want, valid := Amount(o)
		if valid && want == n {
			return o
		}
	}
	if detail.InputUnit == "万" && !strings.ContainsAny(s, "万wW元") {
		for _, o := range options {
			want, valid := Amount(o)
			if valid && want == n*10000 {
				return o
			}
		}
	}
	return v
}

func Valid(v any, d Detail, options []string) bool {
	if b, ok := v.(bool); ok {
		return !b || d.Kind == "boolean"
	}
	for _, o := range options {
		if s, ok := v.(string); ok && s == o {
			return true
		}
	}
	return false
}
