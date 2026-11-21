package duplex

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// applicationCallsLLM completes a batch of application calls immediately, so
// registration on the engine loop can overlap backend event reporting.
type applicationCallsLLM struct {
	calls        []provider.ToolCall
	continuation chan provider.LLMRequest
}

func (*applicationCallsLLM) Name() string { return "application-calls" }

func (l *applicationCallsLLM) Stream(_ context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	out := make(chan provider.LLMDelta, len(l.calls))
	if req.Messages[len(req.Messages)-1].Role == provider.RoleTool {
		l.continuation <- req
	} else {
		for _, call := range l.calls {
			out <- provider.LLMDelta{ToolCall: &call}
		}
	}
	close(out)
	return out, nil
}

func TestBackendRegistersEveryApplicationCallBeforeAcceptingResults(t *testing.T) {
	cfg := parityConfig()
	c := newCollector(cfg.ClientRate)
	llm := &applicationCallsLLM{
		calls: []provider.ToolCall{
			{ID: "first", Name: "lookup", Arguments: `{"key":"first"}`},
			{ID: "second", Name: "lookup", Arguments: `{"key":"second"}`},
		},
		continuation: make(chan provider.LLMRequest, 1),
	}
	e := startEngine(t, Options{
		Cfg: cfg, Delegation: live.DelegationResponses,
		Tools: []json.RawMessage{lookupTool},
	}, Deps{LLM: llm, Emit: c.emit})
	e.SubmitItem(live.ResponseItem{
		Type: "message", Role: "user", Content: live.TextContent("input_text", "Look up both records."),
	}, "question")
	e.ContinueResponse("start")
	waitFor(t, "both application calls", time.Second, func() bool { return len(c.functionCalls()) == 2 })
	for _, call := range llm.calls {
		e.SubmitItem(live.ResponseItem{
			Type: "function_call_output", CallID: call.ID, Output: live.FlexString("result for " + call.ID),
		}, call.ID)
	}
	e.ContinueResponse("resume")
	select {
	case req := <-llm.continuation:
		assertWellFormed(t, req.Messages)
		n := len(req.Messages)
		if n < 3 || len(req.Messages[n-3].ToolCalls) != len(llm.calls) {
			t.Fatalf("the continuation lost the application calls: %s", dump(req.Messages))
		}
		for i, want := range llm.calls {
			if got := req.Messages[n-3].ToolCalls[i]; got != want {
				t.Errorf("call %d = %+v, want %+v", i, got, want)
			}
			if got := req.Messages[n-2+i]; got.ToolCallID != want.ID || got.Content != "result for "+want.ID {
				t.Errorf("result %d = %+v", i, got)
			}
		}
	case <-time.After(time.Second):
		t.Fatalf("the registered calls did not accept both results; errors: %v", c.errorCodes())
	}
	if codes := c.errorCodes(); len(codes) != 0 {
		t.Fatalf("application results were rejected: %v", codes)
	}
}
