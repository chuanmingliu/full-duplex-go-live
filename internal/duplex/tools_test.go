package duplex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/tools"
)

// quoteLLM is a chat model with a script: a change to the plan is noted in
// the working memory, a request for the price calls the quote tool, a
// "running" answer is relayed as "稍等", and a system note with a result is
// read out.
type quoteLLM struct {
	mu       sync.Mutex
	calls    int
	requests []provider.LLMRequest
}

func (l *quoteLLM) Name() string { return "quote-script" }

func (l *quoteLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	l.mu.Lock()
	l.requests = append(l.requests, req)
	l.calls++
	id := fmt.Sprintf("call_%d", l.calls)
	l.mu.Unlock()
	out := make(chan provider.LLMDelta, 8)
	last := req.Messages[len(req.Messages)-1]
	var text string
	var call *provider.ToolCall
	switch {
	case last.Role == provider.RoleTool:
		if strings.Contains(last.Content, `"running"`) {
			text = "好的，我这边马上帮您算，请稍等，有其他问题可以先问我。"
		} else {
			text = "好的，记下了。"
		}
	case last.Role == provider.RoleSystem && strings.Contains(last.Content, "后台任务结果"):
		if strings.Contains(last.Content, "方案在计算期间有改动") {
			text = "报价出来了，不过是按您之前的方案算的，要不要按新方案再算一遍？"
		} else {
			text = "报价出来了，一共三千二百元。"
		}
	case strings.Contains(last.Content, "多少钱"):
		call = &provider.ToolCall{ID: id, Name: "quote_total", Arguments: `{}`}
	case strings.Contains(last.Content, "改成"):
		call = &provider.ToolCall{ID: id, Name: "update_memory", Arguments: `{"patch":{"quote":{"三者险":"300万"}}}`}
	default:
		text = mock.Reply(last.Content)
	}
	go func() {
		defer close(out)
		time.Sleep(150 * time.Millisecond)
		for _, r := range text {
			select {
			case out <- provider.LLMDelta{Text: string(r)}:
			case <-ctx.Done():
				return
			}
		}
		if call != nil {
			out <- provider.LLMDelta{ToolCall: call}
		}
	}()
	return out, nil
}

// slowQuote is the tool's service: it takes a while and reports what memory
// version it priced.
type slowQuote struct {
	delay time.Duration
	runs  atomic.Int32
}

func (s *slowQuote) Execute(ctx context.Context, t tools.Tool, req tools.Request) tools.Result {
	s.runs.Add(1)
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return tools.Result{Err: ctx.Err()}
	}
	out, _ := json.Marshal(map[string]any{"total": 3200, "memory_version": req.MemoryVersion})
	return tools.Result{Output: out, Took: s.delay}
}

func (c *collector) taskEvents() []live.TaskEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []live.TaskEvent
	for _, ev := range c.events {
		if t, ok := ev.(live.TaskEvent); ok {
			out = append(out, t)
		}
	}
	return out
}

func (c *collector) saidAll() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, ev := range c.events {
		if d, ok := ev.(live.TranscriptDelta); ok {
			b.WriteString(d.Delta)
		}
	}
	return b.String()
}

func quoteConfig() Options {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.ToolsMemory = true
	cfg.opts.Cfg.ToolsDeliverWaitMS = 1500
	cfg.opts.Cfg.Tools = append(cfg.opts.Cfg.Tools, config.ToolConfig{Name: "quote_total", Description: "计算总保费", URL: "http://tool", Async: true, ETASeconds: 3})
	return cfg.opts
}

func startQuoteEngine(t *testing.T, opts Options, deps Deps, exec tools.Executor) *Engine {
	t.Helper()
	if deps.TTS == nil {
		deps.TTS = mock.NewTTS()
	}
	opts.ClientRate = opts.Cfg.ClientRate
	opts.SessionID = "test"
	e := New(opts, deps)
	e.toolExec = exec
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(func() { e.Close(); cancel() })
	return e
}

// The point of it: the price is asked for, the tool runs in the background,
// the assistant says so and answers another question meanwhile, and the
// result is read out when it comes.
func TestASlowToolDoesNotHoldTheConversation(t *testing.T) {
	opts := quoteConfig()
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱今天天气怎么样")
	asr.msPerRune = 150
	llm := &quoteLLM{}
	quote := &slowQuote{delay: 2500 * time.Millisecond}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, quote)

	speak(e, rate, 750) // "一共多少钱"
	pause(e, rate, 700)
	waitFor(t, "the tool to be submitted", 5*time.Second, func() bool {
		for _, ev := range c.taskEvents() {
			if ev.Status == "running" {
				return true
			}
		}
		return false
	})
	waitFor(t, "the assistant to say it is calculating", 5*time.Second, func() bool { return strings.Contains(c.saidAll(), "稍等") })
	if e.tools.pool.Running() != 1 {
		t.Fatalf("running tasks %d", e.tools.pool.Running())
	}
	// Meanwhile another question, answered as usual.
	listenThrough(t, e, rate)
	speak(e, rate, 1050) // "今天天气怎么样"
	pause(e, rate, 700)
	waitFor(t, "the other question to be answered", 5*time.Second, func() bool {
		return strings.Contains(c.saidAll(), mock.Reply("今天天气怎么样。")[:4])
	})
	// The result arrives and is read out once the floor is free.
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(c.saidAll(), "报价出来了") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	if !strings.Contains(c.saidAll(), "报价出来了，一共三千二百元") {
		t.Fatalf("the result was not read out; said: %q", c.saidAll())
	}
	var statuses []string
	for _, ev := range c.taskEvents() {
		statuses = append(statuses, ev.Status)
	}
	if strings.Join(statuses, ",") != "running,done,delivered" {
		t.Fatalf("task events %v", statuses)
	}
	if quote.runs.Load() != 1 {
		t.Fatalf("the tool ran %d times", quote.runs.Load())
	}
}

// A change to the plan while the price is being worked out: the memory is
// edited through the tool, and the result, when it comes, is reported as
// belonging to the earlier plan.
func TestAResultForAnOlderPlanSaysSo(t *testing.T) {
	opts := quoteConfig()
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱三者改成三百万")
	asr.msPerRune = 150
	llm := &quoteLLM{}
	quote := &slowQuote{delay: 7 * time.Second}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, quote)

	speak(e, rate, 750)
	pause(e, rate, 700)
	waitFor(t, "the assistant to say it is calculating", 5*time.Second, func() bool { return strings.Contains(c.saidAll(), "稍等") })
	listenThrough(t, e, rate)
	speak(e, rate, 1050) // "三者改成三百万"
	pause(e, rate, 700)
	waitFor(t, "the memory to be updated", 5*time.Second, func() bool { return e.tools.memory.Version() == 1 })
	if !strings.Contains(c.saidAll(), "记下了") {
		waitFor(t, "the change to be acknowledged", 5*time.Second, func() bool { return strings.Contains(c.saidAll(), "记下了") })
	}
	deadline := time.Now().Add(14 * time.Second)
	for !strings.Contains(c.saidAll(), "报价出来了") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	listenThrough(t, e, rate) // the whole sentence
	if !strings.Contains(c.saidAll(), "按您之前的方案") {
		t.Fatalf("the result did not say the plan had changed; said: %q", c.saidAll())
	}
	// The memory is in the model's context, after the instructions.
	llm.mu.Lock()
	defer llm.mu.Unlock()
	last := llm.requests[len(llm.requests)-1]
	sys := last.Messages[0].Content
	if !strings.Contains(sys, "【工作记忆 v1】") || !strings.Contains(sys, "300万") {
		t.Fatalf("the system prompt does not carry the memory: %q", sys)
	}
	if !strings.HasPrefix(sys, strings.TrimSpace(opts.Instructions)) && opts.Instructions != "" {
		t.Fatalf("the memory displaced the instructions from the front of the prompt")
	}
}

// Asking again while a quote is running supersedes the first: one result is
// read out, for the latest request.
func TestANewerRequestSupersedesTheOlderTask(t *testing.T) {
	// The same request within repeatWindow is folded, not superseded; this
	// test is about a later one, so the window is off.
	oldWindow := repeatWindow
	repeatWindow = 0
	defer func() { repeatWindow = oldWindow }()
	opts := quoteConfig()
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱一共多少钱")
	asr.msPerRune = 150
	llm := &quoteLLM{}
	quote := &slowQuote{delay: 7 * time.Second}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, quote)
	speak(e, rate, 750)
	pause(e, rate, 700)
	waitFor(t, "the first submission", 5*time.Second, func() bool { return len(c.taskEvents()) >= 1 })
	listenThrough(t, e, rate)
	speak(e, rate, 750)
	pause(e, rate, 700)
	waitFor(t, "the second submission", 5*time.Second, func() bool {
		n := 0
		for _, ev := range c.taskEvents() {
			if ev.Status == "running" {
				n++
			}
		}
		return n == 2
	})
	deadline := time.Now().Add(14 * time.Second)
	for strings.Count(c.saidAll(), "报价出来了") < 1 && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	pause(e, rate, 1500)
	if n := strings.Count(c.saidAll(), "报价出来了"); n != 1 {
		t.Fatalf("results read out: %d; said %q", n, c.saidAll())
	}
	if e.tools.superseded != 1 {
		t.Fatalf("superseded %d", e.tools.superseded)
	}
}

// farewellLLM says goodbye and calls end_call in the same response when the
// caller says goodbye; otherwise it answers as the mock does.
type farewellLLM struct {
	mu      sync.Mutex
	calls   int
	resumed int // requests that followed a tool result
}

func (l *farewellLLM) Name() string { return "farewell-script" }

func (l *farewellLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	l.mu.Lock()
	l.calls++
	id := fmt.Sprintf("call_%d", l.calls)
	l.mu.Unlock()
	out := make(chan provider.LLMDelta, 8)
	last := req.Messages[len(req.Messages)-1]
	text, call := mock.Reply(last.Content), (*provider.ToolCall)(nil)
	if last.Role == provider.RoleTool {
		// The real model, resumed with end_call's output, says goodbye
		// again (the 18:11 simulated call on 10-17). The engine must not
		// ask it.
		l.mu.Lock()
		l.resumed++
		l.mu.Unlock()
		text = "再见！"
	} else if strings.Contains(last.Content, "再见") {
		text = "好的，祝您用车愉快，再见。"
		call = &provider.ToolCall{ID: id, Name: "end_call", Arguments: `{"reason":"客户道别"}`}
	}
	go func() {
		defer close(out)
		time.Sleep(100 * time.Millisecond)
		for _, r := range text {
			select {
			case out <- provider.LLMDelta{Text: string(r)}:
			case <-ctx.Done():
				return
			}
		}
		if call != nil {
			out <- provider.LLMDelta{ToolCall: call}
		}
	}()
	return out, nil
}

// On a phone call somebody has to hang up, or two models say goodbye to
// each other until the line drops. end_call lets the assistant do it: once
// the farewell it said in the same breath has played, and not before, and
// not in the first seconds of a call.
func TestTheAssistantHangsUpAfterItsFarewellHasPlayed(t *testing.T) {
	oldMin, oldGrace := minCallForEndCall, hangupGrace
	minCallForEndCall, hangupGrace = 0, 50*time.Millisecond
	defer func() { minCallForEndCall, hangupGrace = oldMin, oldGrace }()

	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("好的再见")
	asr.msPerRune = 150
	llm := &farewellLLM{}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, &slowQuote{})

	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the farewell to be said", 5*time.Second, func() bool { return strings.Contains(c.saidAll(), "再见") })
	hungUp := func() bool {
		return c.find(func(ev any) bool { _, ok := ev.(live.HangupEvent); return ok }) != nil
	}
	if hungUp() && c.audioMS() == 0 {
		t.Fatal("hung up before the farewell had any audio")
	}
	waitFor(t, "the hangup after the farewell", 8*time.Second, hungUp)
	ev := c.find(func(ev any) bool { _, ok := ev.(live.HangupEvent); return ok }).(live.HangupEvent)
	if ev.Reason != "客户道别" {
		t.Fatalf("reason %q", ev.Reason)
	}
	if e.player.Speaking() {
		t.Fatal("hung up while still speaking")
	}
	time.Sleep(300 * time.Millisecond)
	llm.mu.Lock()
	resumed := llm.resumed
	llm.mu.Unlock()
	if resumed != 0 {
		t.Fatalf("the backend was resumed %d times after end_call was accepted; a second goodbye would be cut by the hangup", resumed)
	}
	if strings.Contains(c.saidAll(), "再见！") {
		t.Fatalf("a second goodbye was said after end_call: %q", c.saidAll())
	}
}

// Too early in a call the model's end_call is refused and nothing hangs up.
func TestEndCallIsRefusedInTheFirstSecondsOfACall(t *testing.T) {
	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("好的再见")
	asr.msPerRune = 150
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: &farewellLLM{}, Emit: c.emit}, &slowQuote{})
	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the farewell to be said", 5*time.Second, func() bool { return strings.Contains(c.saidAll(), "再见") })
	time.Sleep(2 * time.Second)
	if c.find(func(ev any) bool { _, ok := ev.(live.HangupEvent); return ok }) != nil {
		t.Fatal("the call was ended in its first seconds")
	}
	if e.tools.endCall != nil {
		t.Fatal("an early end_call was accepted")
	}
}

// rulesService serves one hangup-rules config the way the lead service does.
func rulesService(t *testing.T, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/hangup-rules" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"config":{"name":"hangup-rules","version":7,"body":` + body + `}}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// shortLLM answers every turn with three characters, so the assistant is
// quiet again at once.
type shortLLM struct{}

func (shortLLM) Name() string { return "short" }
func (shortLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	out := make(chan provider.LLMDelta, 4)
	go func() {
		defer close(out)
		time.Sleep(80 * time.Millisecond)
		out <- provider.LLMDelta{Text: "好的。"}
	}()
	return out, nil
}

func hungUpWith(c *collector) (live.HangupEvent, bool) {
	ev := c.find(func(ev any) bool { _, ok := ev.(live.HangupEvent); return ok })
	if ev == nil {
		return live.HangupEvent{}, false
	}
	return ev.(live.HangupEvent), true
}

// A caller who has gone quiet is not kept on the line for ever: the silence
// rule from the owner's panel says the farewell and hangs up.
func TestTheSilenceRuleSaysItsFarewellAndHangsUp(t *testing.T) {
	oldMin, oldGrace := minCallForEndCall, hangupGrace
	minCallForEndCall, hangupGrace = 0, 50*time.Millisecond
	defer func() { minCallForEndCall, hangupGrace = oldMin, oldGrace }()
	svc := rulesService(t, `{"rules":[{"id":"quiet","kind":"silence","seconds":2,"farewell":"听不到您，再见。"}]}`)

	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = true
	opts.Cfg.ToolsBaseURL = svc.URL
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊")
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: shortLLM{}, Emit: c.emit}, &slowQuote{})
	if e.tools.rules.Version != 7 || len(e.tools.engine) != 1 {
		t.Fatalf("rules not read from the service: %+v", e.tools.rules)
	}

	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the answer", 5*time.Second, func() bool { return c.audioMS() > 100 })
	// The caller says nothing more.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hungUpWith(c); ok {
			break
		}
		pause(e, rate, 200)
	}
	ev, ok := hungUpWith(c)
	if !ok {
		t.Fatal("the silence rule did not hang up")
	}
	if ev.Reason != "quiet" {
		t.Fatalf("reason %q", ev.Reason)
	}
	if !strings.Contains(c.saidAll(), "听不到您") {
		t.Fatalf("the farewell was not said: %q", c.saidAll())
	}
}

// The caller's own words can end the call: a keyword rule (a complaint, a
// "stop calling") says the farewell instead of an answer, and hangs up.
func TestTheKeywordRuleEndsTheCallOnTheCallersWords(t *testing.T) {
	oldMin, oldGrace := minCallForEndCall, hangupGrace
	minCallForEndCall, hangupGrace = 0, 50*time.Millisecond
	defer func() { minCallForEndCall, hangupGrace = oldMin, oldGrace }()
	svc := rulesService(t, `{"rules":[{"id":"complaint","kind":"keyword","pattern":"投诉|别再打","farewell":"好的，不再打扰，再见。"}]}`)

	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = true
	opts.Cfg.ToolsBaseURL = svc.URL
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("别再打了")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, &slowQuote{})

	speak(e, rate, 700)
	pause(e, rate, 700)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hungUpWith(c); ok {
			break
		}
		pause(e, rate, 200)
	}
	ev, ok := hungUpWith(c)
	if !ok {
		t.Fatal("the keyword rule did not hang up")
	}
	if ev.Reason != "complaint" {
		t.Fatalf("reason %q", ev.Reason)
	}
	said := c.saidAll()
	if !strings.Contains(said, "不再打扰") {
		t.Fatalf("the farewell was not said: %q", said)
	}
	if strings.Contains(said, "我听到你说") {
		t.Fatalf("the model's answer was said as well as the farewell: %q", said)
	}
}

// The farewell is said whole. A sound over it — the caller's "嗯", a station
// announcement — does not cut it, and nothing said after the goodbye is
// answered: the line drops after the whole farewell and not before.
func TestTheFarewellIsNotCutByASoundOverIt(t *testing.T) {
	oldMin, oldGrace := minCallForEndCall, hangupGrace
	minCallForEndCall, hangupGrace = 0, 50*time.Millisecond
	defer func() { minCallForEndCall, hangupGrace = oldMin, oldGrace }()

	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("好的再见")
	asr.msPerRune = 150
	llm := &farewellLLM{}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, &slowQuote{})

	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the farewell to begin", 5*time.Second, func() bool { return c.audioMS() > 100 })
	// A sound over the farewell, long enough to be a barge-in anywhere else.
	speak(e, rate, 900)
	pause(e, rate, 2500)
	hungUp := func() bool { _, ok := hungUpWith(c); return ok }
	waitFor(t, "the hangup", 8*time.Second, hungUp)
	if n := c.count(live.ExtAudioTruncated); n != 0 {
		t.Fatalf("the farewell was cut (%d truncation reports)", n)
	}
	if !strings.Contains(c.saidAll(), "再见。") {
		t.Fatalf("the farewell was not said to its end: %q", c.saidAll())
	}
	llm.mu.Lock()
	resumed := llm.resumed
	llm.mu.Unlock()
	if resumed != 0 || llm.calls > 1 {
		t.Fatalf("the sound over the farewell was answered: calls %d resumed %d", llm.calls, resumed)
	}
}

// goodbyeLLM says goodbye without calling end_call, as the real model did
// twice on the 09:59 call of 10-20.
type goodbyeLLM struct{}

func (goodbyeLLM) Name() string { return "goodbye-no-tool" }
func (goodbyeLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	out := make(chan provider.LLMDelta, 4)
	go func() {
		defer close(out)
		time.Sleep(80 * time.Millisecond)
		out <- provider.LLMDelta{Text: "好的，再见。"}
	}()
	return out, nil
}

// A goodbye the model says without the tool still ends the call: the
// assistant rule in the default set matches the reply once it has played.
func TestAGoodbyeSaidWithoutTheToolStillEndsTheCall(t *testing.T) {
	oldMin, oldGrace := minCallForEndCall, hangupGrace
	minCallForEndCall, hangupGrace = 0, 50*time.Millisecond
	defer func() { minCallForEndCall, hangupGrace = oldMin, oldGrace }()

	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true // no lead service: the built-in rules
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("好的拜拜")
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: goodbyeLLM{}, Emit: c.emit}, &slowQuote{})

	speak(e, rate, 600)
	pause(e, rate, 700)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hungUpWith(c); ok {
			break
		}
		pause(e, rate, 200)
	}
	ev, ok := hungUpWith(c)
	if !ok {
		t.Fatal("the assistant said goodbye and the call did not end")
	}
	if ev.Reason != "said_goodbye" {
		t.Fatalf("reason %q", ev.Reason)
	}
	if e.player.Speaking() {
		t.Fatal("hung up while still speaking")
	}
}

// The engine's own tool rules follow the owner's prompt in the system
// prompt, so a prompt that says "no direct quotes" is outweighed by the
// rule that a customer who asks for a price gets one through the tool.
func TestTheToolRulesFollowTheOwnersPrompt(t *testing.T) {
	opts := quoteConfig()
	opts.Instructions = "你是小翠儿。不能直接报价。"
	opts.Cfg.InstructionsAppend = "【工具规则】客户要报价时调用 quote_total。"
	opts.ClientRate = opts.Cfg.ClientRate
	opts.SessionID = "test"
	e := New(opts, Deps{ASR: newTrailingASR("x"), LLM: mock.NewLLM(), TTS: mock.NewTTS(), Emit: func(any) {}})
	got := e.systemPrompt(false)
	i, j := strings.Index(got, "不能直接报价"), strings.Index(got, "【工具规则】")
	if i < 0 || j < 0 || j < i {
		t.Fatalf("prompt order wrong: %q", got)
	}
}

// The same tool asked twice in a row on the same memory is one task, not
// two results read out.
func TestARepeatedToolCallIsFoldedIntoThePreviousOne(t *testing.T) {
	opts := quoteConfig()
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	e := startQuoteEngine(t, opts, Deps{ASR: newTrailingASR("x"), LLM: mock.NewLLM(), Emit: c.emit}, &slowQuote{})
	done := make(chan int, 1)
	e.post(func() {
		job := backendJob{turn: e.tracker.Begin("", false)}
		call := provider.ToolCall{ID: "k1", Name: "quote_total", Arguments: `{}`}
		tool := e.tools.byName["quote_total"]
		e.submitTask(job, call, tool)
		call.ID = "k2"
		e.submitTask(job, call, tool)
		done <- e.tools.async
	})
	if n := <-done; n != 1 {
		t.Fatalf("tasks submitted %d; wanted the second call folded into the first", n)
	}
}

// The silence clock runs from whoever spoke last. A long answer is not
// the caller's silence: with a 2 s rule and a 4 s answer, the hangup must
// not come as the answer ends.
func TestTheSilenceRuleDoesNotCountTheAssistantsOwnAnswer(t *testing.T) {
	oldMin, oldGrace := minCallForEndCall, hangupGrace
	minCallForEndCall, hangupGrace = 0, 50*time.Millisecond
	defer func() { minCallForEndCall, hangupGrace = oldMin, oldGrace }()
	svc := rulesService(t, `{"rules":[{"id":"quiet","kind":"silence","seconds":2,"farewell":"听不到您，再见。"}]}`)

	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = true
	opts.Cfg.ToolsBaseURL = svc.URL
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊")
	llm := mock.NewLLM() // a long answer: several seconds of audio
	llm.DelayPerRune = 2 * time.Millisecond
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, &slowQuote{})

	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the answer", 5*time.Second, func() bool { return c.audioMS() > 100 })
	// Listen through the whole answer; the rule must not fire while it
	// plays nor the moment it ends.
	deadline := time.Now().Add(12 * time.Second)
	for e.assistantHasFloor() && time.Now().Before(deadline) {
		pause(e, rate, 200)
	}
	ended := time.Now()
	pause(e, rate, 800)
	if e.hangingUp() {
		t.Fatalf("hanging up %v after the answer ended: the answer was counted as the caller's silence", time.Since(ended))
	}
	// Two seconds of real silence after it: now it fires.
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hungUpWith(c); ok {
			break
		}
		pause(e, rate, 200)
	}
	if _, ok := hungUpWith(c); !ok {
		t.Fatal("the silence rule did not fire after the caller's real silence")
	}
}

// The caller answering the silence farewell ("喂？我在") must not cut it:
// no turn begins after the farewell, so nothing aborts its speech.
func TestTheCallerSpeakingOverTheSilenceFarewellDoesNotCutIt(t *testing.T) {
	oldMin, oldGrace := minCallForEndCall, hangupGrace
	minCallForEndCall, hangupGrace = 0, 50*time.Millisecond
	defer func() { minCallForEndCall, hangupGrace = oldMin, oldGrace }()
	svc := rulesService(t, `{"rules":[{"id":"quiet","kind":"silence","seconds":2,"farewell":"听不到您的声音，我稍后再联系您，再见。"}]}`)

	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = true
	opts.Cfg.ToolsBaseURL = svc.URL
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊我在呢")
	asr.msPerRune = 150
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: shortLLM{}, Emit: c.emit}, &slowQuote{})

	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the answer", 5*time.Second, func() bool { return c.audioMS() > 100 })
	waitFor(t, "the silence farewell to begin", 8*time.Second, func() bool { return e.hangingUp() })
	heard := c.audioMS()
	// The caller speaks over the farewell.
	speak(e, rate, 900)
	pause(e, rate, 2500)
	waitFor(t, "the hangup", 6*time.Second, func() bool { _, ok := hungUpWith(c); return ok })
	if n := c.count(live.ExtAudioTruncated); n != 0 {
		t.Fatalf("the farewell was cut (%d truncation reports)", n)
	}
	if c.audioMS() <= heard+300 {
		t.Fatalf("the farewell did not play on: %.0f ms before, %.0f after", heard, c.audioMS())
	}
}

// refusingLLM says the caller refused and asks to end the call under the
// "refused twice" rule on every turn.
type refusingLLM struct {
	mu    sync.Mutex
	calls int
}

func (l *refusingLLM) Name() string { return "refusing-script" }
func (l *refusingLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	l.mu.Lock()
	l.calls++
	id := fmt.Sprintf("call_%d", l.calls)
	l.mu.Unlock()
	out := make(chan provider.LLMDelta, 8)
	last := req.Messages[len(req.Messages)-1]
	go func() {
		defer close(out)
		time.Sleep(80 * time.Millisecond)
		if last.Role == provider.RoleTool {
			out <- provider.LLMDelta{Text: "好的，那我再介绍一下。"}
			return
		}
		out <- provider.LLMDelta{Text: "好的，不打扰您了，再见。"}
		out <- provider.LLMDelta{ToolCall: &provider.ToolCall{ID: id, Name: "end_call", Arguments: `{"rule":"refused_twice","reason":"客户明确拒绝"}`}}
	}()
	return out, nil
}

// A model rule with min_turns is not acted on before the caller has had that
// many turns: "refused twice" on the caller's first (garbled) line, 17 s into
// the 09:43 call of 10-23 (ISS-054). The model is told why and carries on.
func TestAModelHangupRuleWaitsForItsMinimumTurns(t *testing.T) {
	oldMin, oldGrace := minCallForEndCall, hangupGrace
	minCallForEndCall, hangupGrace = 0, 50*time.Millisecond
	defer func() { minCallForEndCall, hangupGrace = oldMin, oldGrace }()
	// With the goodbye net on too: the refused farewell must not end the
	// call by the back door.
	svc := rulesService(t, `{"rules":[{"id":"refused_twice","kind":"model","when":"客户明确拒绝且已挽留过一次","min_turns":2,"farewell":"好的，再见。"},
		{"id":"said_goodbye","kind":"assistant","pattern":"再见|不打扰您了"}]}`)

	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = true
	opts.Cfg.ToolsBaseURL = svc.URL
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("不需要不需要不需要不需要") // two utterances' worth: the phrase is consumed in order
	asr.msPerRune = 150
	llm := &refusingLLM{}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, &slowQuote{})
	if got := e.tools.rules.MinTurnsFor("refused_twice", ""); got != 2 {
		t.Fatalf("min_turns read as %d", got)
	}
	// Turn 1: the model wants to hang up; the rule needs two turns.
	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the first answer", 5*time.Second, func() bool { return c.audioMS() > 100 })
	time.Sleep(1500 * time.Millisecond)
	if e.tools.endCall != nil {
		t.Fatal("end_call was accepted on the caller's first turn")
	}
	if _, ok := hungUpWith(c); ok {
		t.Fatal("hung up on the first turn")
	}
	// Turn 2: now it may.
	pause(e, rate, 1500)
	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the second end_call", 6*time.Second, func() bool { return e.tools.endCall != nil })
}

// The owner's words for the wait and for the result (10-23): when an async
// tool is submitted the assistant says the tool's ack from the phrase cache
// — the model is not resumed to improvise one — and the reply that delivers
// the result opens with the tool's fixed opening before the model's first
// token. The opening is for results of the configured status only.
func TestAnAsyncToolsAckAndResultOpeningAreTheOwners(t *testing.T) {
	opts := quoteConfig()
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].Ack = "这会儿正在给您计算保费，请您稍等。"
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].ResultOpening = "保费给您计算好了，"
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].ResultOpeningStatus = "quote"
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱")
	asr.msPerRune = 150
	llm := &quoteLLM{}
	quote := &statusQuote{delay: 800 * time.Millisecond, status: "quote"}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, quote)

	speak(e, rate, 750)
	pause(e, rate, 700)
	waitFor(t, "the ack to be said", 6*time.Second, func() bool { return strings.Contains(c.saidAll(), "请您稍等") })
	if strings.Contains(c.saidAll(), "我这边马上帮您算") {
		t.Fatalf("the model was resumed to improvise the wait: %q", c.saidAll())
	}
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(c.saidAll(), "三千二百") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	said := c.saidAll()
	i, j := strings.Index(said, "保费给您计算好了"), strings.Index(said, "报价出来了")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("the result was not opened with the owner's words before the model's: %q", said)
	}
	if strings.Contains(said, "请您稍等。保费") == false && !strings.Contains(said, "请您稍等。") {
		t.Fatalf("the ack was cut: %q", said)
	}
	llm.mu.Lock()
	var resumed int
	for _, r := range llm.requests {
		if last := r.Messages[len(r.Messages)-1]; last.Role == provider.RoleTool {
			resumed++
		}
	}
	llm.mu.Unlock()
	if resumed != 0 {
		t.Fatalf("the model was resumed %d time(s) with the running output", resumed)
	}
}

// …and a "need" result (a field is missing) gets no "计算好了" opening — and
// when it comes back at once, as the rules' does, no "请您稍等" either: there
// was nothing to wait for (the owner, 22:31 on 10-23).
func TestTheResultOpeningIsOnlyForItsStatus(t *testing.T) {
	opts := quoteConfig()
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].Ack = "这会儿正在给您计算保费，请您稍等。"
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].ResultOpening = "保费给您计算好了，"
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].ResultOpeningStatus = "quote"
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱")
	asr.msPerRune = 150
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: &quoteLLM{}, Emit: c.emit}, &statusQuote{delay: 10 * time.Millisecond, status: "need"})
	speak(e, rate, 750)
	pause(e, rate, 700)
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(c.saidAll(), "报价出来了") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	if strings.Contains(c.saidAll(), "保费给您计算好了") {
		t.Fatalf("a need result was opened as a quote: %q", c.saidAll())
	}
	if strings.Contains(c.saidAll(), "请您稍等") {
		t.Fatalf("the ack was said for a result that was already in: %q", c.saidAll())
	}
}

// statusQuote answers with a status, as the lead service does; dflt marks
// the result as the prefetched default quotation.
type statusQuote struct {
	delay  time.Duration
	status string
	dflt   bool
}

func (s *statusQuote) Execute(ctx context.Context, t tools.Tool, req tools.Request) tools.Result {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return tools.Result{Err: ctx.Err()}
	}
	// A quote, as the lead service sends it, has no status field: total and
	// lines say what it is. A need has.
	vars := map[string]any{"total": 3200, "lines": []any{map[string]any{"coverage": "交强险", "premium": 950}}, "memory_version": req.MemoryVersion}
	if s.dflt {
		vars["default"] = true
	}
	if s.status != "quote" {
		vars = map[string]any{"status": s.status, "memory_version": req.MemoryVersion}
	}
	out, _ := json.Marshal(vars)
	return tools.Result{Output: out, Took: s.delay}
}

// A quote that is back at once (the placeholder rules take 15 ms) is still
// announced: ack, then the opening, then the model's words, in that order
// (the owner, 23:15 on 10-23: "where is 请您稍等").
func TestAnInstantQuoteStillGetsTheAckThenTheOpening(t *testing.T) {
	opts := quoteConfig()
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].Ack = "这会儿正在给您计算保费，请您稍等。"
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].ResultOpening = "保费给您计算好了，"
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].ResultOpeningStatus = "quote"
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱")
	asr.msPerRune = 150
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: &quoteLLM{}, Emit: c.emit}, &statusQuote{delay: 10 * time.Millisecond, status: "quote"})
	speak(e, rate, 750)
	pause(e, rate, 700)
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(c.saidAll(), "三千二百") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	said := c.saidAll()
	a, o, m := strings.Index(said, "请您稍等"), strings.Index(said, "保费给您计算好了"), strings.Index(said, "报价出来了")
	if a < 0 || o < 0 || m < 0 || !(a < o && o < m) {
		t.Fatalf("wanted ack, opening, then the model's words; said %q", said)
	}
}

// The panel's quote-speech config: the figures are read out from its
// template right after the posthook, before the model says anything, and
// the model is told not to repeat them.
func TestTheQuoteIsReadOutFromThePanelsTemplate(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/quote-speech" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"config":{"name":"quote-speech","version":2,"body":{"prehook":"马上给您算。","posthook":"算好了，","posthook_status":"quote","template":"一共{{ total | round | int }}元整。"}}}`))
	}))
	t.Cleanup(svc.Close)
	opts := quoteConfig()
	opts.Cfg.ToolsBaseURL = svc.URL
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].Speech = "quote-speech"
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱")
	asr.msPerRune = 150
	llm := &quoteLLM{}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, &statusQuote{delay: 10 * time.Millisecond, status: "quote"})
	if d := e.tools.byName["quote_total"]; d.Ack != "马上给您算。" || d.Speech == nil {
		t.Fatalf("the panel's speech was not applied: %+v", d)
	}
	speak(e, rate, 750)
	pause(e, rate, 700)
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(c.saidAll(), "报价出来了") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	said := c.saidAll()
	a, o, r, m := strings.Index(said, "马上给您算"), strings.Index(said, "算好了，"), strings.Index(said, "一共3200元整"), strings.Index(said, "报价出来了")
	if a < 0 || o < 0 || r < 0 || m < 0 || !(a < o && o < r && r < m) {
		t.Fatalf("wanted prehook, posthook, the readout, then the model; said %q", said)
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	last := llm.requests[len(llm.requests)-1].Messages
	if note := last[len(last)-1].Content; !strings.Contains(note, "不得出现任何数字") || strings.Contains(note, "报总价和关键构成") {
		t.Fatalf("the model was not told the figures were read out: %q", note)
	}
}

// The default quotation was ready before the call: neither the prehook nor
// the posthook is said — the figures are read out and the model carries on
// (the owner, 22:29 on 10-24).
func TestTheDefaultQuotationGetsNeitherPrehookNorPosthook(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/quote-speech" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"config":{"name":"quote-speech","version":2,"body":{"prehook":"马上给您算。","posthook":"算好了，","posthook_status":"quote","template":"一共{{ total | round | int }}元整。"}}}`))
	}))
	t.Cleanup(svc.Close)
	opts := quoteConfig()
	opts.Cfg.ToolsBaseURL = svc.URL
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].Speech = "quote-speech"
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱")
	asr.msPerRune = 150
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: &quoteLLM{}, Emit: c.emit}, &statusQuote{delay: 10 * time.Millisecond, status: "quote", dflt: true})
	speak(e, rate, 750)
	pause(e, rate, 700)
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(c.saidAll(), "报价出来了") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	said := c.saidAll()
	if strings.Contains(said, "马上给您算") || strings.Contains(said, "算好了，") {
		t.Fatalf("the default quotation was announced as a computation: %q", said)
	}
	if r, m := strings.Index(said, "一共3200元整"), strings.Index(said, "报价出来了"); r < 0 || m < 0 || r > m {
		t.Fatalf("the figures were not read out first: %q", said)
	}
	_ = e
}

// talkativeQuoteLLM talks through the turn that calls the tool — three
// clauses, against the instructions — as the real model did on the 20:35
// call of 10-25 (4.2 s before the ready default was read out).
type talkativeQuoteLLM struct{ quoteLLM }

func (l *talkativeQuoteLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	last := req.Messages[len(req.Messages)-1]
	if !strings.Contains(last.Content, "多少钱") || last.Role == provider.RoleSystem {
		return l.quoteLLM.Stream(ctx, req)
	}
	l.mu.Lock()
	l.requests = append(l.requests, req)
	l.calls++
	id := fmt.Sprintf("call_%d", l.calls)
	l.mu.Unlock()
	out := make(chan provider.LLMDelta, 8)
	go func() {
		defer close(out)
		time.Sleep(150 * time.Millisecond)
		out <- provider.LLMDelta{Text: "好的，我帮您看一下基本方案，这个方案是按您的车辆信息来算的，您稍微等一下就好。"}
		out <- provider.LLMDelta{ToolCall: &provider.ToolCall{ID: id, Name: "quote_total", Arguments: `{}`}}
	}()
	return out, nil
}

// The model's words in the turn that calls the tool end at the clause in
// flight: the script (the ack, or a result already in) follows at once
// instead of after the whole sentence.
func TestTheModelsWordsInTheCallingTurnEndAtTheClauseInFlight(t *testing.T) {
	opts := quoteConfig()
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].Ack = "这会儿正在给您计算保费，请您稍等。"
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].ResultOpening = "保费给您计算好了，"
	opts.Cfg.Tools[len(opts.Cfg.Tools)-1].ResultOpeningStatus = "quote"
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱")
	asr.msPerRune = 150
	tts := mock.NewTTS()
	tts.FirstByteDelay = 250 * time.Millisecond // a vendor's first audio: the later clauses are not yet asked for when the call lands
	llm := &talkativeQuoteLLM{}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit, TTS: tts}, &statusQuote{delay: 1500 * time.Millisecond, status: "quote"})
	speak(e, rate, 750)
	pause(e, rate, 700)
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(c.saidAll(), "三千二百") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	said := c.saidAll()
	if !strings.Contains(said, "请您稍等") || !strings.Contains(said, "保费给您计算好了") {
		t.Fatalf("the script was not said: %q", said)
	}
	if strings.Contains(said, "您稍微等一下就好") {
		t.Fatalf("the model's sentence was said to its end before the script: %q", said)
	}
}
