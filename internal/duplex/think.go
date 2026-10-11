package duplex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
)

// This file is the think channel: delegation in both of gpt-live-1's modes,
// the function-call round trip, and the three ways an application talks back.
//
// gpt-live-1 is two models. A conversational one holds the call, and when a
// request needs more than conversation it delegates — to a Responses model
// OpenAI runs (responses mode), or to the application (client mode) — and
// keeps talking while that work happens. golive has one language model and
// arranges it to fit:
//
//	responses        every caller turn goes to the configured backend, with the
//	                 session's delegation.responses settings: its instructions,
//	                 its function tools. Text comes back and is spoken; function
//	                 calls go to the client inside response.event, and their
//	                 results come back through response.item.create and
//	                 response.create.
//	client, always   every caller turn is announced with
//	                 session.delegation.created and golive says nothing itself.
//	                 The application answers with session.commentary.append.
//	client, auto     the backend plays the conversational model. It is given one
//	                 function, delegate, and decides for itself whether a turn
//	                 needs the application.
//
// In all three the conversation is never blocked on backend work. A caller can
// keep talking while a function runs, and its result is read back when the
// floor is next free — "interrupting speech does not cancel backend work".

// pendingResponse is a backend response that ended in function calls and is
// waiting for the application to run them.
type pendingResponse struct {
	delegationID string
	turn         *Turn
	calls        []provider.ToolCall
	outputs      map[string]string
	createdAt    time.Time
}

func (p *pendingResponse) complete() bool {
	for _, c := range p.calls {
		if _, ok := p.outputs[c.ID]; !ok {
			return false
		}
	}
	return true
}

func (p *pendingResponse) missing() []string {
	var ids []string
	for _, c := range p.calls {
		if _, ok := p.outputs[c.ID]; !ok {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// backendJob is one streaming completion and where its results belong.
type backendJob struct {
	turn         *Turn
	delegationID string
	responseID   string
	// front marks the conversational model of client-auto mode. Its stream is
	// the live layer thinking, not a delegation, so none of it is reported in
	// response.event.
	front bool
}

// delegateTool is the one function the conversational model has in client-auto
// mode.
const delegateToolName = "delegate"

var delegateTool = json.RawMessage(`{"type":"function","function":{"name":"delegate",` +
	`"description":"Hand the user's request to the backend application. Use it whenever answering ` +
	`needs information or an action this conversation cannot supply by itself: looking something up, ` +
	`reading or changing a record, booking, cancelling, anything that must be checked rather than ` +
	`recalled. Do not use it for greetings, small talk or clarifying questions.",` +
	`"parameters":{"type":"object","properties":{"request":{"type":"string","description":` +
	`"What the backend should do, in one sentence, including every detail the user gave."}},` +
	`"required":["request"]}}}`)

// delegationPolicy is appended to the instructions of the conversational model
// in client-auto mode. gpt-live-1 asks its users to write this section of the
// prompt themselves; here it is supplied, because without it a general-purpose
// model answers from memory instead of delegating.
const delegationPolicy = "You are the conversational layer of a voice service. A backend application " +
	"can do what you cannot: look things up, read and change records, take actions. When a request " +
	"needs it, call the delegate function, and say in a few words that you are checking. Never state " +
	"a result the backend has not given you, and never guess one while you wait. Results arrive " +
	"later and are spoken for you. Greetings, small talk and clarifying questions you answer yourself."

// queuedCommentary is something an application asked to have said, waiting for
// a moment it can be said in.
type queuedCommentary struct {
	delegationID string
	text         string
}

// Bounds on what a client — or a sideband — can leave queued in a session.
const (
	// maxQueuedItems is how many typed messages, or commentary appends, may
	// be waiting at once.
	maxQueuedItems = 32
	// maxTypedChars bounds typed input waiting for response.create.
	maxTypedChars = 8000
	// maxFunctionOutputChars bounds one function_call_output. It is sent to
	// the backend whole, so it is the backend's context it spends.
	maxFunctionOutputChars = 16000
	// maxInstructionAppends is how many maximum-size instruction appends a
	// session may accumulate.
	maxInstructionAppends = 8
)

func delegationIDOf(turn *Turn) string {
	if turn.DelegationID != "" {
		return turn.DelegationID
	}
	return fmt.Sprintf("%s.%d", turn.ID, turn.Revision)
}

func (e *Engine) clientMode() bool { return e.opts.Delegation == live.DelegationClient }

func (e *Engine) clientAuto() bool {
	return e.clientMode() && e.opts.ClientDelegation == "auto"
}

func (e *Engine) sessionMS() int64 { return time.Since(e.startAt).Milliseconds() }

func (e *Engine) cancelBackend() {
	if e.backendCancel != nil {
		e.backendCancel()
		e.backendCancel = nil
	}
}

// --- starting work ---

// beginGeneration starts whatever answers a turn. spoken says the turn came
// from the caller's voice, which is what gives it a speech-end origin to
// measure latency from.
func (e *Engine) beginGeneration(turn *Turn, reason string, spoken bool) {
	if len([]rune(turn.Transcript)) < e.opts.Cfg.Duplex.DelegateMinChars && turn.Transcript != "" {
		return
	}
	// Only stamp the origin when it belongs to this turn. While the utterance
	// is still open — which is the case for a mid-pause speculative turn — the
	// engine's speechEndAt is the *previous* utterance's, and stamping it here
	// dated the turn to the last thing the caller said. That is where
	// llm_first_token_ms=16279 came from: a real number, measured from the
	// wrong zero. A speculative turn is stamped when speech actually ends, and
	// a turn nobody spoke has no origin at all.
	if spoken && !e.userOpen && !e.speechEndAt.IsZero() {
		turn.MarkSpeechEnd(e.speechEndAt, e.speechEndMS, e.speechEndSilenceMS)
	}
	// Something the caller said earlier and was never answered — they carried
	// on after a pause that closed the turn, or asked again before the answer
	// came — is still part of what they are asking now.
	if prev := e.lastCommitted; prev != nil && prev.ID != turn.ID {
		e.ensureUserRecorded(prev)
	}

	e.delegatedAt = time.Now()
	e.delegatedTurn = turn
	e.filled = false
	e.cancelBackend()

	e.log.Debug("think: delegating",
		"turn", turn.ID,
		"revision", turn.Revision,
		"target", e.opts.Delegation,
		"reason", reason,
		"transcript", turn.Transcript,
		"history_messages", len(e.history))

	if e.clientMode() && !e.clientAuto() {
		// The application owns the work. It will answer with
		// session.commentary.append or session.thinking.append.
		e.announceClientDelegation(turn, delegationIDOf(turn), reason, "")
		e.publishState()
		return
	}
	e.startBackend(turn, reason)
	e.publishState()
}

// announceClientDelegation tells the application there is work for it.
//
// The standard event is held while the turn is still a guess, because a
// delegation is not something gpt-live-1 ever takes back and an application is
// entitled to act on one. In its place, at once, goes a hint that is explicitly
// retractable, for applications that can use the head start.
func (e *Engine) announceClientDelegation(turn *Turn, id, reason, request string) {
	e.registerDelegation(id)
	offset := e.sessionMS()
	e.hintSpeculation(turn, id, live.DelegationClient)
	turn.emitOrHold(live.DelegationCreatedEvent{
		Envelope: live.Envelope{Type: live.ServerDelegationCreated},
		OffsetMS: offset,
		Delegation: live.Delegation{
			ID:         id,
			Type:       "delegation",
			Target:     live.DelegationClient,
			Reason:     reason,
			Transcript: turn.Transcript,
			Request:    request,
		},
	}, e.deps.Emit)
}

// hintSpeculation says, on a golive extension event, that work has started on
// a turn that is still a guess. The standard announcement waits for the guess
// to be confirmed; this does not, and says so.
func (e *Engine) hintSpeculation(turn *Turn, id, target string) {
	if turn.Committed() {
		return
	}
	e.deps.Emit(live.SpeculativeDelegationEvent{
		Envelope:     live.Envelope{Type: live.ExtDelegationSpeculative},
		OffsetMS:     e.sessionMS(),
		DelegationID: id,
		Target:       target,
		Transcript:   turn.Transcript,
	})
}

func (e *Engine) startBackend(turn *Turn, reason string) {
	if e.deps.LLM == nil {
		return
	}
	front := e.clientAuto()
	e.respSeq++
	job := backendJob{
		turn:         turn,
		delegationID: delegationIDOf(turn),
		responseID:   fmt.Sprintf("resp_%s_%d", e.opts.SessionID, e.respSeq),
		front:        front,
	}
	if !front {
		e.registerDelegation(job.delegationID)
		e.hintSpeculation(turn, job.delegationID, live.DelegationResponses)
		turn.emitOrHold(live.DelegationCreatedEvent{
			Envelope: live.Envelope{Type: live.ServerDelegationCreated},
			OffsetMS: e.sessionMS(),
			Delegation: live.Delegation{
				ID:         job.delegationID,
				Type:       "delegation",
				Target:     live.DelegationResponses,
				ResponseID: job.responseID,
				Reason:     reason,
				Transcript: turn.Transcript,
			},
		}, e.deps.Emit)
	}

	ctx, cancel := context.WithCancel(e.ctx)
	e.backendCancel = cancel
	e.backendPending.Add(1)
	go e.runBackend(ctx, cancel, job, e.backendRequest(e.snapshotMessages(turn, front), front))
}

// backendRequest fills in a completion from the session's settings.
func (e *Engine) backendRequest(msgs []provider.Message, front bool) provider.LLMRequest {
	req := provider.LLMRequest{
		Model:           e.opts.Cfg.BackendModel,
		Messages:        msgs,
		Temperature:     e.opts.Cfg.Temperature,
		MaxTokens:       e.opts.Cfg.MaxOutputTokens,
		DisableThinking: e.opts.Cfg.DisableThinking,
	}
	if front {
		req.Tools = []json.RawMessage{delegateTool}
		req.ToolChoice = "auto"
		return req
	}
	if e.backend.Model != "" {
		req.Model = e.backend.Model
	}
	if e.backend.MaxOutputTokens > 0 {
		req.MaxTokens = e.backend.MaxOutputTokens
	}
	req.Tools = append([]json.RawMessage(nil), e.backend.Tools...)
	req.ToolChoice = e.backend.ToolChoice
	req.ParallelToolCalls = e.backend.ParallelToolCalls
	return req
}

// systemPrompt assembles the instructions a completion runs under.
//
// The order is fixed and the conversational instructions come first, so the
// text is byte-identical from one turn to the next for as long as nothing is
// appended to it. A provider with a prefix cache then has the longest part of
// the prompt — which is usually this — already processed.
func (e *Engine) systemPrompt(front bool) string {
	e.instrMu.RLock()
	parts := []string{strings.TrimSpace(e.instr)}
	e.instrMu.RUnlock()
	if !e.clientMode() {
		parts = append(parts, strings.TrimSpace(e.backend.Instructions))
	}
	if front {
		parts = append(parts, delegationPolicy)
	}
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}

// snapshotMessages builds the backend request on the loop goroutine so the
// generation goroutine never touches mutable history.
func (e *Engine) snapshotMessages(turn *Turn, front bool) []provider.Message {
	msgs := make([]provider.Message, 0, len(e.history)+2)
	if system := e.systemPrompt(front); system != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: system})
	}
	msgs = append(msgs, e.history...)
	if turn != nil && turn.Transcript != "" && !e.recordedUser[turn.ID] {
		msgs = appendUserMessage(msgs, turn.Transcript)
	}
	return msgs
}

// --- running it ---

func (e *Engine) runBackend(ctx context.Context, cancel context.CancelFunc, job backendJob, req provider.LLMRequest) {
	// Released on every way out. The context used to outlive an abandoned
	// stream whenever the turn was superseded by something other than a
	// barge-in, which left the provider's goroutine blocked writing into a
	// channel nobody read, and the HTTP connection under it unavailable for the
	// next request — which then paid a fresh handshake.
	defer cancel()
	turn := job.turn

	// The request counts as an answer in progress until it has a speech pipe to
	// show for it, or has failed to get one. See Engine.assistantHasFloor.
	var once sync.Once
	settled := func() {
		once.Do(func() {
			e.backendPending.Add(-1)
			e.wake()
		})
	}
	defer settled()

	// report sends a nested backend event, held back while the turn is a guess.
	report := func(event map[string]any) {
		if job.front {
			return
		}
		turn.emitOrHold(live.ResponseEventEnvelope{
			Envelope:     live.Envelope{Type: live.ServerResponseEvent},
			DelegationID: job.delegationID,
			Event:        rawJSON(event),
		}, e.deps.Emit)
	}

	deltas, err := e.deps.LLM.Stream(ctx, req)
	if err != nil && ctx.Err() == nil && retryable(err) {
		metrics.Default().Retry("llm")
		e.log.Warn("llm stream retrying", "err", err)
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return
		}
		deltas, err = e.deps.LLM.Stream(ctx, req)
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		metrics.Default().ProviderError("llm")
		e.log.Error("llm stream failed", "err", err)
		e.deps.Emit(live.NewError("server_error", "backend_unavailable", err.Error(), ""))
		return
	}
	report(map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": job.responseID, "object": "response", "status": "in_progress", "model": req.Model},
	})

	pipe := e.newSpeechPipe(turn, false)
	settled()
	var full strings.Builder
	var calls []provider.ToolCall
	var usage provider.TokenUsage
	started := time.Now()
	firstToken := true
	failed := false

	for d := range deltas {
		if ctx.Err() != nil || !e.tracker.IsCurrent(turn) {
			pipe.Abort()
			return
		}
		if d.Err != nil {
			e.log.Error("llm delta error", "err", d.Err)
			e.deps.Emit(live.NewError("server_error", "backend_error", d.Err.Error(), ""))
			failed = true
			break
		}
		if d.Usage != nil {
			usage = *d.Usage
			e.inTok.Add(int64(d.Usage.InputTokens))
			e.outTok.Add(int64(d.Usage.OutputTokens))
			e.cachedTok.Add(int64(d.Usage.CachedInputTokens))
		}
		if d.ToolCall != nil {
			calls = append(calls, *d.ToolCall)
			continue
		}
		if d.Text == "" {
			continue
		}
		turn.MarkFirstToken()
		if firstToken {
			firstToken = false
			e.log.Debug("think: first token",
				"turn", turn.ID,
				"provider", e.deps.LLM.Name(),
				"model", req.Model,
				"ms", time.Since(started).Milliseconds())
		}
		full.WriteString(d.Text)
		report(map[string]any{
			"type":        "response.output_text.delta",
			"response_id": job.responseID,
			"delta":       d.Text,
		})
		pipe.Push(d.Text)
	}

	// A stream that ended because it was cancelled did not complete, and what
	// it had produced so far is not the response.
	if ctx.Err() != nil || !e.tracker.IsCurrent(turn) {
		pipe.Abort()
		return
	}

	text := full.String()
	turn.AppendResponse(text)
	e.log.Debug("think: backend complete",
		"turn", turn.ID,
		"chars", len([]rune(text)),
		"tool_calls", len(calls),
		"cached_prompt_tokens", usage.CachedInputTokens,
		"ms", time.Since(started).Milliseconds())
	pipe.Close()

	if len(calls) > 0 && !failed {
		// A function call is the one thing here that cannot be unsaid: the
		// application will run it. So it waits until the turn it came from is
		// certain, and if that turn is abandoned instead, nobody ever hears of
		// it.
		if !turn.awaitCommit(ctx.Done()) {
			return
		}
		// Registered before it is announced: an application on the same machine
		// can answer a call faster than a goroutine can be scheduled, and a
		// result for a call the engine has not yet recorded would be refused.
		if job.front {
			e.post(func() { e.onDelegateCalls(job, calls) })
		} else {
			e.post(func() { e.onToolCalls(job, calls) })
			for i, call := range calls {
				report(map[string]any{
					"type":         "response.output_item.done",
					"response_id":  job.responseID,
					"output_index": i,
					"item": map[string]any{
						"type":      "function_call",
						"id":        "fc_" + call.ID,
						"call_id":   call.ID,
						"name":      call.Name,
						"arguments": call.Arguments,
						"status":    "completed",
					},
				})
			}
		}
	}
	if text != "" {
		report(map[string]any{
			"type":         "response.output_item.done",
			"response_id":  job.responseID,
			"output_index": len(calls),
			"item": map[string]any{
				"type":    "message",
				"role":    "assistant",
				"status":  "completed",
				"content": []map[string]any{{"type": "output_text", "text": text}},
			},
		})
	}
	status := "completed"
	if failed {
		status = "failed"
	}
	report(map[string]any{
		"type": "response." + status,
		"response": map[string]any{
			"id":     job.responseID,
			"object": "response",
			"status": status,
			"model":  req.Model,
			"usage": map[string]any{
				"input_tokens":         usage.InputTokens,
				"output_tokens":        usage.OutputTokens,
				"total_tokens":         usage.InputTokens + usage.OutputTokens,
				"input_tokens_details": map[string]any{"cached_tokens": usage.CachedInputTokens},
			},
		},
	})
}

// --- function calls: responses mode ---

// onToolCalls records that a backend response is waiting on the application.
//
// Nothing goes into conversation history here. The calls and their results are
// added together, when the results are in — a chat backend requires each tool
// message to follow the assistant message that asked for it, and a call left in
// history with no result (the application never answered, the caller hung up)
// poisons every later request.
func (e *Engine) onToolCalls(job backendJob, calls []provider.ToolCall) {
	// The question has to be on record before the calls it led to.
	e.ensureUserRecorded(job.turn)

	if old, ok := e.pending[job.delegationID]; ok {
		for _, c := range old.calls {
			delete(e.callIndex, c.ID)
		}
	} else {
		e.pendingOrder = append(e.pendingOrder, job.delegationID)
	}
	e.pending[job.delegationID] = &pendingResponse{
		delegationID: job.delegationID,
		turn:         job.turn,
		calls:        calls,
		outputs:      map[string]string{},
		createdAt:    time.Now(),
	}
	for _, c := range calls {
		e.callIndex[c.ID] = job.delegationID
	}
	e.log.Debug("think: waiting on the application for function results",
		"delegation", job.delegationID, "calls", len(calls))
	// Bound what an application that never answers can leave behind.
	for len(e.pendingOrder) > 16 {
		e.dropPending(e.pendingOrder[0])
	}
}

func (e *Engine) dropPending(id string) {
	if p, ok := e.pending[id]; ok {
		for _, c := range p.calls {
			delete(e.callIndex, c.ID)
		}
		delete(e.pending, id)
	}
	for i, v := range e.pendingOrder {
		if v == id {
			e.pendingOrder = append(e.pendingOrder[:i], e.pendingOrder[i+1:]...)
			break
		}
	}
}

// SubmitItem accepts response.item.create: a function result, or a message the
// user typed. Neither starts anything; response.create does.
func (e *Engine) SubmitItem(item live.ResponseItem, clientEventID string) {
	e.post(func() {
		switch item.Type {
		case "function_call_output":
			id, ok := e.callIndex[item.CallID]
			if !ok {
				e.deps.Emit(live.NewError("invalid_request_error", "unknown_call_id",
					fmt.Sprintf("no pending tool call with call_id %q", item.CallID), clientEventID))
				return
			}
			if n := len([]rune(string(item.Output))); n > maxFunctionOutputChars {
				e.deps.Emit(live.NewError("invalid_request_error", "content_too_long",
					fmt.Sprintf("the function output is %d characters; the limit is %d. Return what the "+
						"model needs to answer, not the whole record.", n, maxFunctionOutputChars), clientEventID))
				return
			}
			e.pending[id].outputs[item.CallID] = string(item.Output)
		case "message":
			if item.Role != "" && item.Role != provider.RoleUser {
				e.deps.Emit(live.NewError("invalid_request_error", "unsupported_item",
					fmt.Sprintf("only user messages can be queued; got role %q", item.Role), clientEventID))
				return
			}
			text := strings.TrimSpace(item.Content.Text())
			if text == "" {
				e.deps.Emit(live.NewError("invalid_request_error", "empty_content",
					"the message has no input_text content", clientEventID))
				return
			}
			queued := len([]rune(text))
			for _, t := range e.typed {
				queued += len([]rune(t))
			}
			if len(e.typed) >= maxQueuedItems || queued > maxTypedChars {
				e.deps.Emit(live.NewError("invalid_request_error", "content_too_long",
					fmt.Sprintf("too much typed input is waiting for response.create (the limit is %d "+
						"messages and %d characters)", maxQueuedItems, maxTypedChars), clientEventID))
				return
			}
			e.typed = append(e.typed, text)
		default:
			e.deps.Emit(live.NewError("invalid_request_error", "unsupported_item",
				fmt.Sprintf("item type %q is not supported; use function_call_output or message", item.Type),
				clientEventID))
		}
	})
}

// ContinueResponse is response.create: resume every backend response whose
// function results are all in, and start on any typed input that is queued.
func (e *Engine) ContinueResponse(clientEventID string) {
	e.post(func() {
		var ready, missing []string
		for _, id := range e.pendingOrder {
			if p := e.pending[id]; p.complete() {
				ready = append(ready, id)
			} else {
				missing = append(missing, p.missing()...)
			}
		}
		for _, id := range ready {
			p := e.pending[id]
			e.dropPending(id)
			e.continuations = append(e.continuations, p)
		}
		if len(ready) == 0 && len(e.typed) == 0 && len(missing) > 0 {
			e.deps.Emit(live.NewError("invalid_request_error", "tool_outputs_missing",
				"response.create needs a function_call_output for every pending call first; still waiting on "+
					strings.Join(missing, ", "), clientEventID))
			return
		}
		if len(e.typed) > 0 {
			e.startTypedTurn()
		}
		e.drainDeferred()
	})
}

// startTypedTurn answers text the user typed. It takes the floor the way a
// spoken question would.
func (e *Engine) startTypedTurn() {
	text := strings.Join(e.typed, "\n")
	e.typed = nil
	if e.assistantHasFloor() {
		e.yieldFloor(false)
	}
	turn := e.tracker.Begin(text, false)
	e.specTurn = nil
	e.commitTurn(turn)
	e.beginGeneration(turn, "typed_input", false)
}

// startContinuation resumes a backend response with its function results.
func (e *Engine) startContinuation(p *pendingResponse) {
	// The calls and their results enter history together, in the order the
	// model made them.
	e.history = append(e.history, provider.Message{Role: provider.RoleAssistant, ToolCalls: p.calls})
	for _, c := range p.calls {
		e.history = append(e.history, provider.Message{
			Role:       provider.RoleTool,
			Name:       c.Name,
			ToolCallID: c.ID,
			Content:    p.outputs[c.ID],
		})
	}
	e.trimHistory()

	turn := e.tracker.Begin("", false)
	turn.DelegationID = p.delegationID
	e.specTurn = nil
	e.commitTurn(turn)
	e.log.Debug("think: resuming the backend with function results",
		"delegation", p.delegationID, "turn", turn.ID, "calls", len(p.calls),
		"waited_ms", time.Since(p.createdAt).Milliseconds())
	e.beginGeneration(turn, "tool_result", false)
}

// --- function calls: the conversational model delegating ---

func (e *Engine) onDelegateCalls(job backendJob, calls []provider.ToolCall) {
	e.ensureUserRecorded(job.turn)
	n := 0
	for _, call := range calls {
		if call.Name != delegateToolName {
			e.log.Warn("think: the conversational model called a function it was not given",
				"name", call.Name)
			continue
		}
		var args struct {
			Request string `json:"request"`
		}
		_ = json.Unmarshal([]byte(call.Arguments), &args)

		id := job.delegationID
		if n > 0 {
			id = fmt.Sprintf("%s.%d", job.delegationID, n)
		}
		n++
		e.announceClientDelegation(job.turn, id, "model", strings.TrimSpace(args.Request))
		// Closed in history at once, so the conversation stays well formed
		// whether or not the application ever answers.
		e.history = append(e.history,
			provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
			provider.Message{
				Role:       provider.RoleTool,
				Name:       call.Name,
				ToolCallID: call.ID,
				Content: "Delegated to the application as " + id +
					". Its result has not arrived yet and will be spoken when it does; do not guess it.",
			})
	}
	if n > 0 {
		e.trimHistory()
		// The wait the holding filler covers starts now, not when the caller
		// stopped talking.
		e.delegatedAt = time.Now()
		e.delegatedTurn = job.turn
		e.filled = false
	}
}

// --- delegation ids ---

func (e *Engine) registerDelegation(id string) {
	if e.delegations[id] {
		return
	}
	e.delegations[id] = true
	e.delegationOrder = append(e.delegationOrder, id)
	for len(e.delegationOrder) > 256 {
		delete(e.delegations, e.delegationOrder[0])
		e.delegationOrder = e.delegationOrder[1:]
	}
}

func (e *Engine) forgetDelegation(id string) {
	// Anything an application already sent for it is withdrawn with it. An
	// application that acts on the early hint can answer a guess before the
	// guess is abandoned, and that answer is to a question nobody asked.
	if id != "" && len(e.commentary) > 0 {
		kept := e.commentary[:0]
		for _, c := range e.commentary {
			if c.delegationID != id {
				kept = append(kept, c)
			}
		}
		e.commentary = kept
	}
	if !e.delegations[id] {
		return
	}
	delete(e.delegations, id)
	for i, v := range e.delegationOrder {
		if v == id {
			e.delegationOrder = append(e.delegationOrder[:i], e.delegationOrder[i+1:]...)
			break
		}
	}
}

// checkDelegation rejects an append that names a delegation this session never
// announced. Null — session-wide — is always fine.
func (e *Engine) checkDelegation(id, clientEventID string) bool {
	if id == "" || e.delegations[id] {
		return true
	}
	e.deps.Emit(live.NewError("invalid_request_error", "unknown_delegation_id",
		fmt.Sprintf("delegation_id %q is not a delegation this session announced; use null for session-wide context",
			id), clientEventID))
	return false
}

func (e *Engine) ack(kind, delegationID, clientEventID string) {
	at := e.sessionMS()
	e.deps.Emit(live.AckEvent{
		Envelope:      live.Envelope{Type: kind},
		ClientEventID: clientEventID,
		DelegationID:  delegationID,
		StartMS:       &at,
		EndMS:         &at,
	})
}

// --- the application talking back ---

// AppendInstructions adds trusted system-level guidance mid-session.
func (e *Engine) AppendInstructions(delegationID, content, clientEventID string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	e.post(func() {
		if !e.checkDelegation(delegationID, clientEventID) {
			return
		}
		// Each append is bounded by the session; this bounds the sum, which is
		// what the backend is sent on every turn from now on.
		limit := e.opts.Cfg.Duplex.MaxAppendChars * maxInstructionAppends
		n := len([]rune(content))
		if limit > 0 && e.appendedChars+n > limit {
			e.deps.Emit(live.NewError("invalid_request_error", "content_too_long",
				fmt.Sprintf("session.instructions.append has added %d characters to this session and "+
					"the limit is %d; they are sent with every request to the backend",
					e.appendedChars, limit), clientEventID))
			return
		}
		e.appendedChars += n
		e.instrMu.Lock()
		if e.instr == "" {
			e.instr = content
		} else {
			e.instr += "\n" + content
		}
		e.instrMu.Unlock()
		e.noteContext()
		e.ack(live.ServerInstructionsAppended, delegationID, clientEventID)
	})
}

// AppendThinking records backend context that must inform the conversation but
// must not be spoken. This is the quiet half of client delegation: progress an
// application knows about, injected without taking the floor.
func (e *Engine) AppendThinking(delegationID, content, clientEventID string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	e.post(func() {
		if !e.checkDelegation(delegationID, clientEventID) {
			return
		}
		e.history = append(e.history, provider.Message{
			Role:    provider.RoleSystem,
			Content: "[backend] " + content,
		})
		e.trimHistory()
		e.ack(live.ServerThinkingAppended, delegationID, clientEventID)
	})
}

// AppendCommentary hands the engine text to speak. In client-delegation mode
// this is how an application's answer reaches the user.
//
// It is acknowledged at once and spoken when it can be: not over the caller,
// and not through the middle of something the assistant is already saying.
// Two updates sent close together are said one after the other, in order — the
// second used to cut the first off mid-word.
func (e *Engine) AppendCommentary(delegationID, content, clientEventID string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	e.post(func() {
		if !e.checkDelegation(delegationID, clientEventID) {
			return
		}
		if len(e.commentary) >= maxQueuedItems {
			e.deps.Emit(live.NewError("invalid_request_error", "commentary_queue_full",
				fmt.Sprintf("%d commentary appends are already waiting to be spoken; send the next "+
					"when session.output_transcript.delta shows them going out", maxQueuedItems), clientEventID))
			return
		}
		e.commentary = append(e.commentary, queuedCommentary{delegationID: delegationID, text: content})
		e.ack(live.ServerCommentaryAppended, delegationID, clientEventID)
		e.drainDeferred()
	})
}

// wake asks the loop to look again at whatever is waiting for the floor. It
// never blocks: the tick is the fallback.
func (e *Engine) wake() { e.post(e.drainDeferred) }

// drainDeferred starts the next thing that has been waiting for a quiet caller
// and a free floor: a function result to read back, then something an
// application asked to have said.
func (e *Engine) drainDeferred() {
	if len(e.continuations) == 0 && len(e.commentary) == 0 {
		return
	}
	if e.userOpen || e.awaitingFinal() || e.assistantHasFloor() {
		return
	}
	if len(e.continuations) > 0 {
		p := e.continuations[0]
		e.continuations = e.continuations[1:]
		e.startContinuation(p)
		return
	}
	text := e.commentary[0].text
	e.commentary = e.commentary[1:]

	turn := e.tracker.Current()
	if turn == nil || !e.tracker.IsCurrent(turn) || !turn.Committed() {
		// Unsolicited commentary (a push notification, say) still deserves
		// a turn of its own so truncation accounting works.
		turn = e.tracker.Begin("", false)
		e.specTurn = nil
		e.commitTurn(turn)
	}
	pipe := e.newSpeechPipe(turn, false)
	pipe.Push(text)
	pipe.Close()
	turn.AppendResponse(text)
}

// SetBackend replaces the session's delegation.responses settings for
// subsequent generations.
func (e *Engine) SetBackend(b BackendOptions) {
	b.Tools = append([]json.RawMessage(nil), b.Tools...)
	e.post(func() { e.backend = b })
}

// --- warming the provider's prefix cache ---

// maybeWarmPrefix sends the conversation so far to the backend with a
// one-token limit as the caller starts to speak, when the profile asks for it.
//
// A backend's time to first token grows with the prompt, and on a long persona
// prompt it is the largest number in the whole cascade. Providers that cache a
// prompt prefix skip the part they have seen — but only once it is in the
// cache, and the request that puts it there is the one the caller is waiting
// on. This puts it there a sentence earlier, with a request nobody waits on.
//
// It costs the prompt's tokens on every utterance and returns nothing a
// provider without such a cache can use, which is why it is off unless asked
// for. See duplex.backend_prefix_warm.
func (e *Engine) maybeWarmPrefix() {
	if !e.opts.Cfg.Duplex.BackendPrefixWarm || e.deps.LLM == nil {
		return
	}
	if e.clientMode() && !e.clientAuto() {
		return // no model call is coming
	}
	if time.Since(e.lastWarm) < 3*time.Second {
		return
	}
	e.lastWarm = time.Now()
	front := e.clientAuto()
	req := e.backendRequest(e.snapshotMessages(nil, front), front)
	req.MaxTokens = 1
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
		defer cancel()
		started := time.Now()
		deltas, err := e.deps.LLM.Stream(ctx, req)
		if err != nil {
			e.log.Debug("think: prefix warm failed", "err", err)
			return
		}
		for d := range deltas {
			if d.Usage != nil {
				e.inTok.Add(int64(d.Usage.InputTokens))
				e.outTok.Add(int64(d.Usage.OutputTokens))
				e.cachedTok.Add(int64(d.Usage.CachedInputTokens))
			}
		}
		e.log.Debug("think: prefix warmed", "ms", time.Since(started).Milliseconds())
	}()
}
