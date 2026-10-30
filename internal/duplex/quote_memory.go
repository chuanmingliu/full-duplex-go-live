package duplex

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Bound only the extra price facts, leaving the configured spoken readout
// unchanged. Whole option records are kept; an omitted record is never
// presented as a zero-price option.
const maxQuoteMemoryChars = 4096

type rememberedQuoteLine struct {
	Coverage string   `json:"coverage"`
	Choice   string   `json:"choice"`
	Premium  *float64 `json:"premium"`
}

type rememberedQuoteOption struct {
	Label   string                `json:"label"`
	Total   *float64              `json:"total"`
	Lines   []rememberedQuoteLine `json:"lines"`
	Skipped []string              `json:"skipped"`
	Error   string                `json:"error"`
}

// quoteMemoryFacts keeps prices from the tool, not arithmetic reconstructed
// from the comparison's total differences. Pointer amounts distinguish a
// known zero from a missing price, and a failed option has no usable price.
func quoteMemoryFacts(raw []byte) string {
	var quote struct {
		rememberedQuoteOption
		Status   string                  `json:"status"`
		Currency string                  `json:"currency"`
		Variants []rememberedQuoteOption `json:"variants"`
	}
	if json.Unmarshal(raw, &quote) != nil || quote.Status != "quote" {
		return ""
	}
	header := "【报价明细依据】按客户问题回答单项或合计。金额只用工具明细，不得用总价差额推算单项保费；未返回的明细须查询。"
	if quote.Currency != "" {
		header += "币种=" + quote.Currency + "。"
	}
	const omitted = "\n其余明细未保留；缺失金额须查询，不可推算。"
	var b strings.Builder
	chars := 0
	appendRecord := func(record string) bool {
		n := utf8.RuneCountInString(record)
		if chars+n+utf8.RuneCountInString(omitted) > maxQuoteMemoryChars {
			b.WriteString(omitted)
			return false
		}
		b.WriteString(record)
		chars += n
		return true
	}
	if !appendRecord(header) || !appendRecord("\n"+quoteOptionFacts("当前方案", quote.rememberedQuoteOption)) {
		return b.String()
	}
	for index, option := range quote.Variants {
		label := option.Label
		if label == "" {
			label = fmt.Sprintf("备选方案%d", index+1)
		}
		if !appendRecord("\n" + quoteOptionFacts(label, option)) {
			break
		}
	}
	return b.String()
}

func quoteOptionFacts(label string, option rememberedQuoteOption) string {
	if option.Error != "" {
		return label + "：未能报价（没有可用金额）。"
	}
	var parts []string
	if option.Total != nil {
		name := "合计="
		if len(option.Skipped) > 0 {
			name = "已报价部分合计="
		}
		parts = append(parts, name+quoteAmount(*option.Total))
	}
	skipped := map[string]bool{}
	for _, coverage := range option.Skipped {
		skipped[coverage] = true
	}
	for _, line := range option.Lines {
		if line.Coverage == "" || skipped[line.Coverage] {
			continue
		}
		name := line.Coverage
		if line.Choice != "" {
			name += "[" + line.Choice + "]"
		}
		amount := "未返回保费"
		if line.Premium != nil {
			amount = quoteAmount(*line.Premium)
		}
		parts = append(parts, name+"="+amount)
	}
	if len(option.Skipped) > 0 {
		parts = append(parts, "未报价="+strings.Join(option.Skipped, "、"))
	}
	if len(parts) == 0 {
		return label + "：未返回金额明细。"
	}
	return label + "：" + strings.Join(parts, "；") + "。"
}

func quoteAmount(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
