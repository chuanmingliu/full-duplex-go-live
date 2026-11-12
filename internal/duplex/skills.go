package duplex

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

type skillOutput struct {
	Status           string               `json:"status"`
	Scope            string               `json:"scope,omitempty"`
	MemoryVersion    int                  `json:"memory_version"`
	Source           string               `json:"source,omitempty"`
	Guidance         []skillsmcp.Guidance `json:"guidance,omitempty"`
	RenderingVersion int                  `json:"rendering_version,omitempty"`
}

func skillScope(turn *Turn) string {
	if turn == nil {
		return ""
	}
	if turn.skillScope != "" {
		return turn.skillScope
	}
	return turn.ID
}

// prepareSkills runs on the generation worker, before the first completion.
// Selection and content retrieval share one deadline. A fresh loop-owned
// snapshot prevents backend events arriving during I/O from leaving stale state.
func (e *Engine) prepareSkills(ctx context.Context, job backendJob, req provider.LLMRequest) provider.LLMRequest {
	if e.skills == nil {
		return req
	}
	began := time.Now()
	trace := skillsmcp.Trace{}
	trace.Begin("snapshot turn", "", began)
	memory, version := e.MemorySnapshot()
	text := latestUserText(req.Messages)
	if job.request != "" {
		text = job.request
	}
	out := skillOutput{Status: "no_match", Scope: skillScope(job.turn), MemoryVersion: version}
	docs, renderingVersion, err := e.skills.PrepareWithTrace(ctx, coverageSelectionText(req.Messages, text), memory, &trace)
	if err != nil {
		out.Status = "unavailable"
	} else if len(docs) > 0 {
		out.Status, out.Source, out.Guidance = "loaded", "configured-business-mcp", docs
	}
	type rendered struct {
		req           provider.LLMRequest
		started       time.Time
		outcome       string
		memoryVersion int
	}
	trace.Begin("wait for engine loop", "", time.Now())
	ready := make(chan rendered, 1)
	e.post(func() {
		started := time.Now()
		if ctx.Err() != nil {
			ready <- rendered{req, started, "cancelled", version}
			return
		}
		_, current := e.memorySnapshot()
		if current != version {
			out.Status, out.Guidance, out.MemoryVersion = "expired", nil, current
		}
		for _, guide := range out.Guidance {
			if _, guarded := guide.State["wechat.status"]; guarded {
				e.wechatTurn = job.turn
			}
		}
		fresh := req
		fresh.Messages = e.snapshotMessages(job.turn, false)
		if renderingVersion == 2 && len(out.Guidance) > 0 && fillSkillContentSlot(fresh.Messages, out.Guidance) {
			out.RenderingVersion = 2
			// The body now resides in the first system message. Keep only its
			// provenance here, without modifying the retrieved guidance slice.
			out.Guidance = append([]skillsmcp.Guidance(nil), out.Guidance...)
			for i := range out.Guidance {
				out.Guidance[i].Text = ""
			}
		}
		if out.Status != "no_match" {
			raw, _ := json.Marshal(out)
			fresh.Messages = append(fresh.Messages, provider.Message{Role: provider.RoleSystem, Name: skillsmcp.ToolName, Content: string(raw)})
		}
		if out.Status == "unavailable" || out.Status == "expired" {
			fresh.Messages = append(fresh.Messages, provider.Message{Role: provider.RoleSystem, Content: "当前业务指导未能加载。只使用原始提示已明确提供的业务步骤，不要自行编造页面入口、按钮或业务结果。信息不足时询问用户当前状态。"})
		}
		if len(out.Guidance) > 0 {
			fresh.Messages = append(fresh.Messages, provider.Message{Role: provider.RoleSystem, Content: "Apply this turn's configured business guidance to the latest user request only, subject to original instructions, current business state and tool permissions. Use existing tools directly when needed. Ask for clarification if the request is ambiguous."})
		}
		ready <- rendered{fresh, started, out.Status, current}
	})
	var result rendered
	select {
	case result = <-ready:
		trace.Begin("recheck memory and render", "", result.started)
	case <-ctx.Done():
		result = rendered{req: req, outcome: "cancelled", memoryVersion: version}
	}
	ended := time.Now()
	trace.End(result.outcome, ended)
	detail, _ := json.Marshal(trace)
	turnID := ""
	if job.turn != nil {
		turnID = job.turn.ID
	}
	e.log.Info("skills: prepare", "turn", turnID, "response", job.responseID, "status", result.outcome, "rendering_version", renderingVersion, "memory_version_start", version, "memory_version_end", result.memoryVersion, "ms", ended.Sub(began).Milliseconds(), "us", ended.Sub(began).Microseconds(), "trace", string(detail))
	return result.req
}

// businessStateNote is intentionally outside the optional MCP component so a
// service outage cannot revive a completed or declined workflow.
func businessStateNote(memory json.RawMessage) string {
	var state struct {
		Wechat *struct {
			Status string `json:"status"`
		} `json:"wechat"`
	}
	_ = json.Unmarshal(memory, &state)
	if state.Wechat == nil {
		return ""
	}
	switch state.Wechat.Status {
	case "added":
		return "当前权威业务状态：微信已经添加成功。添加流程已完成。若当前问题仍在询问添加步骤，请简短确认成功。其他问题直接回答当前业务内容，无须重复通知添加成功。停止添加、搜索服务通知或重新确认是否添加的指引。"
	case "declined":
		return "当前业务状态：用户已拒绝添加微信。停止添加指引和再次邀请，直接回答当前问题。概念问题只简短解释概念，不推测界面路径。只有用户明确重新同意添加时才更新同意状态。"
	case "", "unknown":
		return "微信添加尚未获得确认同意。若用户只问概念，仅简短解释概念，不推测界面路径，不主动询问或邀请添加微信。仅问概念时在定义后结束回答，不追加任何问题，包括是否需要帮忙或添加微信。只有用户明确要求或同意添加时，才调用 update_memory，以 patch={\"wechat\":{\"status\":\"consented\"}} 记录同意，再开始操作指引。用户拒绝时记录 status=declined。不能把用户说已添加当成后端 addV 确认。"
	}
	return ""
}

func latestUserText(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleUser {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

// loadSkill runs off the audio loop. The result is bound to the immutable
// memory snapshot and caller turn; continuation checks it again before use.
func (e *Engine) loadSkill(turn *Turn, args string, memory json.RawMessage, version int) string {
	out := skillOutput{Status: "unavailable", Scope: skillScope(turn), MemoryVersion: version}
	var input struct {
		Event string `json:"event"`
	}
	began := time.Now()
	if e.skills != nil && json.Unmarshal([]byte(args), &input) == nil && input.Event != "" {
		docs, err := e.skills.Load(e.ctx, input.Event, memory)
		if err == nil {
			out.Status = "no_match"
			if len(docs) > 0 {
				out.Status = "loaded"
				out.Guidance = docs
				out.Source = "configured-business-mcp"
			}
		}
	}
	e.log.Info("skills: load", "status", out.Status, "ms", time.Since(began).Milliseconds())
	raw, _ := json.Marshal(out)
	return string(raw)
}

// expiredSkillHistory preserves call/result pairing while removing instruction
// bodies belonging to a previous turn or a superseded memory snapshot.
func expiredSkillHistory(history []provider.Message, scope string, version int) []provider.Message {
	out := append([]provider.Message(nil), history...)
	for i, m := range out {
		if m.Role != provider.RoleTool || m.Name != skillsmcp.ToolName {
			continue
		}
		var result skillOutput
		if json.Unmarshal([]byte(m.Content), &result) != nil || result.Scope != scope || result.MemoryVersion != version {
			out[i].Content = `{"status":"expired","note":"Load current guidance again when needed; use the original prompt on failure."}`
		}
	}
	return out
}

// Bootstrap only a relevant MCP-enabled turn. Unrelated calls retain their
// original prompt; consent is still a semantic model decision, not a keyword
// side effect. Existing declined/added/consented state is never replaced here.
func wechatBootstrapNote(memory json.RawMessage, text string) string {
	var state map[string]json.RawMessage
	if json.Unmarshal(memory, &state) != nil {
		return ""
	}
	if raw, ok := state["wechat"]; ok && string(raw) != "null" {
		return ""
	}
	if !strings.Contains(text, "微信") && !strings.Contains(text, "加微") && !strings.Contains(strings.ToLower(text), "加v") && !strings.Contains(text, "服务通知") {
		return ""
	}
	return businessStateNote(json.RawMessage(`{"wechat":{"status":"unknown"}}`))
}
