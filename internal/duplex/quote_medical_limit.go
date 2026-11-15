package duplex

import (
	"regexp"
	"slices"
	"strings"
)

const thirdPartyMedical = "医保外用药责任险-三者"

// Only bind a limit in a selection clause naming the rider. The preceding
// main-policy amount is never evidence for a fixed or shared rider limit.
var medicalSelection = regexp.MustCompile(`(?:^|[，,。 ]|我想|我要|请|再|还)(?:加上|加个|加|把|选)?(?:三者的?医保外(?:用药)?(?:责任险)?|医保外用药责任险-三者)[，, ]*(?:改成|调成|选|要|单独)?([^。？！?!]*)[。？！?!]*$`)
var medicalNegation = regexp.MustCompile(`(?:不要|不想|不用|不需要|别)(?:再)?加`)
var medicalValue = regexp.MustCompile(`^(?:先按|就按|我选|选|要)?(?:单独(?:的|保额)?[，, ]*)?(共享(?:保额)?|[0-9]+万|[一二两三四五六七八九十百]+万)(?:多少钱|吧|就行|，其他不变)?$`)

func spokenMedicalValue(s string) string {
	m := medicalValue.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	if strings.HasPrefix(m[1], "共享") {
		return "共享保额"
	}
	values := map[string]string{"十万": "10万", "十五万": "15万", "二十万": "20万", "三十万": "30万", "五十万": "50万", "一百万": "100万", "一百五十万": "150万", "两百万": "200万", "二百万": "200万", "三百万": "300万", "四百万": "400万"}
	if v := values[m[1]]; v != "" {
		return v
	}
	return m[1]
}

func medicalLimitRequest(text string) (value string, applies bool) {
	m := medicalSelection.FindStringSubmatch(text)
	if m == nil || strings.ContainsAny(text, "比或") || medicalNegation.MatchString(text) {
		return "", false
	}
	if v := spokenMedicalValue(m[1]); v != "" {
		return v, true
	}
	// A request to add the rider without choosing its limit needs a follow-up.
	return "", strings.TrimSpace(m[1]) == "" && strings.Contains(m[0], "加")
}

func (e *Engine) medicalLimitClarification(turn *Turn, ids []string, config map[string]any, configs []map[string]any) string {
	if len(config)+len(configs) == 0 {
		return ""
	}
	variants := configs
	if len(variants) == 0 {
		variants = []map[string]any{config}
	}
	for _, req := range e.tools.unresolvedQuoteRequests {
		if req.ID != turn.ID && !slices.Contains(ids, req.ID) {
			continue
		}
		want, applies := medicalLimitRequest(req.Text)
		if !applies {
			continue
		}
		if req.ID != turn.ID {
			if clarified, ok := medicalLimitRequest(turn.Transcript); ok {
				want = clarified
			} else if v := spokenMedicalValue(strings.Trim(turn.Transcript, " 。！？?!")); want == "" && v != "" {
				want = v
			}
		}
		if want == "" {
			return "请先问清三者医保外用药选共享保额还是单独保额、选哪一档，等来电人回答后再登记。"
		}
		for _, v := range variants {
			if v[thirdPartyMedical] != want {
				return "来电人指定的三者医保外用药档位是" + want + "。请用该档位重新登记；若目录不支持或意思不清，先询问来电人。"
			}
		}
	}
	return ""
}
