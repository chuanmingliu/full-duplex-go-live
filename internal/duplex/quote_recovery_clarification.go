package duplex

import (
	"context"
	"regexp"
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

var quoteConceptQuestion = regexp.MustCompile(`是什么|什么意思|保障什么|怎么赔|赔什么|有什么区别`)
var quoteHoldRequest = regexp.MustCompile(`不要|不用|先别|暂停|等一下|别继续|只说|只讲`)

// The selected concept skill explains the question. The engine supplies the
// next authoritative section so an omitted model transition cannot lose it.
func (e *Engine) prepareQuoteClarification(turn *Turn, guidance []skillsmcp.Guidance, req *provider.LLMRequest) {
	turn.quoteClarification = false
	d := e.currentQuoteDelivery()
	if d == nil || (d.status != "interrupted" && d.status != "pending") || len(d.sections) == 0 || !quoteConceptQuestion.MatchString(turn.Transcript) || quoteHoldRequest.MatchString(turn.Transcript) {
		return
	}
	for _, guide := range guidance {
		if guide.URI == "skill://vehicle_insurance_concepts/SKILL.md" {
			turn.quoteClarification = true
			req.Messages = append(req.Messages, provider.Message{Role: provider.RoleSystem, Content: "本轮是报价中的概念澄清。请简短解释当前问题，不追加提问。解释后引擎会在同一次语音中接上未完成的权威报价段落；本轮由引擎负责接回报价，请勿自行复述金额或追加报价过渡语。"})
			return
		}
	}
}

// Called on the loop after model generation, before the speech pipe closes.
func (e *Engine) quoteClarificationTail(turn *Turn, answer string) string {
	d := e.currentQuoteDelivery()
	if !turn.quoteClarification || d == nil || turn.quoteDelivery != d || turn.quoteDeliveryEpoch != d.epoch || (d.status != "interrupted" && d.status != "pending") {
		return ""
	}
	all := strings.Contains(turn.Transcript, "继续") || strings.Contains(turn.Transcript, "接着")
	var tail strings.Builder
	for _, section := range d.sections {
		if !section.Selected || section.Complete {
			continue
		}
		if !strings.Contains(quotePlaybackText(answer), quotePlaybackText(section.Text)) {
			tail.WriteString(section.Text)
		}
		if !all {
			break
		}
	}
	if tail.Len() == 0 {
		return ""
	}
	e.traceQuoteDelivery(d, turn.ID, "clarification_resume")
	return "接着刚才的报价，" + tail.String()
}

func (e *Engine) finishQuoteClarification(ctx context.Context, turn *Turn, answer string) string {
	ready := make(chan string, 1)
	e.post(func() {
		if ctx.Err() != nil || !e.tracker.IsCurrent(turn) {
			ready <- ""
			return
		}
		ready <- e.quoteClarificationTail(turn, answer)
	})
	select {
	case tail := <-ready:
		return tail
	case <-ctx.Done():
		return ""
	}
}
