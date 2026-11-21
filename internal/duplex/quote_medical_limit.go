package duplex

import (
	"regexp"
	"slices"
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
)

const thirdPartyMedical = "医保外用药责任险-三者"

// Only bind a limit in a selection clause naming the rider. The preceding
// main-policy amount is never evidence for a fixed or shared rider limit.
var medicalSelection = regexp.MustCompile(`(?:^|[，,。 ]|我想|我要|请|再|还)(?:加上|加个|加|把|选)?(?:三者的?医保外(?:用药)?(?:责任险)?|医保外用药责任险-三者)[，, ]*(?:改成|调成|选|要|单独)?[，, ]*([^。？！?!]*)[。？！?!]*$`)
var medicalNegation = regexp.MustCompile(`(?:不要|不想|不用|不需要|别)(?:再)?加`)
var medicalValue = regexp.MustCompile(`^(?:(?:呃|嗯|对)[，, ]*)*(?:先按|就按照|就按|改成|我选|选|要)?(?:单独(?:的|保额)?[，, ]*)?(共享(?:保额)?|[0-9]+万|[一二两三四五六七八九十百]+万)(?:多少钱|算吧|吧|就行|，其他不变)?$`)

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

// ASR can insert a pause inside the compound coverage name.
var medicalNamePause = regexp.MustCompile(`(?:三者|司机|乘客)的?[，, ]+医保外(?:[，, ]+用药)?|医保外[，, ]+用药`)

var removeMedicalNamePause = strings.NewReplacer("，", "", ",", "", " ", "")

func joinMedicalCoverageName(text string) string {
	return medicalNamePause.ReplaceAllStringFunc(text, removeMedicalNamePause.Replace)
}

func medicalLimitRequest(text string) (value string, applies bool) {
	m := medicalSelection.FindStringSubmatch(joinMedicalCoverageName(text))
	if m == nil || strings.ContainsAny(text, "比或") || medicalNegation.MatchString(text) {
		return "", false
	}
	if v := spokenMedicalValue(m[1]); v != "" {
		return v, true
	}
	// A request to add the rider without choosing its limit needs a follow-up.
	return "", strings.TrimSpace(m[1]) == "" && strings.Contains(m[0], "加")
}

// A comparison can answer a pending limit question with two alternatives.
// Bind both independent amounts only when they form a pair in a price question.
var medicalComparison = regexp.MustCompile(`([0-9一二两三四五六七八九十百]+万)[，, ]*(?:跟|和|与)(?:那个|这个)?([0-9一二两三四五六七八九十百]+万)[，, ]*(?:的)?(?:差多少钱|差多少|差价多少|分别多少钱)[。？！?! ]*$`)

func requestedMedicalComparison(text string) []string {
	pair := medicalComparison.FindStringSubmatch(text)
	if pair == nil {
		return nil
	}
	if names := interruptedCoverageNames(text); len(names) > 0 && (len(names) != 1 || names[0] != thirdPartyMedical) {
		return nil
	}
	a, b := spokenMedicalValue(pair[1]), spokenMedicalValue(pair[2])
	if a == "" || b == "" || a == b {
		return nil
	}
	return []string{a, b}
}

func matchesMedicalComparison(pair []string, variants []map[string]any) bool {
	if len(pair) != 2 || len(variants) != 2 {
		return false
	}
	x, y := variants[0][thirdPartyMedical], variants[1][thirdPartyMedical]
	return x == pair[0] && y == pair[1] || x == pair[1] && y == pair[0]
}

// Attach a caller's comparison to the still-open rider question. Keep it on
// that request so interruptions and short approval replies preserve both tiers.
func (e *Engine) retainMedicalComparison(text string) {
	pair := requestedMedicalComparison(text)
	if pair == nil {
		return
	}
	if coverageTopic.FindString(text) == "" {
		for i := len(e.history) - 1; i >= 0; i-- {
			msg := e.history[i]
			if msg.Role != provider.RoleAssistant && msg.Role != provider.RoleUser {
				continue
			}
			if topic := coverageTopic.FindString(msg.Content); topic != "" {
				if !strings.Contains(msg.Content, "医保外") {
					return
				}
				break
			}
		}
	}
	for i := len(e.tools.unresolvedQuoteRequests) - 1; i >= 0; i-- {
		req := &e.tools.unresolvedQuoteRequests[i]
		if want, medical := medicalLimitRequest(req.Text); medical && want == "" {
			req.Comparison = pair
			return
		}
	}
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
		if want == "" && (matchesMedicalComparison(req.Comparison, configs) || matchesMedicalComparison(requestedMedicalComparison(turn.Transcript), configs)) {
			continue
		}
		if want == "" && len(req.Comparison) == 2 {
			return "来电人只比较三者医保外用药" + req.Comparison[0] + "和" + req.Comparison[1] + "。请用两个 configs 登记比较并等待确认，不需要选择其中一档。"
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
