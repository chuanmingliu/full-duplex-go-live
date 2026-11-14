package duplex

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

const quoteConfirmationInstructions = `本通话开启后续报价确认。已有报价后，客户改保障、问新方案价格或要求比较档位，都先简短复述待报价范围并自然询问是否还有其他保障要调整，并在同一轮必须调用一次 quote_total 登记，不能只问不保存；这时只保存，不启动计算。修改选定方案用 config；假设或比较用 configs（全部档位也可用 compare_coverage），确认比较不会修改已选方案。收到 awaiting_confirmation 后，本轮已经问过就不要重复，否则补问。后续修改比较范围时传完整更新后的 configs；改为选定一个方案才用 config。只说具体险种和选项，不概括改动项数；结合上下文变换措辞，不机械重复上一句，不说已经在计算。只有客户在后续发言明确确认报价范围全部确定，才传 revision_action="confirm" 和当前 revision_id，不再传 config/configs。按上一问理解回答：“还有别的要改吗”的“有”表示还有改动，“没有了，就这样”表示结束调整。含糊回答先澄清，不从沉默或无关肯定推断同意。客户仅说“车损再改改”或“算一下这些”且具体选项尚不明确时，先按活动目录问清是否投保等具体选择，不能猜测保额或直接计算。取消用 revision_action="cancel" 和当前 revision_id。不得用 update_memory.quote 绕过确认。首次报价仍用原流程。确认后计算中的简短回应由你按上下文表达，避免重复固定等待话术。`

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
	var args struct {
		Action string `json:"revision_action"`
	}
	if json.Unmarshal([]byte(c.Arguments), &args) == nil && args.Action == "confirm" {
		t.Ack = ""
	}
	return t
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
	return text
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
		Config  map[string]any   `json:"config"`
		Configs []map[string]any `json:"configs"`
		Action  string           `json:"revision_action"`
		ID      string           `json:"revision_id"`
	}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return call, revisionReply("invalid", "报价参数无法解析，请纠正参数。", nil)
	}
	if args.Action != "" || args.ID != "" {
		return e.resolveQuoteRevision(turn, call, args.Action, args.ID, len(args.Config) > 0 || len(args.Configs) > 0)
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
		return call, e.stageQuoteRequest(turn, nil, args.Configs, memory)
	}
	if len(args.Config) == 0 {
		if pending != nil {
			return call, revisionReply("awaiting_confirmation", quoteConfirmationInstructions, pending)
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
		if pending == nil {
			return call, ""
		}
		e.tools.quoteRevision = nil
		return call, revisionReply("unchanged", "调整后的保障与当前已确认方案相同，使用现有有效报价；不要再说正在计算。", nil)
	}
	return call, e.stageQuoteRequest(turn, changes, nil, memory)
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
		return revisionReply("awaiting_confirmation", quoteConfirmationInstructions, pending)
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
	return revisionReply("awaiting_confirmation", quoteConfirmationInstructions, pending)
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
		e.tools.quoteRevision = nil
		e.log.Info("quote: revision cancelled", "revision", id, "turn", turn.ID)
		return call, revisionReply("cancelled", "已取消待确认改动，保留原已确认方案。按上下文简短回应。", nil)
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
