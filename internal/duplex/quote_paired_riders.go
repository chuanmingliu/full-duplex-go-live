package duplex

import (
	"regexp"
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
)

const driverMedical = "医保外用药责任险-司机"
const passengerMedical = "医保外用药责任险-乘客"

// Amount-only follow-ups must be a selection, not a comparison, price question,
// negation or a new coverage. Permit ASR restarts as in "你配个，你配个二十万".
var pairedRiderAmount = regexp.MustCompile(`^(?:(?:嗯|那|这个|那个|你给我|给我|你|都|各|就|按|按照|配高一点|配高点|配个|配|改成|改到|调到|提高到)[，,、 ]*)*(共享保额|[0-9一二两三四五六七八九十百]+万)(?:[，, ]*(?:配高一点|其他不变|就行了|就行|吧))*[。！! ]*$`)
var pairedMedicalScope = regexp.MustCompile(`(?:司机(?:跟|和|与|及|、|，|,|还有|的和)?乘客|乘客(?:跟|和|与|及|、|，|,|还有)?司机)[、，, ]*(?:的|两项|各自的)?[、，, ]*医保外(?:用药)?(?:责任险)?`)
var riderCorrection = regexp.MustCompile(`^(?:不对|不是|嗯|那个)+$`)
var directQuoteApproval = regexp.MustCompile(`^(?:(?:不用|没有|没有了)[，, ]+)?(?:你|您)?(?:直接|赶紧)(?:给我)?报(?:价)?(?:就行了?|了吧|吧)?[。！! ]*$`)

func namesOnlyPairedRiders(text string) bool {
	subject := strings.Trim(text, " 。！!")
	loc := pairedMedicalScope.FindStringIndex(subject)
	return loc != nil && loc[0] == 0 && loc[1] == len(subject)
}

func (e *Engine) pairedRiderLimit(text string) string {
	selection := strings.TrimSpace(text)
	if loc := pairedMedicalScope.FindStringIndex(selection); loc != nil {
		// Only strip a subject at the start of the clause; never discard a negation.
		prefix := strings.TrimSpace(selection[:loc[0]])
		if prefix != "" && prefix != "把" && prefix != "那" {
			return ""
		}
		selection = strings.TrimLeft(selection[loc[1]:], "，,、 ")
	} else if !e.recentPairedMedicalScope(selection) {
		return ""
	}
	match := pairedRiderAmount.FindStringSubmatch(selection)
	if match == nil {
		return ""
	}
	return normalizeRiderLimit(match[1])
}

func normalizeRiderLimit(value string) string {
	switch value {
	case "一万":
		return "1万"
	case "二万", "两万":
		return "2万"
	case "五万":
		return "5万"
	}
	return spokenMedicalValue(value)
}

func (e *Engine) recentPairedMedicalScope(current string) bool {
	// The caller owns the selection. The assistant may recap parent policies
	// alongside the riders while answering, without changing the requested scope.
	assistantPair := false
	seenAssistant := false
	for i := len(e.history) - 1; i >= 0; i-- {
		m := e.history[i]
		if m.Role != provider.RoleUser && m.Role != provider.RoleAssistant {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if m.Role == provider.RoleUser {
			// Unanswered fragments may have been joined in one history message.
			parts := strings.Split(text, "\n")
			text = strings.TrimSpace(parts[len(parts)-1])
			if text == current && len(parts) > 1 {
				text = strings.TrimSpace(parts[len(parts)-2])
			}
			if text == current || riderCorrection.MatchString(NormalizePhrase(text)) {
				continue
			}
		}
		if m.Role == provider.RoleUser {
			if pairedMedicalScope.MatchString(text) {
				return true
			}
			// A clipped caller question can use the assistant's immediately
			// preceding clarification only when the caller named both seats and medical coverage.
			return assistantPair && strings.Contains(text, "司机") && strings.Contains(text, "乘客") && strings.Contains(text, "医保")
		}
		if !seenAssistant {
			assistantPair = pairedMedicalScope.MatchString(text)
			seenAssistant = true
		}
	}
	return false
}

func pairedRiderConfig(limit string) map[string]any {
	return map[string]any{driverMedical: limit, passengerMedical: limit}
}

// Bare assent only confirms when the last spoken question asked to approve
// these two riders at the staged limit, not whether more changes are wanted.
var riderTierMention = regexp.MustCompile(`共享保额|[0-9一二两三四五六七八九十百]+万`)
var riderConfirmQuestion = regexp.MustCompile(`(?:对吗|是吗|是吧|确认吗|对吧)[？?]$`)

func (e *Engine) affirmedPairedRiders(text string, p *pendingQuoteRevision) bool {
	if NormalizePhrase(text) != "对" && NormalizePhrase(text) != "确认" {
		return false
	}
	limit, ok := p.Config[driverMedical].(string)
	if !ok || p.Config[passengerMedical] != limit {
		return false
	}
	for i := len(e.history) - 1; i >= 0; i-- {
		m := e.history[i]
		if m.Role == provider.RoleUser && strings.TrimSpace(m.Content) == text {
			continue
		}
		if m.Role == provider.RoleUser {
			return false
		}
		if m.Role == provider.RoleAssistant && m.Content != "" {
			q := strings.TrimSpace(m.Content)
			if !pairedMedicalScope.MatchString(q) || !riderConfirmQuestion.MatchString(q) || strings.Contains(q, "还有") || strings.Contains(q, "其他要改") {
				return false
			}
			tiers := riderTierMention.FindAllString(q, -1)
			if len(tiers) == 0 {
				return false
			}
			for _, tier := range tiers {
				if normalizeRiderLimit(tier) != limit {
					return false
				}
			}
			return true
		}
	}
	return false
}
