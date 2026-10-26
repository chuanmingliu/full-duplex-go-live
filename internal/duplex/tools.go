package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	// callerTurns counts the caller's turns that were committed with words
	// (what the hangup rules' min_turns is judged on). netHeldUntil keeps
	// the goodbye net from acting on the farewell of a refused end_call.
	callerTurns  int
	netHeldUntil time.Time
	// ackPending holds a task's delivery until its ack has been said or
	// passed over (the ack comes first, the result after it).
	ackPending map[string]bool
	// deliveries are finished tasks waiting for a gap in the conversation.
	deliveries []*tools.Task
	// lastResult is the latest result read out to the caller, kept beside
	// the working memory so the model still has the figures once the
	// result note has left a trimmed history (10:48 on 10-26: the second
	// quotation's note was trimmed within four seconds of its delivery and
	// the next turn had no total to give).
	lastResult *tools.Task
	// lastResultTurn is the turn that read lastResult out; lastResultHeard
	// is what the caller heard of it when that turn was cut short (10:48 on
	// 10-26: the second quotation's readout was cut 4.7 s in and the total
	// was never said again).
	lastResultTurn  string
	lastResultCut   bool
	lastResultHeard string
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
		desc := c.Description
		if c.Brief == "quote-plans" {
			if brief, err := tools.FetchQuoteBrief(cfg.ToolsBaseURL, e.toolsToken(), 2*time.Second); err == nil {
				desc = strings.TrimSpace(desc) + "\n" + brief.Text("")
				e.log.Info("tools: the quote plan's fields are in the tool description", "tool", c.Name, "plan", brief.Plan, "fields", len(brief.Fields))
			} else {
				e.log.Warn("tools: the quote plan could not be read for the tool description", "tool", c.Name, "err", err)
			}
		}
		def := tools.Tool{Name: c.Name, Description: desc, Parameters: c.Parameters,
			URL: url, Async: c.Async, TimeoutMS: c.TimeoutMS, ETASeconds: c.ETASeconds,
			Ack: strings.TrimSpace(c.Ack), ResultOpening: strings.TrimSpace(c.ResultOpening), ResultOpeningStatus: c.ResultOpeningStatus}
		if c.Speech == tools.QuoteSpeechName {
			if qs, err := tools.FetchQuoteSpeech(cfg.ToolsBaseURL, e.toolsToken(), 2*time.Second); err == nil {
				def = def.WithSpeech(qs)
				e.log.Info("tools: what is said around the quotation comes from the panel", "tool", c.Name, "version", qs.Version,
					"prehook", qs.Prehook != "", "posthook", qs.Posthook != "", "template", qs.Template != "")
			} else {
				e.log.Warn("tools: the quote-speech config could not be read; using the profile's lines", "tool", c.Name, "err", err)
			}
		}
		ts.defs = append(ts.defs, def)
		if c.Prefetch != "" && cfg.ToolsBaseURL != "" {
			go e.prefetch(strings.TrimRight(cfg.ToolsBaseURL, "/") + c.Prefetch)
		}
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
	if t := ts.lastResult; t != nil {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "【最近一次报价结果】基于工作记忆 v%d：%s", t.Request.MemoryVersion, lastResultLine(t))
		if ts.lastResultCut {
			fmt.Fprintf(&b, "\n这段报价念到“%s”时被客户打断，客户很可能没听到总价；客户再问价格或方案时，先把总价和改动的险种复述一遍，不必再调用工具。", ts.lastResultHeard)
		} else {
			b.WriteString("\n已完整念给客户。")
		}
	}
	return b.String()
}

// lastResultLine is a delivered result in the words the caller heard — the
// template's readout when the tool has one, the raw output otherwise.
func lastResultLine(t *tools.Task) string {
	if s := t.Tool.ReadoutFor(t.Result.Output); s != "" {
		return s
	}
	return string(t.Result.Output)
}

// recordConfigChange applies the quote tool's `config` argument to the
// working memory before the task takes its snapshot, so a change to one
// item of the plan is a single call: quote_total with the change. On the
// 10:46 call of 10-26 the model recorded the change with update_memory and
// called the tool only after its own sentence had played: 3.3 s of the
// 8.2 s the caller waited for the figure.
func (e *Engine) recordConfigChange(c provider.ToolCall) {
	var args struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal([]byte(c.Arguments), &args); err != nil || len(args.Config) == 0 {
		return
	}
	// Nothing new leaves the version alone, so a repeated call still folds.
	snap, _ := e.tools.memory.Snapshot()
	var mem struct {
		Quote map[string]any `json:"quote"`
	}
	_ = json.Unmarshal(snap, &mem)
	same := true
	for k, v := range args.Config {
		if cur, ok := mem.Quote[k]; !ok || fmt.Sprint(cur) != fmt.Sprint(v) {
			same = false
			break
		}
	}
	if same {
		return
	}
	patch, _ := json.Marshal(map[string]any{"quote": args.Config})
	v, err := e.tools.memory.Patch(patch)
	if err != nil {
		return
	}
	e.log.Info("tools: the plan change in the call's config is recorded", "version", v, "keys", patchKeys(patch))
	e.deps.Emit(live.MemoryEvent{Envelope: live.Envelope{Type: live.ExtMemory}, Version: v, Patch: patch})
}

// recordSelection brings the working memory's plan to the selection a
// quotation was priced on (`selection` in the lead service's answer), so a
// later change builds on it. The default quotation is quoted with nothing
// in memory; on the 13:55 simulated call of 10-26 the first change was laid
// over that nothing and one coverage was priced. Coverages the selection
// no longer has are removed (nil in the patch).
func (e *Engine) recordSelection(output []byte) {
	ts := e.tools
	if ts == nil || ts.memory == nil {
		return
	}
	var r struct {
		Selection map[string]any `json:"selection"`
	}
	if json.Unmarshal(output, &r) != nil || len(r.Selection) == 0 {
		return
	}
	snap, _ := ts.memory.Snapshot()
	var mem struct {
		Quote map[string]any `json:"quote"`
	}
	_ = json.Unmarshal(snap, &mem)
	quote := map[string]any{}
	same := len(mem.Quote) == len(r.Selection)
	for k, v := range r.Selection {
		quote[k] = v
		if cur, ok := mem.Quote[k]; !ok || fmt.Sprint(cur) != fmt.Sprint(v) {
			same = false
		}
	}
	for k := range mem.Quote {
		if _, keep := r.Selection[k]; !keep {
			quote[k] = nil
			same = false
		}
	}
	if same {
		return
	}
	patch, _ := json.Marshal(map[string]any{"quote": quote})
	v, err := ts.memory.Patch(patch)
	if err != nil {
		return
	}
	e.log.Info("tools: the working memory's plan is the one priced", "version", v, "coverages", len(r.Selection))
	e.deps.Emit(live.MemoryEvent{Envelope: live.Envelope{Type: live.ExtMemory}, Version: v, Patch: patch})
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
			out, taskID := e.submitTask(job, c, t)
			p.outputs[c.ID] = out
			if taskID != "" && t.Ack != "" && p.scripted == "" {
				p.scriptedTask = taskID
				// The owner's words for the wait, not the model's: said from
				// the phrase cache once the model's own sentence has played,
				// and the model is not resumed until the result comes.
				p.scripted = t.Ack
				if ts.ackPending == nil {
					ts.ackPending = map[string]bool{}
				}
				ts.ackPending[taskID] = true
			}
		default:
			wait = append(wait, c)
		}
	}
	if p.scripted != "" {
		// The model was told to say nothing in the turn that calls the tool;
		// when it talks anyway ("好的，我帮您看一下基本方案", 4.2 s on the 20:35
		// call of 10-25, with the default quotation ready at once behind it),
		// the clause in flight is the last of it: the script — the ack, or
		// the result itself — follows at the clause's end, not the sentence's.
		if pipe := e.speech.Load(); pipe != nil && pipe.turn == job.turn {
			pipe.SoftStop()
			e.log.Info("tools: the model's words in the calling turn end at the clause in flight; the script follows", "turn", job.turn.ID, "task", p.scriptedTask)
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
			res := ts.exec.Execute(e.ctx, t, tools.Request{Tool: c.Name, CallID: c.ID, Session: e.opts.SessionID, Call: e.opts.CallID,
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
		Rule   string `json:"rule"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(c.Arguments), &args)
	if since := time.Since(e.tools.started); since < minCallForEndCall {
		e.log.Warn("tools: end_call refused this early in the call", "after_ms", since.Milliseconds(), "reason", args.Reason)
		return `{"status":"refused","note":"通话刚开始，不能挂断；请继续对话"}`
	}
	// A rule may want the caller to have been heard a few times first: "refused
	// twice" on one garbled line 17 s into the 09:43 call of 10-23 (ISS-054).
	if min := e.tools.rules.MinTurnsFor(args.Rule, args.Reason); min > 0 && e.tools.callerTurns < min {
		e.log.Warn("tools: end_call refused: too few turns for the rule", "rule", args.Rule, "reason", args.Reason,
			"turns", e.tools.callerTurns, "min_turns", min)
		// The farewell it said with this call must not end the call by the
		// goodbye net either (said_goodbye matches "不打扰您了").
		e.tools.netHeldUntil = time.Now().Add(netHoldAfterRefusal)
		return fmt.Sprintf(`{"status":"refused","note":"客户还只说了 %d 轮，按规则至少 %d 轮后才能以此理由挂断；请继续对话（如客户拒绝，先挽留一次）"}`, e.tools.callerTurns, min)
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

// netHoldAfterRefusal is how long the goodbye net stands down after an
// end_call was refused for too few turns: the model's farewell in that
// reply has already been said and must not end the call by the back door.
var netHoldAfterRefusal = 20 * time.Second

// prefetch asks the lead service to work the default quotation out now,
// while the phone is ringing (crm/prefetch.go); a call placed from a lead
// already has one and the ask is a no-op there.
func (e *Engine) prefetch(url string) {
	body, _ := json.Marshal(map[string]any{"call": e.opts.CallID, "session": e.opts.SessionID})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := e.toolsToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		e.log.Warn("tools: the default quotation could not be prefetched", "err", err)
		return
	}
	resp.Body.Close()
	e.log.Info("tools: default quotation prefetch asked for", "call", e.opts.CallID, "status", resp.StatusCode)
}

// toolsToken is the lead service's bearer token, from the profile's file.
func (e *Engine) toolsToken() string {
	if f := e.opts.Cfg.ToolsTokenFile; f != "" {
		if b, err := os.ReadFile(f); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

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
	if time.Now().Before(ts.netHeldUntil) {
		e.log.Info("hangup: the assistant's words would end the call, but its end_call was just refused; not acting", "chars", len([]rune(said)))
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
func (e *Engine) submitTask(job backendJob, c provider.ToolCall, t tools.Tool) (string, string) {
	ts := e.tools
	if ts.memory != nil {
		e.recordConfigChange(c)
	}
	snap, ver := e.memorySnapshot()
	if r, ok := ts.recent[t.Name]; ok && r.ver == ver && r.args == c.Arguments && time.Since(r.at) < repeatWindow {
		e.log.Info("tools: a repeated call was folded into the previous one", "tool", t.Name, "task", r.id, "memory_version", ver)
		return fmt.Sprintf(`{"status":"running","task_id":%q,"note":"同一请求已在处理中，结果出来后会自动通知你；不要重复调用"}`, r.id), ""
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
	req := tools.Request{Tool: c.Name, CallID: c.ID, Session: e.opts.SessionID, Call: e.opts.CallID, Arguments: json.RawMessage(c.Arguments), Memory: snap, MemoryVersion: ver}
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
	if t.Ack != "" {
		return fmt.Sprintf(`{"status":"running","task_id":%q,"eta_s":%d,"note":"任务已在后台计算，系统会替你对用户说“%s”，你这句话到此为止，不要再说任何等待的话；结果出来后会自动通知你"}`, task.ID, eta, t.Ack), task.ID
	}
	return fmt.Sprintf(`{"status":"running","task_id":%q,"eta_s":%d,"note":"任务已在后台计算，结果出来后会自动通知你；请先告诉用户稍等，并继续回答用户的其他问题"}`, task.ID, eta), task.ID
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
	if ts.ackPending[task.ID] {
		return // its ack first
	}
	waited := time.Since(task.Submitted.Add(task.Result.Took))
	wait := time.Duration(e.opts.Cfg.ToolsDeliverWaitMS) * time.Millisecond
	if wait <= 0 {
		wait = 1500 * time.Millisecond
	}
	busy := e.userOpen || e.awaitingFinal() || e.assistantHasFloor() || e.liveSpeculation() != nil
	if busy && waited < wait {
		return
	}
	if cur := e.tracker.Current(); busy && cur != nil && cur.Scripted && e.assistantHasFloor() {
		// The ack for this very task is still being said; the result waits
		// for it to finish rather than cutting "请您稍等" in half.
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
		readOut := false
		if opening := task.Tool.OpeningFor(task.Result.Output); opening != "" && task.Request.MemoryVersion == ver {
			if tools.ResultIsDefault(task.Result.Output) {
				opening = "" // the default quotation: ready before the call, no "算好了"
			}
			if readout := task.Tool.ReadoutFor(task.Result.Output); readout != "" {
				fmt.Fprintf(&note, "\n系统已替你完整念出报价：“%s%s”。你的回复里不得出现任何数字、金额或险种名，不要复述；只用一句话简短询问客户对这个方案的意见，或继续对话。", opening, readout)
				readOut = true
			} else if opening != "" {
				fmt.Fprintf(&note, "\n系统已替你说出开头“%s”，请直接接着说后面的内容，不要重复这个开头。", opening)
			}
		}
		if task.Request.MemoryVersion != ver {
			fmt.Fprintf(&note, "\n注意：该结果基于工作记忆 v%d 计算，而当前工作记忆已是 v%d（方案在计算期间有改动）。请先告知结果对应的是之前的方案，并询问是否按最新方案重新计算。", task.Request.MemoryVersion, ver)
		} else if !readOut {
			note.WriteString("\n请现在自然地把结果告诉用户，简洁，报总价和关键构成即可。")
		}
	}
	e.log.Info("tools: delivering a task's result", "tool", task.Tool.Name, "task", task.ID,
		"waited_for_gap_ms", waited.Milliseconds(), "interrupting", busy, "memory_changed", task.Request.MemoryVersion != ver)
	e.deps.Emit(live.TaskEvent{Envelope: live.Envelope{Type: live.ExtTask}, TaskID: task.ID, Tool: task.Tool.Name, Status: "delivered",
		MemoryVersion: task.Request.MemoryVersion, WaitedMS: waited.Milliseconds(), Interrupting: busy})
	e.history = append(e.history, provider.Message{Role: provider.RoleSystem, Content: note.String()})
	if task.Result.Err == nil && task.Request.MemoryVersion == ver {
		e.recordSelection(task.Result.Output)
	}
	turn := e.tracker.Begin("", false)
	e.specTurn = nil
	e.commitTurn(turn)
	if task.Result.Err == nil && task.Tool.OpeningFor(task.Result.Output) != "" {
		ts.lastResult, ts.lastResultTurn, ts.lastResultCut, ts.lastResultHeard = task, turn.ID, false, ""
	}
	if task.Result.Err == nil && task.Request.MemoryVersion == ver {
		if opening := task.Tool.OpeningFor(task.Result.Output); opening != "" {
			// The posthook from the cache, then the figures from the template
			// (synthesized: they change), before the model's first token. The
			// default quotation gets the figures alone.
			if tools.ResultIsDefault(task.Result.Output) {
				opening = ""
			}
			turn.Opening = opening + task.Tool.ReadoutFor(task.Result.Output)
		}
	}
	e.beginGeneration(turn, "task_result", false)
}

// noteReadoutCut remembers that the turn reading a result out was cut
// short, and what the caller heard of it, so the memory block can say the
// total may not have been heard.
func (e *Engine) noteReadoutCut(turnID, heard string) {
	ts := e.tools
	if ts == nil || ts.lastResult == nil || ts.lastResultTurn != turnID {
		return
	}
	ts.lastResultCut, ts.lastResultHeard = true, strings.TrimSpace(heard)
	e.log.Info("tools: the readout was cut short; the figures stay beside the memory", "task", ts.lastResult.ID, "heard_chars", len([]rune(heard)))
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
			out, taskID := e.submitTask(job, c, t)
			p.outputs[c.ID] = out
			if taskID != "" && t.Ack != "" && p.scripted == "" {
				p.scriptedTask = taskID
				p.scripted = t.Ack
				if e.tools.ackPending == nil {
					e.tools.ackPending = map[string]bool{}
				}
				e.tools.ackPending[taskID] = true
			}
		default:
			c := c
			go func() {
				res := e.tools.exec.Execute(e.ctx, t, tools.Request{Tool: c.Name, CallID: c.ID, Session: e.opts.SessionID, Call: e.opts.CallID,
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
