package duplex

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
)

var quoteApproval = regexp.MustCompile(`^(?:(?:嗯|呃|对|好)[，。！!、 ]*)*(?:没有(?:了|其他要(?:改|调整)的)?|就这样吧|(?:你|您)?(?:先|直接)?算(?:一下)?吧|确认报价)[。！! ]*$`)

func (e *Engine) applyQuoteTurnPolicy(turn *Turn, req *provider.LLMRequest) {
	if !e.confirmsQuoteRevisions() || !e.hasQuotedSelection() || turn.waitingTask != "" || turn.Transcript == "" {
		return
	}
	text := strings.TrimSpace(turn.Transcript)
	note := ""
	force := false
	pending := e.tools.quoteRevision
	pairedLimit := e.pairedRiderLimit(text)
	switch {
	case pending != nil && pending.TurnID != turn.ID && len(e.tools.unresolvedQuoteRequests) == 0 && e.callerApprovesQuote(text, pending):
		note = "来电人明确确认刚才已登记的范围。现在调用 quote_total，revision_action=confirm，revision_id=" + pending.ID + "。只在工具接受任务后说正在计算。"
		force = true
	case namesOnlyPairedRiders(text):
		note = "来电人指的是司机与乘客两项医保外附加险。只问这两项分别或共同要配多少保额；尚未给出档位，不登记或计算，不调整座位主险。"
	case pairedLimit != "":
		config := pairedRiderConfig(pairedLimit)
		schema := e.quoteSchema()
		if len(schema.Options[driverMedical]) == 0 || len(schema.Options[passengerMedical]) == 0 {
			return
		}
		if note = e.unsupportedRevisionTier(config, nil); note == "" {
			args, _ := json.Marshal(config)
			note = "现在调用 quote_total 一次，同时登记两项附加险 config=" + string(args) + "。登记后询问是否确认报价。保留原主险和其他险种。"
			force = true
		}
	default:
		limit, explicit := medicalLimitRequest(text)
		if explicit && limit != "" && len(interruptedCoverageNames(text)) == 1 {
			options := e.quoteSchema().Options[thirdPartyMedical]
			if len(options) > 0 && !slices.Contains(options, limit) {
				note = "当前三者医保外用药不提供来电人所说的档位。请告诉来电人已配置的可选档位：" + strings.Join(options, "、") + "，请对方选择。不要计算或替对方改档。"
			} else if slices.Contains(options, limit) {
				args, _ := json.Marshal(map[string]any{thirdPartyMedical: limit})
				note = "这是明确的三者医保外用药调整。现在调用 quote_total 登记 config=" + string(args) + "。如有对应的未登记 request_id 一并处理。等待工具返回登记结果后再复述和询问确认。"
				force = true
			}
		}
	}
	if note != "" {
		req.Messages = append(req.Messages, provider.Message{Role: provider.RoleSystem, Content: note})
	}
	if force && len(req.Tools) > 0 && (req.ToolChoice == nil || req.ToolChoice == "auto") {
		req.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": "quote_total"}}
	}
}

func (e *Engine) callerApprovesQuote(text string, pending *pendingQuoteRevision) bool {
	return directQuoteApproval.MatchString(text) || (quoteApproval.MatchString(text) && e.quoteApprovalInContext(text)) || e.affirmedPairedRiders(text, pending)
}

// A short "没有" is consent only in response to the no-more-changes question.
func (e *Engine) quoteApprovalInContext(text string) bool {
	if !strings.Contains(text, "没有") {
		return true
	}
	for i := len(e.history) - 1; i >= 0; i-- {
		msg := e.history[i]
		switch msg.Role {
		case provider.RoleAssistant:
			if msg.Content != "" {
				return strings.ContainsAny(msg.Content, "？?") &&
					(strings.Contains(msg.Content, "还有") || strings.Contains(msg.Content, "还需要")) &&
					strings.ContainsAny(msg.Content, "改调")
			}
		case provider.RoleUser:
			return false
		}
	}
	return false
}

func (e *Engine) quotePricingStateNote() string {
	if !e.confirmsQuoteRevisions() || !e.hasQuotedSelection() {
		return ""
	}
	for _, task := range e.tools.tasks {
		if task.Tool.Name == "quote_total" && e.tools.quoteRevision == nil && len(e.tools.unresolvedQuoteRequests) == 0 {
			return "当前报价任务已提交。依据后台任务及实际工具结果说明状态，价格以返回结果为准。"
		}
	}
	return "当前没有与来电人新要求对应的在途报价。对话中先前说过“登记了”或“在算”不能作为依据。不得说还在算、快好了或让来电人继续等。未登记先登记，待确认先核对范围；问结果时说明目前的实际状态。活动目录未提供的险种、档位或组合，请说明不支持本次报价，告诉来电人相关可选保障或档位并请其选择。"
}

// Check configured tier values before saving a revision. CRM still validates
// the complete combination and its required fields before calculating.
func (e *Engine) unsupportedRevisionTier(config map[string]any, configs []map[string]any) string {
	options := e.quoteSchema().Options
	if len(options) == 0 {
		return ""
	}
	for _, variant := range append([]map[string]any{config}, configs...) {
		for name, value := range variant {
			tier, ok := value.(string)
			allowed := options[name]
			if ok && len(allowed) > 0 && !slices.Contains(allowed, tier) {
				return name + "当前不提供" + tier + "。请告诉来电人此保障可选：" + strings.Join(allowed, "、") + "，请对方选择；不要说已登记或正在计算。"
			}
		}
	}
	return ""
}
