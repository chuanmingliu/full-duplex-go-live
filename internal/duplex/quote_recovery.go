package duplex

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

// quoteDelivery lives on the engine loop, independently of trimmed history.
// Its task retains the authoritative prices and their input snapshot.
type quoteDelivery struct {
	task          *tools.Task
	initialTurn   string
	status        string
	sections      []quoteSection
	interruptions int
	epoch         int
}

type quoteSection struct {
	ID          int    `json:"id"`
	Text        string `json:"text"`
	Complete    bool   `json:"complete"`
	Selected    bool   `json:"selected"`
	completedBy *Turn
}

var quoteDeliveryTool = tools.Tool{
	Name: "quote_delivery", Builtin: "quote_delivery",
	Description: "保存当前报价的讲解控制，不能只口头答应而不调用。客户说等一下用 pause，说不用报价了/结束这个话题用 cancel，只讲指定档位用 resume 并填写 section_ids，明确继续用 resume。pause/cancel 请在同次回复先说一句简短确认，再调用工具。resume 可先按已知 text 说出客户要求的段落，再调用以保存范围。task_id 必须来自当前报价进度。resume 的 section_ids 可选，只讲这些段落（也可重讲已完成段落）；省略则继续未完成段落。普通澄清问答后直接按进度继续，无需调用本工具。改变投保方案仍调用 quote_total，不在此修改。",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"action":{"type":"string","enum":["pause","cancel","resume"]},"section_ids":{"type":"array","items":{"type":"integer"},"minItems":1,"maxItems":64}},"required":["task_id","action"],"additionalProperties":false}`),
}

var quoteScopeRequest = regexp.MustCompile(`^(?:请)?只(?:说|讲|介绍)[^。！？?]*档`)

// An explicit request to explain only certain tiers must save its scope even
// if the model would otherwise answer with text alone. The model still selects
// section IDs from the authoritative readout. Ordinary questions stay auto.
func (e *Engine) requireQuoteScopeControl(turn *Turn, req *provider.LLMRequest) {
	d := e.currentQuoteDelivery()
	if d == nil || d.status == "superseded" || len(d.sections) == 0 || !quoteScopeRequest.MatchString(strings.TrimSpace(turn.Transcript)) {
		return
	}
	if req.ToolChoice != nil && req.ToolChoice != "auto" {
		return
	}
	req.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": quoteDeliveryTool.Name}}
}

func (e *Engine) startQuoteDelivery(task *tools.Task, turn *Turn) {
	if !e.opts.Cfg.Duplex.QuoteInterruptionRecovery || !turn.QuoteResult {
		return
	}
	if previous := e.tools.quoteDelivery; previous != nil {
		previous.status = "superseded"
		e.traceQuoteDelivery(previous, turn.ID, "replacement")
	}
	delivery := &quoteDelivery{task: task, initialTurn: turn.ID, status: "queued"}
	if turn.Readout == "" {
		delivery.status = "pending"
	}
	for i, text := range quoteSections(turn.Readout) {
		delivery.sections = append(delivery.sections, quoteSection{ID: i + 1, Text: text, Selected: true})
	}
	e.tools.quoteDelivery = delivery
	e.traceQuoteDelivery(delivery, turn.ID, "created")
}

// Split at sentence/option boundaries, retaining commas inside a coverage and
// its premium. The tail is kept intact if a custom template has many sections.
func quoteSections(text string) []string {
	var sections []string
	start := 0
	for i, r := range text {
		if !strings.ContainsRune("。！？；;\n", r) || len(sections) >= 63 {
			continue
		}
		end := i + len(string(r))
		if section := strings.TrimSpace(text[start:end]); section != "" {
			sections = append(sections, section)
		}
		start = end
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		sections = append(sections, tail)
	}
	return sections
}

func quotePlaybackText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("，。!！?？;；：:\"“”", r) {
			return -1
		}
		return r
	}, text)
}

func (e *Engine) currentQuoteDelivery() *quoteDelivery {
	if e.tools == nil || !e.opts.Cfg.Duplex.QuoteInterruptionRecovery {
		return nil
	}
	d := e.tools.quoteDelivery
	if d == nil || d.status == "superseded" {
		return d
	}
	memory, version := e.memorySnapshot()
	if version != d.task.Request.MemoryVersion && !quoteResultMatchesMemory(d.task, memory) {
		d.status = "superseded"
		e.traceQuoteDelivery(d, "", "inputs_changed")
	}
	return d
}

func (e *Engine) quoteRecoveryPrompt(turn *Turn) string {
	d := e.currentQuoteDelivery()
	if d == nil {
		return ""
	}
	if turn != nil {
		turn.quoteDelivery = d
		turn.quoteDeliveryEpoch = d.epoch
	}
	state, _ := json.Marshal(struct {
		TaskID   string         `json:"task_id"`
		Status   string         `json:"status"`
		Sections []quoteSection `json:"sections"`
	}{d.task.ID, d.status, d.sections})
	facts := ""
	if e.tools.lastResult != d.task {
		facts = "\n本次报价的权威工具结果：" + lastResultLine(d.task)
	}
	return "【报价讲解进度】" + string(state) + facts + `
进度依据服务端播放估计。报价事实以工作记忆中的工具结果为准。先回答客户当前问题。
queued 表示原始播报正在排队，本次结果回复让系统播报前缀承载报价，后续只简短衔接，不重念。
客户仅澄清本方案的保障含义时，简短解释后自然接回下一个未完成的 selected 段落；客户要求继续全部比较时讲完所选剩余段落。接回时按 text 原文完整说出该段，保额与保费一起说，中途被打断的数字从整段重说。无需再次查询已有价格，也无需单独调用工具来回答澄清。概念技能用于回答疑问，随后依照此进度回到原先尚未结束的报价。
客户仅问一项价格时只答该项。客户明确要求继续、暂停、取消或缩小讲解范围时，必须调用 quote_delivery 保存选择，不能仅口头答应；“不用报价了/结束这个话题”调用 cancel，“等一下”调用 pause，“只讲某档”调用 resume 并指定 section_ids。pause/cancel 在同次回复中先说一句简短确认，再调用保存状态，无需追加问题；resume 可先按 text 说所选段落，再调用保存范围。客户换话题时先回答新话题，保留进度供后续明确返回，勿主动接回长报价。
paused 或 cancelled 状态等待客户明确要求继续；complete 状态无需主动重讲；superseded 的报价不得作为当前方案续讲。客户修改方案时调用 quote_total，等待新结果。不要把生成、排队的文本当作已播放完成。
sections 为空表示这次使用自由讲解，依据工具金额和已播放的对话补全客户所需说明，不声称知道精确段落进度。` + e.quoteRevisionRecoveryNote(turn)
}

// A local delivery control needs no further completion when this response
// already contains its acknowledgement or the complete requested readout.
func (e *Engine) quoteControlAnswered(p *pendingResponse) bool {
	if len(p.calls) == 0 || p.turn == nil {
		return false
	}
	for _, call := range p.calls {
		if call.Name != quoteDeliveryTool.Name || !strings.HasPrefix(p.outputs[call.ID], "【报价讲解进度】") {
			return false
		}
	}
	p.turn.mu.Lock()
	text := strings.TrimSpace(strings.TrimPrefix(p.turn.Response, p.turn.Opening+p.turn.Readout))
	p.turn.mu.Unlock()
	if text == "" {
		return false
	}
	d := e.currentQuoteDelivery()
	if d == nil || d.status == "superseded" {
		return false
	}
	if d.status == "paused" || d.status == "cancelled" {
		return true
	}
	if len(d.sections) == 0 {
		return false
	}
	for _, section := range d.sections {
		if section.Selected && !section.Complete && !strings.Contains(quotePlaybackText(text), quotePlaybackText(section.Text)) {
			return false
		}
	}
	return true
}

// A fast result can overtake the calling turn's "running" continuation.
// Once delivered, that continuation must not start new speech at the next gap.
func (e *Engine) deliveredQuoteCovers(p *pendingResponse) bool {
	if e.tools == nil || e.tools.lastResult == nil || len(p.calls) == 0 {
		return false
	}
	for _, call := range p.calls {
		var output struct {
			Status string `json:"status"`
			TaskID string `json:"task_id"`
		}
		if call.Name != "quote_total" || json.Unmarshal([]byte(p.outputs[call.ID]), &output) != nil || output.Status != "running" || output.TaskID != e.tools.lastResult.ID {
			return false
		}
	}
	return true
}

// Keep call-owned delivery state after supplementary skill instructions.
func quoteRecoveryLast(messages []provider.Message) []provider.Message {
	for i, message := range messages {
		if message.Name == "golive_quote_delivery" {
			copy(messages[i:], messages[i+1:])
			messages[len(messages)-1] = message
			break
		}
	}
	return messages
}

func (e *Engine) applyQuoteDeliveryCall(call provider.ToolCall, turn *Turn) string {
	var args struct {
		TaskID   string `json:"task_id"`
		Action   string `json:"action"`
		Sections []int  `json:"section_ids"`
	}
	decoder := json.NewDecoder(strings.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return `{"error":"invalid quotation delivery arguments"}`
	}
	if decoder.Decode(new(any)) != io.EOF {
		return `{"error":"expected one quotation delivery object"}`
	}
	d := e.currentQuoteDelivery()
	if d == nil || d.status == "superseded" || args.TaskID != d.task.ID || turn == nil || turn.Dead() {
		return `{"error":"quotation is unavailable or superseded"}`
	}
	if args.Action != "pause" && args.Action != "cancel" && args.Action != "resume" {
		return `{"error":"unknown quotation delivery action"}`
	}
	selected := map[int]bool{}
	for _, id := range args.Sections {
		if args.Action != "resume" || id < 1 || id > len(d.sections) || selected[id] {
			return `{"error":"invalid quotation section selection"}`
		}
		selected[id] = true
	}
	d.epoch++
	switch args.Action {
	case "pause":
		d.status = "paused"
	case "cancel":
		d.status = "cancelled"
	case "resume":
		if len(selected) > 0 {
			for i := range d.sections {
				d.sections[i].Selected = selected[d.sections[i].ID]
				if d.sections[i].Selected {
					d.sections[i].Complete = false
					d.sections[i].completedBy = nil
				}
			}
		}
		d.status = "pending"
		if len(d.sections) > 0 {
			d.status = "complete"
			for _, section := range d.sections {
				if section.Selected && !section.Complete {
					d.status = "pending"
					break
				}
			}
		}
	}
	e.traceQuoteDelivery(d, turn.ID, args.Action)
	// A short spoken control can finish before its tool call is processed.
	// Reapply that receipt to the new scope, using played text only.
	turn.quoteDelivery, turn.quoteDeliveryEpoch = d, d.epoch
	turn.mu.Lock()
	played, natural := turn.Spoken, turn.completedNaturally
	turn.mu.Unlock()
	if natural {
		e.noteQuotePlayback(turn, PlaybackReport{SpokenText: played}, false)
	}
	return e.quoteRecoveryPrompt(turn)
}

// Matching complete played text is deliberately conservative. A late
// truncation may revoke completion previously attributed to the same turn.
func (e *Engine) noteQuotePlayback(turn *Turn, report PlaybackReport, truncated bool) {
	d := e.currentQuoteDelivery()
	if d == nil || turn.quoteDelivery != d || turn.quoteDeliveryEpoch != d.epoch || d.status == "superseded" {
		return
	}
	heard := quotePlaybackText(report.SpokenText)
	changed := false
	remaining := 0
	for i := range d.sections {
		s := &d.sections[i]
		text := quotePlaybackText(s.Text)
		if truncated && s.completedBy == turn && !strings.Contains(heard, text) {
			s.Complete = false
			s.completedBy = nil
			changed = true
		}
		if s.Selected && !s.Complete && text != "" && strings.Contains(heard, text) {
			s.Complete = true
			s.completedBy = turn
			changed = true
		}
		if s.Selected && !s.Complete {
			remaining++
		}
	}
	if d.status != "paused" && d.status != "cancelled" {
		if remaining == 0 && len(d.sections) > 0 {
			d.status = "complete"
		} else if truncated {
			d.status = "interrupted"
			d.interruptions++
		} else if turn.ID == d.initialTurn && len(d.sections) == 0 {
			d.status = "complete"
		} else {
			d.status = "pending"
		}
	}
	if changed || truncated || turn.ID == d.initialTurn {
		e.traceQuoteDelivery(d, turn.ID, "playback")
	}
}

func (e *Engine) traceQuoteDelivery(d *quoteDelivery, turn, reason string) {
	var completed, remaining []int
	for _, s := range d.sections {
		if !s.Selected {
			continue
		}
		if s.Complete {
			completed = append(completed, s.ID)
		} else {
			remaining = append(remaining, s.ID)
		}
	}
	e.log.Info("quote: delivery progress", "task", d.task.ID, "turn", turn,
		"status", d.status, "reason", reason, "memory_version", d.task.Request.MemoryVersion,
		"completed", fmt.Sprint(completed), "remaining", fmt.Sprint(remaining), "interruptions", d.interruptions)
}
