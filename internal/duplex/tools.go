package duplex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

// Tools the engine carries out itself, and the call's working memory.
//
// A phone conversation about a policy goes round in circles — "三者改成三百万",
// "座位险不要了", "那一共多少钱" — and what has been settled must not have to
// be reconstructed from the transcript each time. The model keeps it in a
// working memory it edits through update_memory; the memory is rendered
// into its context on every request, after the instructions so the cached
// prefix is untouched.
//
// Pricing takes seconds, and seconds of silence end a phone call. A tool
// marked async is submitted to a pool and the model is told at once that it
// is running, with an id and an estimate; the model says so to the caller
// and the conversation goes on. When the result comes it is delivered as a
// turn of its own at the first gap — nobody speaking, no answer in flight —
// with what it was computed on and whether the memory has changed since, so
// the model can say "按您刚才的方案…" or "方案后来改了，要不要重新算".
// A result nobody wants any more (the task was superseded, the call ended)
// is dropped.

// toolState is the engine's side.
type toolState struct {
	defs   []tools.Tool
	byName map[string]tools.Tool
	exec   tools.Executor
	pool   *tools.Pool
	memory *tools.Memory
	// tasks are the async tool calls in flight, by task id; latest is the
	// newest per tool, which supersedes older ones of the same tool.
	tasks  map[string]*tools.Task
	latest map[string]string
	// recent is the last submission per tool: the same call again within
	// a few seconds on the same memory is folded into it, not resubmitted
	// (the model asked quote_total twice in half a second on 10-21 and the
	// total was read out twice).
	recent map[string]recentCall
	// started is when the tools came up (the call's start, near enough);
	// endCall is the model's request to hang up, once made.
	started time.Time
	endCall *endCallRequest
	// rules are the hangup rules in force (see tools.HangupRules); engine
	// is the subset the engine judges itself, checked on the tick.
	rules  tools.HangupRules
	engine []tools.HangupRule
	// lastCallerAt is when the caller last finished saying something, and
	// lastQuietAt when the assistant last finished speaking: the silence
	// rule's clock runs from the later of the two, so a long answer is not
	// counted as the caller's silence (16:38 on 10-21: two answers of 20 s
	// and the rule hung up as the second one ended).
	lastCallerAt time.Time
	lastQuietAt  time.Time
	// deliveries are finished tasks waiting for a gap in the conversation.
	deliveries []*tools.Task
	// stats
	calls, async, delivered, dropped, superseded int
}

// setupTools reads the profile.
func (e *Engine) setupTools() {
	cfg := e.opts.Cfg
	if len(cfg.Tools) == 0 && !cfg.ToolsMemory && !cfg.ToolsEndCall {
		return
	}
	ts := &toolState{byName: map[string]tools.Tool{}, tasks: map[string]*tools.Task{}, latest: map[string]string{}, started: time.Now()}
	if cfg.ToolsMemory {
		ts.memory = tools.NewMemory(nil)
		ts.defs = append(ts.defs, tools.UpdateMemoryTool)
	}
	if cfg.ToolsEndCall {
		ts.rules = e.loadHangupRules()
		ts.engine = ts.rules.EngineRules()
		ts.defs = append(ts.defs, ts.rules.EndCallTool())
	}
	for _, c := range cfg.Tools {
		if c.Name == "" || c.Name == tools.UpdateMemoryTool.Name {
			continue
		}
		url := c.URL
		if strings.HasPrefix(url, "/") && cfg.ToolsBaseURL != "" {
			url = strings.TrimRight(cfg.ToolsBaseURL, "/") + url
		}
		ts.defs = append(ts.defs, tools.Tool{Name: c.Name, Description: c.Description, Parameters: c.Parameters,
			URL: url, Async: c.Async, TimeoutMS: c.TimeoutMS, ETASeconds: c.ETASeconds})
	}
	for _, d := range ts.defs {
		ts.byName[d.Name] = d
	}
	token := ""
	if cfg.ToolsTokenFile != "" {
		if b, err := os.ReadFile(cfg.ToolsTokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		} else {
			e.log.Warn("tools: the token file could not be read; calling the tools without one", "file", cfg.ToolsTokenFile, "err", err)
		}
	}
	if e.toolExec != nil {
		ts.exec = e.toolExec
	} else {
		ts.exec = &tools.HTTPExecutor{Token: token}
	}
	workers := cfg.ToolsWorkers
	if workers <= 0 {
		workers = 4
	}
	ts.pool = tools.NewPool(ts.exec, workers)
	e.tools = ts
	names := make([]string, 0, len(ts.defs))
	for _, d := range ts.defs {
		if d.Async {
			names = append(names, d.Name+" (async)")
		} else {
			names = append(names, d.Name)
		}
	}
	e.log.Info("tools: on", "tools", strings.Join(names, ", "), "memory", ts.memory != nil, "workers", workers, "base_url", cfg.ToolsBaseURL)
}

// SeedMemory puts values into the call's working memory before anything is
// said: what the lead service knows about the lead.
func (e *Engine) SeedMemory(seed map[string]any) {
	e.post(func() {
		if e.tools == nil || e.tools.memory == nil || len(seed) == 0 {
			return
		}
		b, _ := json.Marshal(seed)
		if _, err := e.tools.memory.Patch(b); err == nil {
			e.log.Debug("tools: memory seeded", "keys", len(seed))
		}
	})
}

// MemorySnapshot is the working memory as it stands, for whoever keeps it.
func (e *Engine) MemorySnapshot() (json.RawMessage, int) {
	if e.tools == nil || e.tools.memory == nil {
		return nil, 0
	}
	return e.tools.memory.Snapshot()
}

// toolDefinitions are what the backend request carries.
func (e *Engine) toolDefinitions() []json.RawMessage {
	if e.tools == nil {
		return nil
	}
	out := make([]json.RawMessage, 0, len(e.tools.defs))
	for _, d := range e.tools.defs {
		out = append(out, d.Definition())
	}
	return out
}

// memoryBlock is the working memory and the tasks in flight, as the model
// sees them. It goes after the instructions, so the prefix cache is kept.
func (e *Engine) memoryBlock() string {
	ts := e.tools
	if ts == nil {
		return ""
	}
	var b strings.Builder
	if ts.memory != nil && !ts.memory.Empty() {
		snap, v := ts.memory.Snapshot()
		fmt.Fprintf(&b, "【工作记忆 v%d】以下是本次通话已确认的信息，以此为准：\n%s", v, string(snap))
	}
	if len(ts.tasks) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("【后台任务】")
		for _, t := range ts.tasks {
			fmt.Fprintf(&b, "%s（%s，基于工作记忆 v%d）计算中，已等待 %d 秒；", t.ID, t.Tool.Name, t.Request.MemoryVersion, int(time.Since(t.Submitted).Seconds()))
		}
		b.WriteString("结果出来后会自动告诉你，到时再向用户报告；用户若问起，如实说还在计算。")
	}
	return b.String()
}

// isServerTool says whether a call is one the engine carries out.
func (e *Engine) isServerTool(name string) bool {
	if e.tools == nil {
		return false
	}
	_, ok := e.tools.byName[name]
	return ok
}

// onServerCalls carries out the calls the model made that are the engine's,
// and resumes the backend with their outputs. Called on the loop goroutine
// once the turn is certain.
func (e *Engine) onServerCalls(job backendJob, calls []provider.ToolCall) {
	ts := e.tools
	e.ensureUserRecorded(job.turn)
	p := &pendingResponse{delegationID: job.delegationID, turn: job.turn, calls: calls, outputs: map[string]string{}, createdAt: time.Now()}
	var wait []provider.ToolCall
	for _, c := range calls {
		ts.calls++
		t := ts.byName[c.Name]
		switch {
		case t.Builtin == "update_memory":
			p.outputs[c.ID] = e.applyMemoryCall(c)
		case t.Builtin == "end_call":
			p.outputs[c.ID] = e.requestEndCall(c)
		case t.Async:
			p.outputs[c.ID] = e.submitTask(job, c, t)
		default:
			wait = append(wait, c)
		}
	}
	if len(wait) == 0 {
		if e.hangingUp() {
			// The model has said goodbye and asked to hang up. Resuming it
			// with the tool's output would only draw a second goodbye, cut
			// off by the hangup; the conversation is over.
			e.log.Debug("tools: end_call accepted; not resuming the backend", "turn", job.turn)
			return
		}
		e.continueWith(p)
		return
	}
	// Quick tools run now, off the loop, and the backend resumes when all
	// are in. The caller is waiting through this, so the timeout is short.
	snap, ver := e.memorySnapshot()
	go func() {
		for _, c := range wait {
			t := ts.byName[c.Name]
			began := time.Now()
			res := ts.exec.Execute(e.ctx, t, tools.Request{Tool: c.Name, CallID: c.ID, Session: e.opts.SessionID,
				Arguments: json.RawMessage(c.Arguments), Memory: snap, MemoryVersion: ver})
			out := toolOutput(res)
			e.log.Info("tools: ran", "tool", c.Name, "ms", time.Since(began).Milliseconds(), "err", errString(res.Err))
			e.post(func() { p.outputs[c.ID] = out })
		}
		e.post(func() {
			if p.complete() {
				e.continueWith(p)
			}
		})
	}()
}

func (e *Engine) memorySnapshot() (json.RawMessage, int) {
	if e.tools == nil || e.tools.memory == nil {
		return nil, 0
	}
	return e.tools.memory.Snapshot()
}

// continueWith resumes the backend with a response's function outputs, as a
// client's response.create would.
func (e *Engine) continueWith(p *pendingResponse) {
	if !e.tracker.IsCurrent(p.turn) && p.turn.Dead() {
		return
	}
	e.continuations = append(e.continuations, p)
	e.drainDeferred()
}

// requestEndCall is the model saying the conversation is over. The call is
// ended once whatever is being said has played, and not before: the
// farewell usually arrives in the same response as the tool call. A call
// that has barely begun is not ended on the model's say-so.
func (e *Engine) requestEndCall(c provider.ToolCall) string {
	var args struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(c.Arguments), &args)
	if since := time.Since(e.tools.started); since < minCallForEndCall {
		e.log.Warn("tools: end_call refused this early in the call", "after_ms", since.Milliseconds(), "reason", args.Reason)
		return `{"status":"refused","note":"通话刚开始，不能挂断；请继续对话"}`
	}
	if e.tools.endCall == nil {
		e.tools.endCall = &endCallRequest{reason: args.Reason, at: time.Now(), byModel: true}
		e.log.Info("tools: end_call: the call will end when the answer has played", "reason", args.Reason)
		go e.endCallWhenQuiet(args.Reason)
	}
	return `{"status":"ok","note":"这句话播完后电话会挂断"}`
}

// minCallForEndCall is how old a call must be before the model may end it.
var minCallForEndCall = 10 * time.Second

// loadHangupRules reads the rules from the lead service when the profile
// says to, and falls back to the built-in set: a call must not start
// without a way to end.
func (e *Engine) loadHangupRules() tools.HangupRules {
	cfg := e.opts.Cfg
	if cfg.HangupRules && cfg.ToolsBaseURL != "" {
		token := ""
		if cfg.ToolsTokenFile != "" {
			if b, err := os.ReadFile(cfg.ToolsTokenFile); err == nil {
				token = strings.TrimSpace(string(b))
			}
		}
		hr, errs := tools.FetchHangupRules(cfg.ToolsBaseURL, token, 2*time.Second)
		if len(hr.Rules) > 0 {
			e.log.Info("hangup: rules from the lead service", "version", hr.Version, "model", len(hr.ModelRules()), "engine", len(hr.EngineRules()), "dropped", len(errs))
			for _, err := range errs {
				e.log.Warn("hangup: a rule was dropped", "err", err)
			}
			return hr
		}
		e.log.Warn("hangup: the lead service's rules could not be read; using the built-in set", "errs", errs)
	}
	hr, _ := tools.ParseHangupRules([]byte(tools.DefaultHangupRules))
	return hr
}

// checkAssistantHangupRules runs when the assistant has finished saying a
// reply: an assistant rule that matches it ends the call now — the reply
// was the farewell. Called on the loop goroutine.
func (e *Engine) checkAssistantHangupRules(said string) {
	ts := e.tools
	if ts == nil || ts.endCall != nil || said == "" || time.Since(ts.started) < minCallForEndCall {
		return
	}
	for _, r := range ts.engine {
		if r.Kind != "assistant" || !r.Matches(said) {
			continue
		}
		e.log.Info("hangup: the assistant said goodbye; ending the call", "rule", r.ID, "chars", len([]rune(said)))
		ts.endCall = &endCallRequest{reason: r.ID, at: time.Now()}
		go e.endCallWhenQuiet(r.ID)
		return
	}
}

// noteAssistantQuiet marks the assistant finishing (or being cut off); the
// silence rule's clock restarts here.
func (e *Engine) noteAssistantQuiet() {
	if e.tools != nil {
		e.tools.lastQuietAt = time.Now()
	}
}

// noteCallerSpoke is the silence rule's clock: the caller's last word.
func (e *Engine) noteCallerSpoke() {
	if e.tools != nil {
		e.tools.lastCallerAt = time.Now()
	}
}

// checkHangupRules judges the engine's rules once a tick: silence, duration,
// and — on the caller's final words — keyword. A rule that applies is
// acted on once: the farewell is said as an opening line would be, and
// the call ends when it has played, exactly as the model's end_call does.
func (e *Engine) checkHangupRules(callerWords string) {
	ts := e.tools
	if ts == nil || ts.endCall != nil || len(ts.engine) == 0 {
		return
	}
	age := time.Since(ts.started)
	if age < minCallForEndCall {
		return
	}
	for _, r := range ts.engine {
		switch r.Kind {
		case "keyword":
			if callerWords == "" || !r.Matches(callerWords) {
				continue
			}
		case "duration":
			if age < time.Duration(r.Seconds)*time.Second {
				continue
			}
			// At a quiet moment, not over the caller.
			if e.userOpen || e.assistantHasFloor() {
				continue
			}
		case "silence":
			since := ts.lastCallerAt
			if since.IsZero() {
				since = ts.started
			}
			if ts.lastQuietAt.After(since) {
				since = ts.lastQuietAt
			}
			if time.Since(since) < time.Duration(r.Seconds)*time.Second || e.userOpen || e.assistantHasFloor() {
				continue
			}
		default:
			continue
		}
		e.log.Info("hangup: a rule applies", "rule", r.ID, "kind", r.Kind, "name", r.Name)
		ts.endCall = &endCallRequest{reason: r.ID, at: time.Now()}
		if r.Kind == "keyword" {
			e.abandonSpeculation("the hangup rule " + r.ID)
		}
		if r.Farewell != "" {
			e.Say(r.Farewell)
		}
		go e.endCallWhenQuiet(r.ID)
		return
	}
}

// hangingUp says whether the model's request to end the call stands.
func (e *Engine) hangingUp() bool {
	return e.tools != nil && e.tools.endCall != nil
}

type endCallRequest struct {
	reason string
	at     time.Time
	// byModel: the model asked (end_call); otherwise one of the engine's
	// own rules did.
	byModel bool
}

// endCallWhenQuiet waits for the player to finish what it has (the farewell
// in the same response, typically), gives the last of it time to reach the
// line, and asks the session to hang up. A bounded wait: a farewell that
// never starts does not keep the call up either.
func (e *Engine) endCallWhenQuiet(reason string) {
	deadline := time.Now().Add(20 * time.Second)
	// Give the response that carried the tool call a moment to begin.
	time.Sleep(600 * time.Millisecond)
	quiet := func() bool { return !e.player.Speaking() && e.player.PendingMS() == 0 }
	for time.Now().Before(deadline) {
		if e.ctx.Err() != nil {
			return
		}
		if !quiet() {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// Quiet now; let the last of it reach the line, and hang up only if
		// nothing began to play in the meantime (the first simulated call
		// with the tool saw a second farewell start inside the grace and be
		// cut by the hangup).
		time.Sleep(hangupGrace)
		if quiet() {
			break
		}
	}
	if e.ctx.Err() != nil {
		return
	}
	e.log.Info("tools: end_call: hanging up", "reason", reason)
	e.deps.Emit(live.HangupEvent{Envelope: live.Envelope{Type: "session.hangup"}, Reason: reason})
}

// hangupGrace is how long the last audio is given to reach the far end.
var hangupGrace = 700 * time.Millisecond

func (e *Engine) applyMemoryCall(c provider.ToolCall) string {
	var args struct {
		Patch json.RawMessage `json:"patch"`
	}
	if err := json.Unmarshal([]byte(c.Arguments), &args); err != nil || len(args.Patch) == 0 {
		// The model sometimes sends the patch itself as the arguments.
		args.Patch = json.RawMessage(c.Arguments)
	}
	v, err := e.tools.memory.Patch(args.Patch)
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error())
	}
	e.log.Info("tools: memory updated", "version", v, "chars", len(args.Patch), "keys", patchKeys(args.Patch))
	e.deps.Emit(live.MemoryEvent{Envelope: live.Envelope{Type: live.ExtMemory}, Version: v, Patch: args.Patch})
	return fmt.Sprintf(`{"ok":true,"version":%d}`, v)
}

// submitTask starts an async tool and answers the model at once.
func (e *Engine) submitTask(job backendJob, c provider.ToolCall, t tools.Tool) string {
	ts := e.tools
	snap, ver := e.memorySnapshot()
	if r, ok := ts.recent[t.Name]; ok && r.ver == ver && r.args == c.Arguments && time.Since(r.at) < repeatWindow {
		e.log.Info("tools: a repeated call was folded into the previous one", "tool", t.Name, "task", r.id, "memory_version", ver)
		return fmt.Sprintf(`{"status":"running","task_id":%q,"note":"同一请求已在处理中，结果出来后会自动通知你；不要重复调用"}`, r.id)
	}
	// A newer request for the same tool supersedes the older: its result
	// would be read out to a caller who has moved on.
	if oldID := ts.latest[t.Name]; oldID != "" {
		if old := ts.tasks[oldID]; old != nil {
			old.Cancel()
			delete(ts.tasks, oldID)
			ts.superseded++
			e.log.Info("tools: a task was superseded by a newer one", "tool", t.Name, "task", oldID)
		}
	}
	req := tools.Request{Tool: c.Name, CallID: c.ID, Session: e.opts.SessionID, Arguments: json.RawMessage(c.Arguments), Memory: snap, MemoryVersion: ver}
	task := ts.pool.Submit(e.ctx, t, req, func(task *tools.Task) {
		e.post(func() { e.onTaskDone(task) })
	})
	ts.tasks[task.ID] = task
	ts.latest[t.Name] = task.ID
	if ts.recent == nil {
		ts.recent = map[string]recentCall{}
	}
	ts.recent[t.Name] = recentCall{id: task.ID, ver: ver, args: c.Arguments, at: time.Now()}
	ts.async++
	eta := t.ETASeconds
	if eta <= 0 {
		eta = 10
	}
	e.log.Info("tools: task submitted", "tool", c.Name, "task", task.ID, "memory_version", ver, "turn", job.turn.ID)
	e.deps.Emit(live.TaskEvent{Envelope: live.Envelope{Type: live.ExtTask}, TaskID: task.ID, Tool: c.Name, Status: "running", MemoryVersion: ver})
	return fmt.Sprintf(`{"status":"running","task_id":%q,"eta_s":%d,"note":"任务已在后台计算，结果出来后会自动通知你；请先告诉用户稍等，并继续回答用户的其他问题"}`, task.ID, eta)
}

type recentCall struct {
	id   string
	ver  int
	args string
	at   time.Time
}

// repeatWindow is how long a repeated call of the same tool on the same
// memory is folded into the previous one.
var repeatWindow = 8 * time.Second

// onTaskDone queues a finished task for delivery.
func (e *Engine) onTaskDone(task *tools.Task) {
	ts := e.tools
	if ts == nil || ts.tasks[task.ID] == nil {
		return // superseded or the session is over
	}
	delete(ts.tasks, task.ID)
	if ts.latest[task.Tool.Name] == task.ID {
		delete(ts.latest, task.Tool.Name)
	}
	e.log.Info("tools: task done", "tool", task.Tool.Name, "task", task.ID, "took_ms", task.Result.Took.Milliseconds(), "err", errString(task.Result.Err))
	// What came back, in numbers and names only, so the call journal can
	// show a quotation's steps: a need (which fields), a quote (total,
	// lines, skipped) or an error. Never the person's answers.
	e.log.Info("tools: task result", append([]any{"tool", task.Tool.Name, "task", task.ID}, resultShape(task.Result)...)...)
	ts.deliveries = append(ts.deliveries, task)
	e.deps.Emit(live.TaskEvent{Envelope: live.Envelope{Type: live.ExtTask}, TaskID: task.ID, Tool: task.Tool.Name, Status: "done",
		MemoryVersion: task.Request.MemoryVersion, TookMS: task.Result.Took.Milliseconds(), Error: errString(task.Result.Err)})
}

// maybeDeliver, on each tick, announces a finished task at the first gap
// in the conversation, or after the wait over whatever is going on.
func (e *Engine) maybeDeliver() {
	ts := e.tools
	if ts == nil || len(ts.deliveries) == 0 || e.muted {
		return
	}
	task := ts.deliveries[0]
	waited := time.Since(task.Submitted.Add(task.Result.Took))
	wait := time.Duration(e.opts.Cfg.ToolsDeliverWaitMS) * time.Millisecond
	if wait <= 0 {
		wait = 1500 * time.Millisecond
	}
	busy := e.userOpen || e.awaitingFinal() || e.assistantHasFloor() || e.liveSpeculation() != nil
	if busy && waited < wait {
		return
	}
	ts.deliveries = ts.deliveries[1:]
	ts.delivered++
	if busy {
		// The gap never came: the result interrupts, as a person would.
		e.yieldFloor(false)
	}
	_, ver := e.memorySnapshot()
	var note strings.Builder
	fmt.Fprintf(&note, "【后台任务结果】%s（%s）已完成", task.ID, task.Tool.Name)
	if task.Result.Err != nil {
		fmt.Fprintf(&note, "，但失败了：%s。请告诉用户暂时算不出来，并说明会稍后再试或请人工跟进。", task.Result.Err.Error())
	} else {
		fmt.Fprintf(&note, "，结果：%s", string(task.Result.Output))
		if task.Request.MemoryVersion != ver {
			fmt.Fprintf(&note, "\n注意：该结果基于工作记忆 v%d 计算，而当前工作记忆已是 v%d（方案在计算期间有改动）。请先告知结果对应的是之前的方案，并询问是否按最新方案重新计算。", task.Request.MemoryVersion, ver)
		} else {
			note.WriteString("\n请现在自然地把结果告诉用户，简洁，报总价和关键构成即可。")
		}
	}
	e.log.Info("tools: delivering a task's result", "tool", task.Tool.Name, "task", task.ID,
		"waited_for_gap_ms", waited.Milliseconds(), "interrupting", busy, "memory_changed", task.Request.MemoryVersion != ver)
	e.deps.Emit(live.TaskEvent{Envelope: live.Envelope{Type: live.ExtTask}, TaskID: task.ID, Tool: task.Tool.Name, Status: "delivered",
		MemoryVersion: task.Request.MemoryVersion, WaitedMS: waited.Milliseconds(), Interrupting: busy})
	e.history = append(e.history, provider.Message{Role: provider.RoleSystem, Content: note.String()})
	turn := e.tracker.Begin("", false)
	e.specTurn = nil
	e.commitTurn(turn)
	e.beginGeneration(turn, "task_result", false)
}

// closeTools cancels what is in flight and says what happened.
func (e *Engine) closeTools() {
	ts := e.tools
	if ts == nil {
		return
	}
	for _, t := range ts.tasks {
		t.Cancel()
		ts.dropped++
	}
	ts.dropped += len(ts.deliveries)
	if ts.calls > 0 || ts.memory != nil && !ts.memory.Empty() {
		_, ver := e.memorySnapshot()
		e.log.Info("tools: this call", "calls", ts.calls, "async", ts.async, "delivered", ts.delivered,
			"superseded", ts.superseded, "dropped_at_close", ts.dropped, "memory_version", ver)
	}
}

// patchKeys names the top-level keys of a memory patch: what the model
// recorded, never what.
func patchKeys(patch json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(patch, &m) != nil {
		return "?"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// resultShape summarises a tool's result for the log: the quotation's
// status=need with its missing fields, status=quote with its total, lines
// and skipped coverages, or status=error.
func resultShape(res tools.Result) []any {
	if res.Err != nil {
		return []any{"status", "error", "error", res.Err.Error()}
	}
	var out struct {
		Status  string `json:"status"`
		Plan    string `json:"plan"`
		Missing []struct {
			Field string `json:"field"`
		} `json:"missing"`
		Total   float64  `json:"total"`
		Lines   []any    `json:"lines"`
		Skipped []string `json:"skipped"`
		Source  string   `json:"source"`
	}
	if json.Unmarshal(res.Output, &out) != nil {
		return []any{"status", "unreadable", "chars", len(res.Output)}
	}
	if out.Status == "need" {
		fields := make([]string, 0, len(out.Missing))
		for _, m := range out.Missing {
			fields = append(fields, m.Field)
		}
		return []any{"status", "need", "plan", out.Plan, "missing", strings.Join(fields, ",")}
	}
	if out.Total > 0 || len(out.Lines) > 0 {
		return []any{"status", "quote", "total", out.Total, "lines", len(out.Lines), "skipped", strings.Join(out.Skipped, ","), "source", out.Source}
	}
	return []any{"status", "ok", "chars", len(res.Output)}
}

func toolOutput(res tools.Result) string {
	if res.Err != nil {
		return fmt.Sprintf(`{"ok":false,"error":%q}`, res.Err.Error())
	}
	return string(res.Output)
}

// splitCalls separates the calls the engine carries out from the
// application's.
func (e *Engine) splitCalls(calls []provider.ToolCall) (mine, theirs []provider.ToolCall) {
	for _, c := range calls {
		if e.isServerTool(c.Name) {
			mine = append(mine, c)
		} else {
			theirs = append(theirs, c)
		}
	}
	return mine, theirs
}

// onServerCallsAmong handles a response that called both kinds: the
// application's calls are registered as usual and the engine's outputs are
// put in beside them; the application's response.create resumes the backend
// once everything is in.
func (e *Engine) onServerCallsAmong(job backendJob, mine, theirs []provider.ToolCall) {
	e.onToolCalls(job, append(append([]provider.ToolCall(nil), theirs...), mine...))
	p := e.pending[job.delegationID]
	if p == nil {
		return
	}
	snap, ver := e.memorySnapshot()
	for _, c := range mine {
		e.tools.calls++
		t := e.tools.byName[c.Name]
		switch {
		case t.Builtin == "update_memory":
			p.outputs[c.ID] = e.applyMemoryCall(c)
		case t.Builtin == "end_call":
			p.outputs[c.ID] = e.requestEndCall(c)
		case t.Async:
			p.outputs[c.ID] = e.submitTask(job, c, t)
		default:
			c := c
			go func() {
				res := e.tools.exec.Execute(e.ctx, t, tools.Request{Tool: c.Name, CallID: c.ID, Session: e.opts.SessionID,
					Arguments: json.RawMessage(c.Arguments), Memory: snap, MemoryVersion: ver})
				e.post(func() {
					if p := e.pending[job.delegationID]; p != nil {
						p.outputs[c.ID] = toolOutput(res)
					}
				})
			}()
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ = context.Canceled
