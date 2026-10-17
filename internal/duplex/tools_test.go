package duplex

import (
	"context"
	"encoding/json"
	"fmt"
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
	deps.TTS = mock.NewTTS()
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

// A result that arrives as the assistant finishes a sentence is not read out
// into the next tick: both sides must have been quiet for a while. The tick
// after an answer ends, or the tick after the caller stops, is where a
// mid-request pause lives, and a price announced there lands on the rest of
// the request.
func TestAResultWaitsForARealGap(t *testing.T) {
	const quiet = 900
	opts := quoteConfig()
	opts.Cfg.ToolsDeliverQuietMS = quiet
	opts.Cfg.ToolsDeliverWaitMS = 6000 // never the reason it is read out here
	rate := opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("一共多少钱")
	asr.msPerRune = 150
	llm := &quoteLLM{}
	// Done well before the "please wait" reply has finished playing, so the
	// result is waiting the moment that reply ends.
	quote := &slowQuote{delay: 400 * time.Millisecond}
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, quote)

	speak(e, rate, 750)
	pause(e, rate, 700)
	waitFor(t, "the assistant to say it is calculating", 5*time.Second, func() bool { return strings.Contains(c.saidAll(), "稍等") })

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(c.saidAll(), "报价出来了") && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	if !strings.Contains(c.saidAll(), "报价出来了") {
		t.Fatalf("the result was not read out; said: %q", c.saidAll())
	}

	// On the session clock: the last audio of the "please wait" answer and
	// the first audio of the result.
	var resultItem string
	for _, ev := range c.taskEvents() {
		if ev.Status == "delivered" {
			resultItem = "delivered"
		}
	}
	if resultItem == "" {
		t.Fatal("no delivery event")
	}
	c.mu.Lock()
	var waitEnd, resultStart int64 = -1, -1
	var waitItem string
	for _, ev := range c.events {
		d, ok := ev.(live.TranscriptDelta)
		if ok && strings.Contains(d.Delta, "稍等") {
			waitItem = d.ItemID
		}
	}
	for _, ev := range c.events {
		a, ok := ev.(live.OutputAudioDelta)
		if !ok {
			continue
		}
		if a.ItemID == waitItem && a.EndMS > waitEnd {
			waitEnd = a.EndMS
		}
	}
	for _, ev := range c.events {
		d, ok := ev.(live.TranscriptDelta)
		if ok && strings.Contains(d.Delta, "报价出来了") {
			for _, ev := range c.events {
				if a, ok := ev.(live.OutputAudioDelta); ok && a.ItemID == d.ItemID && (resultStart < 0 || a.StartMS < resultStart) {
					resultStart = a.StartMS
				}
			}
		}
	}
	c.mu.Unlock()
	if waitEnd < 0 || resultStart < 0 {
		t.Fatalf("could not place the two answers on the clock: wait ends %d, result starts %d", waitEnd, resultStart)
	}
	if gap := resultStart - waitEnd; gap < quiet {
		t.Fatalf("the result began %dms after the answer before it; tools_deliver_quiet_ms is %d", gap, quiet)
	}
}
