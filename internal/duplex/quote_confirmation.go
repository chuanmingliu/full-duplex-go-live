package duplex

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

const quoteConfirmationInstructions = `本通话开启后续报价确认。已有报价后，客户改保障、问新方案价格或要求比较档位，都先简短复述待报价范围并自然询问是否还有其他保障要调整，并在同一轮必须调用一次 quote_total 登记，不能只问不保存；这时只保存，不启动计算。已有待确认范围时，客户每次增加或删除保障，必须先调用 quote_total 更新登记，再依据返回的完整待确认范围复述；口头说已调整不等于保存。例如已有三者300万待确认，客户说“我还想去掉车损险”，先传 config={"车损险":false}，不能沿用旧 revision_id 直接确认。修改选定方案用 config；假设或比较用 configs（全部档位也可用 compare_coverage），确认比较不会修改已选方案。收到 awaiting_confirmation 后，本轮已经问过就不要重复，否则补问。后续修改比较范围时传完整更新后的 configs；改为选定一个方案才用 config。只说具体险种和选项，不概括改动项数；结合上下文变换措辞，不机械重复上一句，不说已经在计算。只有客户在后续发言明确确认报价范围全部确定，才传 revision_action="confirm" 和当前 revision_id，不再传 config/configs。按上一问理解回答：“还有别的要改吗”的“有”表示还有改动，“没有了，就这样”表示结束调整。含糊回答先澄清，不从沉默或无关肯定推断同意。客户仅说“车损再改改”或“算一下这些”且具体选项尚不明确时，先按活动目录问清是否投保等具体选择，不能猜测保额或直接计算。取消用 revision_action="cancel" 和当前 revision_id。不得用 update_memory.quote 绕过确认。首次报价仍用原流程。确认后计算中的简短回应由你按上下文表达，避免重复固定等待话术。`

const quoteRevisionSaved = `本次提交已登记，尚未计算。不要重复提交已登记的相同参数或已处理的 request_id。若主提示仍列有尚未登记的要求，先处理其余要求；否则直接依据 pending_revision 复述具体保障和选项，自然询问还有哪些保障需要调整，等待来电人的下一次发言。本轮已经完整问过就结束，不重复询问。不概括调整项数，不说正在计算。`

type pendingQuoteRevision struct {
	ID          string           `json:"revision_id"`
	Config      map[string]any   `json:"config,omitempty"`
	Configs     []map[string]any `json:"configs,omitempty"`
	Memory      json.RawMessage  `json:"-"`
	PlanVersion int              `json:"-"`
	TurnID      string           `json:"-"`
}

func (e *Engine) confirmsQuoteRevisions() bool {
	if e.tools == nil {
		return false
	}
	speech := e.tools.byName["quote_total"].Speech
	return e.tools.memory != nil && speech != nil && speech.ConfirmRevisions
}

func (e *Engine) hasQuotedSelection() bool {
	result := e.tools.lastResult
	return e.tools.quotedSelection || e.tools.confirmedRevision != "" || (result != nil && result.Tool.Name == "quote_total" && tools.ResultStatus(result.Result.Output) == "quote")
}

func quoteConfirmationParameters(raw json.RawMessage) json.RawMessage {
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil || schema == nil {
		return raw
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		schema["properties"] = props
	}
	props["revision_action"] = map[string]any{"type": "string", "enum": []string{"confirm", "cancel"}, "description": "Only after a later caller turn confirms the pending pricing scope, or cancels it. Omit config/configs/compare_coverage."}
	props["resolved_request_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "IDs of interrupted quotation requests reconciled by this config/configs update. Use the exact request_id values from the host. May also accompany cancel when no pending revision exists. Never use with confirm."}
	props["revision_id"] = map[string]any{"type": "string", "description": "Exact current pending revision_id returned by this host."}
	if required, ok := schema["required"].([]any); ok {
		filtered := make([]any, 0, len(required))
		for _, name := range required {
			if name != "config" {
				filtered = append(filtered, name)
			}
		}
		schema["required"] = filtered
	}
	out, _ := json.Marshal(schema)
	return out
}

// Confirmed follow-up requests use generated acknowledgment. First quotes
// retain their configured speech and ready-result suppression.
func (e *Engine) confirmationTool(t tools.Tool, c provider.ToolCall) tools.Tool {
	if c.Name != "quote_total" || !e.confirmsQuoteRevisions() {
		return t
	}
	if confirmedQuoteCall(c) {
		t.Ack = ""
	}
	return t
}

func confirmedQuoteCall(c provider.ToolCall) bool {
	var args struct {
		Action string `json:"revision_action"`
	}
	return c.Name == "quote_total" && json.Unmarshal([]byte(c.Arguments), &args) == nil && args.Action == "confirm"
}

func (e *Engine) quoteConfirmationPrompt() string {
	if !e.confirmsQuoteRevisions() {
		return ""
	}
	text := "\n【二次报价确认规则】" + quoteConfirmationInstructions
	if pending := e.tools.quoteRevision; pending != nil {
		raw, _ := json.Marshal(pending)
		text += "\n【待确认报价范围，尚未计算】" + string(raw)
	}
	return text + e.unresolvedQuotePrompt()
}

func revisionReply(status, note string, pending *pendingQuoteRevision) string {
	raw, _ := json.Marshal(map[string]any{"status": status, "note": note, "pending_revision": pending})
	return string(raw)
}

// Runs on the engine loop before memory mutation or any pricing dispatch.
// The model interprets contextual consent; this guard binds it to a pending
// selection, a later caller turn, and unchanged pricing inputs.
func (e *Engine) prepareQuoteRevision(turn *Turn, call provider.ToolCall) (provider.ToolCall, string) {
	if call.Name != "quote_total" || !e.confirmsQuoteRevisions() {
		return call, ""
	}
	var args struct {
		Config   map[string]any   `json:"config"`
		Configs  []map[string]any `json:"configs"`
		Action   string           `json:"revision_action"`
		ID       string           `json:"revision_id"`
		Resolved []string         `json:"resolved_request_ids"`
	}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return call, revisionReply("invalid", "报价参数无法解析，请纠正参数。", nil)
	}
	if !e.validResolvedQuoteRequests(args.Resolved) || len(args.Resolved) > 0 && args.Action != "cancel" && (args.Action != "" || len(args.Config)+len(args.Configs) == 0) {
		return call, revisionReply("invalid", "resolved_request_ids 必须对应尚未处理的要求，并与 config 或 configs 更新一起提交。", e.tools.quoteRevision)
	}
	if args.Action == "cancel" && args.ID == "" && e.tools.quoteRevision == nil && len(args.Resolved) > 0 && len(args.Config)+len(args.Configs) == 0 {
		e.reconcileQuoteRequests(turn, args.Resolved)
		return call, revisionReply("cancelled", "已撤销指定的未登记要求，保留原已确认方案。", nil)
	}
	if args.Action != "" || args.ID != "" {
		return e.resolveQuoteRevision(turn, call, args.Action, args.ID, len(args.Config) > 0 || len(args.Configs) > 0)
	}
	if note := e.medicalLimitClarification(turn, args.Resolved, args.Config, args.Configs); note != "" {
		return call, revisionReply("unresolved_changes", note, e.tools.quoteRevision)
	}
	if !e.resolutionIncludesRequestedCoverages(turn, args.Resolved, args.Config, args.Configs) {
		return call, revisionReply("unresolved_changes", "本次 config/configs 未包含所处理要求的全部 required_coverages。请登记对应保障的实际变化，不得只修改主险后将附加险要求标为已处理。", e.tools.quoteRevision)
	}
	if !e.hasQuotedSelection() {
		return call, ""
	}
	pending := e.tools.quoteRevision
	if len(args.Configs) > 0 {
		if len(args.Config) > 0 {
			return call, revisionReply("invalid", "比较请把全部改动放进各个 configs，不要同时传 config。", pending)
		}
		for i, variant := range args.Configs {
			if !validRevisionConfig(variant) {
				return call, revisionReply("invalid", "每个比较方案必须包含明确的布尔值或档位字符串。", pending)
			}
			// Carry a previously staged selection into every hypothetical option.
			merged := map[string]any{}
			if pending != nil {
				for key, value := range pending.Config {
					merged[key] = value
				}
			}
			for key, value := range variant {
				merged[key] = value
			}
			args.Configs[i] = merged
		}
		memory, _ := e.memorySnapshot()
		reply := e.stageQuoteRequest(turn, nil, args.Configs, memory)
		e.reconcileQuoteRequests(turn, args.Resolved)
		return call, reply
	}
	if len(args.Config) == 0 {
		if len(e.tools.unresolvedQuoteRequests) > 0 {
			return call, revisionReply("unresolved_changes", "请先登记被打断后尚未处理的保障要求，再确认完整报价范围。", pending)
		}
		if pending != nil {
			return call, revisionReply("awaiting_confirmation", quoteRevisionSaved, pending)
		}
		return call, ""
	}
	if !validRevisionConfig(args.Config) {
		return call, revisionReply("invalid", "保障选项必须是布尔值或档位字符串。", pending)
	}
	memory, _ := e.memorySnapshot()
	changes := map[string]any{}
	if pending != nil {
		for key, value := range pending.Config {
			changes[key] = value
		}
	}
	for key, value := range args.Config {
		changes[key] = value
	}
	var state struct {
		Quote map[string]any `json:"quote"`
	}
	_ = json.Unmarshal(memory, &state)
	unchanged := len(state.Quote) > 0
	for key, value := range changes {
		if !reflect.DeepEqual(value, state.Quote[key]) {
			unchanged = false
		}
	}
	if unchanged {
		e.reconcileQuoteRequests(turn, args.Resolved)
		if len(e.tools.unresolvedQuoteRequests) > 0 {
			return call, revisionReply("unresolved_changes", "当前参数没有处理全部被打断的要求，请继续核对完整范围。", pending)
		}
		if pending == nil && len(args.Resolved) == 0 {
			return call, ""
		}
		e.tools.quoteRevision = nil
		return call, revisionReply("unchanged", "调整后的保障与当前已确认方案相同，使用现有有效报价；不要再说正在计算。", nil)
	}
	reply := e.stageQuoteRequest(turn, changes, nil, memory)
	e.reconcileQuoteRequests(turn, args.Resolved)
	return call, reply
}

func validRevisionConfig(config map[string]any) bool {
	if len(config) == 0 {
		return false
	}
	for _, value := range config {
		switch value.(type) {
		case bool, string:
		default:
			return false
		}
	}
	return true
}

func (e *Engine) stageQuoteRequest(turn *Turn, config map[string]any, configs []map[string]any, memory json.RawMessage) string {
	pending := e.tools.quoteRevision
	if pending != nil && reflect.DeepEqual(config, pending.Config) && reflect.DeepEqual(configs, pending.Configs) && e.quoteRevisionCurrent(pending, memory) {
		return revisionReply("awaiting_confirmation", quoteRevisionSaved, pending)
	}
	e.tools.quoteRevisionSerial++
	pending = &pendingQuoteRevision{ID: fmt.Sprintf("revision-%d", e.tools.quoteRevisionSerial), Config: config, Configs: configs, Memory: append(json.RawMessage(nil), memory...), PlanVersion: e.quotePlanVersion("quote_total"), TurnID: turn.ID}
	e.tools.quoteRevision = pending
	if delivery := e.currentQuoteDelivery(); delivery != nil {
		delivery.status = "paused"
		delivery.epoch++
		e.traceQuoteDelivery(delivery, turn.ID, "revision_pending")
	}
	e.log.Info("quote: revision awaiting confirmation", "revision", pending.ID, "turn", turn.ID, "coverages", len(config), "variants", len(configs))
	return revisionReply("awaiting_confirmation", quoteRevisionSaved, pending)
}

func (e *Engine) quoteRevisionCurrent(pending *pendingQuoteRevision, memory json.RawMessage) bool {
	task := &tools.Task{Tool: tools.Tool{Name: "quote_total"}, Request: tools.Request{Memory: pending.Memory}}
	return pending.PlanVersion == e.quotePlanVersion("quote_total") && quoteInputsUnchanged(task, memory)
}

func (e *Engine) resolveQuoteRevision(turn *Turn, call provider.ToolCall, action, id string, hasChanges bool) (provider.ToolCall, string) {
	pending := e.tools.quoteRevision
	if (action != "confirm" && action != "cancel") || id == "" || hasChanges {
		return call, revisionReply("invalid", "确认或取消只能传当前 revision_id 和 revision_action；新的选定方案用 config，比较用完整 configs 重新登记。", pending)
	}
	if id == e.tools.confirmedRevision && pending == nil {
		return call, revisionReply("already_confirmed", "该调整已提交，不要重复计算。等待其结果或回答新的问题。", nil)
	}
	if pending == nil || pending.ID != id {
		return call, revisionReply("expired", "该确认已失效。请依据当前待确认改动再次与客户确认。", pending)
	}
	if action == "cancel" {
		turn.quoteRevisionHandled = true
		e.tools.quoteRevision = nil
		e.tools.unresolvedQuoteRequests = nil
		e.log.Info("quote: revision cancelled", "revision", id, "turn", turn.ID)
		return call, revisionReply("cancelled", "已取消待确认改动，保留原已确认方案。按上下文简短回应。", nil)
	}
	if len(e.tools.unresolvedQuoteRequests) > 0 {
		return call, revisionReply("unresolved_changes", "先处理被打断后尚未登记的要求。用 config/configs 和 resolved_request_ids 更新完整范围，再请您确认。", pending)
	}
	if turn.ID == pending.TurnID || turn.Transcript == "" {
		return call, revisionReply("awaiting_confirmation", "必须等待客户后续发言确认，当前不能提交计算。", pending)
	}
	memory, _ := e.memorySnapshot()
	if !e.quoteRevisionCurrent(pending, memory) {
		return call, revisionReply("expired", "报价依据已经改变。保留的待确认改动在 pending_revision 中，请按 config 或 configs 重新登记并确认。", pending)
	}
	args := map[string]any{"config": pending.Config}
	if len(pending.Configs) > 0 {
		args = map[string]any{"configs": pending.Configs}
	}
	raw, _ := json.Marshal(args)
	call.Arguments = string(raw)
	e.tools.quoteRevision = nil
	e.tools.confirmedRevision = id
	e.log.Info("quote: revision confirmed", "revision", id, "turn", turn.ID, "coverages", len(pending.Config), "variants", len(pending.Configs))
	return call, ""
}

// A complete confirmation question can stream before the state-only tool call.
// Record the tool response normally, then avoid generating the same question
// a second time. Interrupted or unrelated replies still take the normal path.
func (e *Engine) quoteConfirmationAnswered(p *pendingResponse) bool {
	if !e.confirmsQuoteRevisions() || p.turn == nil || len(p.calls) != 1 || p.calls[0].Name != "quote_total" {
		return false
	}
	pending := e.tools.quoteRevision
	if pending == nil || pending.TurnID != p.turn.ID {
		return false
	}
	var result struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(p.outputs[p.calls[0].ID]), &result) != nil || result.Status != "awaiting_confirmation" {
		return false
	}
	p.turn.mu.Lock()
	text := strings.TrimSpace(strings.TrimPrefix(p.turn.Response, p.turn.Opening+p.turn.Readout))
	p.turn.mu.Unlock()
	return strings.ContainsAny(text, "？?") && p.turn.Timings().CompletedNaturally
}

// Keep the current pricing state next to the older readout instructions. A
// retained quote is not evidence that a newly staged comparison is calculating.
func (e *Engine) quoteRevisionRecoveryNote(turn *Turn) string {
	if !e.confirmsQuoteRevisions() || e.tools.quoteRevision == nil {
		return ""
	}
	raw, _ := json.Marshal(e.tools.quoteRevision)
	if turn == nil || turn.Transcript == "" || turn.ID == e.tools.quoteRevision.TurnID {
		return "\n【当前后续报价仍待确认】" + string(raw) + "\n本次登记已完成，现在只能复述已登记范围并询问还有其他调整吗。必须等待下一次来电人发言，当前禁止再次登记或调用 confirm，也不能说正在计算。尚未登记的其他要求仍须先处理。"
	}
	return "\n【当前后续报价仍待确认】" + string(raw) + `
上方讲解进度和金额属于之前已完成的报价。当前 pending_revision 只是登记，尚未提交计算，不得说正在算或等待结果，也不能拿旧方案金额回答新方案。根据最近连续的来电人发言判断完整意思：确认可能分成“就这三档”“其他不动”“你算吧”几句。范围已明确且来电人同意计算时，立即调用 quote_total，revision_action=confirm，并传当前 revision_id，等工具返回 running 后再说明正在计算。只说“我等着”不能凭空建立确认；应核对前文是否已明确同意。若尚未确认，简短核对完整范围；还有尚未登记的要求时先合并登记。普通澄清可以解释旧报价，但不能把新方案说成已算好。`
}
