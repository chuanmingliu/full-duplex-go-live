package main

import (
	"regexp"
	"strings"
)

const placeOrderSkill = "place-order"

const productPage = `(?:产品页|购买页|投保页|下单页)`
const coverageChoice = `(?:保障|险种|保额|保单|组合|方案|档位|三者|车损|医保)`

var purchaseIntent = regexp.MustCompile(`(?:我要|我想|帮我|给我|我就|现在|直接|确认)(?:下单|购买|买这|投保|付款|支付)|(?:怎么|如何|哪里|在哪).{0,8}(?:下单|购买|买这个|投保|付款|支付)|(?:下单|投保|购买|付款|支付)(?:链接|入口)|(?:就按|按这个|就选这|就这份).{0,12}(?:买|投保|下单|办理)|` +
	productPage + `.{0,24}(?:(?:调整|修改|更改|变更|勾选)|(?:换|改|选|增减).{0,12}` + coverageChoice + `|` + coverageChoice + `.{0,12}(?:改|换|选|增减))|(?:调整|修改|更改|变更|选择|勾选).{0,24}` + productPage + `|(?i:\b(?:place (?:an?|the) order|continue with the insurance purchase|checkout)\b)`)
var purchaseRefusal = regexp.MustCompile(`(?:不想|不要|不用|不需要|先不|暂时不|别|取消).{0,12}(?:买|下单|购买|投保|付款|支付|改|调整|选择)|(?:不买|不办|不考虑|先不用)[了。！! ]*$`)

var orderAcceptance = regexp.MustCompile(`^(?:那|好[，, ]*)?(?:我)?就按这(?:个|份|档|个方案)?[吧。！! ]*$`)

func hasOrderContext(current, previous string) bool {
	return purchaseIntent.MatchString(current) || (orderAcceptance.MatchString(strings.TrimSpace(current)) &&
		purchaseIntent.MatchString(previous) && !purchaseRefusal.MatchString(previous))
}
