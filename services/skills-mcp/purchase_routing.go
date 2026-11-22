package main

import (
	"regexp"
	"slices"
	"strings"
)

const wechatNotificationSkill = "wechat-service-notification"

// Checkout intent can bring the guidance into the prompt before consent.
// The original working memory remains authoritative for any next action.
var purchaseIntent = regexp.MustCompile(`(?:我要|我想|帮我|给我|我就|现在|直接|确认)(?:下单|购买|买这|投保|付款|支付)|(?:怎么|如何|哪里|在哪).{0,8}(?:下单|购买|买这个|投保|付款|支付)|(?:下单|投保|购买|付款|支付)(?:链接|入口)|(?:就按|按这个|就选这|就这份).{0,12}(?:买|投保|下单|办理)|^(?:那|好[，, ]*)?(?:我)?就按这(?:个|份|档|个方案)?[吧。！! ]*$`)
var purchaseRefusal = regexp.MustCompile(`(?:不想|不要|不用|不需要|先不|暂时不|别|取消).{0,10}(?:买|下单|购买|投保|付款|支付|加微信)|(?:不买|不办|不考虑)[了。！! ]*$`)

func (t turnInput) purchaseIntent() bool {
	text := strings.TrimSpace(t.Utterance)
	return t.Event == "auto" && purchaseIntent.MatchString(text) &&
		!purchaseRefusal.MatchString(text) && !trailingRecallRefusal.MatchString(text)
}

func (s Skill) stateAllowed(state map[string]string, checkout bool) bool {
	checkout = checkout && s.Name == wechatNotificationSkill
	if checkout && (state["wechat.status"] == "declined" || state["wechat.status"] == "added") {
		return false
	}
	for field, allowed := range s.State {
		value := state[field]
		if checkout && field == "wechat.status" {
			switch value {
			case "", "unknown":
				continue
			}
		}
		if !slices.Contains(allowed, value) {
			return false
		}
	}
	return true
}
