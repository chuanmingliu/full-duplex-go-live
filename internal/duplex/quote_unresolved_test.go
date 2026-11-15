package duplex

import (
	"strings"
	"testing"
	"time"
)

func TestInterruptedMedicalChangeMustBeReconciledBeforeConfirmation(t *testing.T) {
	e := confirmationEngine()
	e.prepareQuoteRevision(&Turn{ID: "base", Transcript: "三者四百万"}, revisionCall(`{"config":{"三者险":"400万"}}`))
	original := e.tools.quoteRevision.ID
	interrupted := &Turn{ID: "medical", Transcript: "三者的医保外调的调成最高吧。", committed: true}
	e.retainQuoteRequest(interrupted)
	e.retainQuoteRequest(interrupted)
	if len(e.tools.unresolvedQuoteRequests) != 1 || !strings.Contains(e.quoteConfirmationPrompt(), interrupted.Transcript) {
		t.Fatal("request lost or duplicated")
	}
	e.history = nil // history trimming cannot erase unprocessed business changes
	_, reply := e.prepareQuoteRevision(&Turn{ID: "approval", Transcript: "就这样算"}, revisionCall(confirmationArgs(original, "confirm")))
	if !strings.Contains(reply, "unresolved_changes") {
		t.Fatal(reply)
	}
	_, reply = e.prepareQuoteRevision(&Turn{ID: "repair", Transcript: "把刚才医保外也加上"}, revisionCall(`{"config":{"医保外用药责任险-三者":"400万"},"resolved_request_ids":["medical"]}`))
	if !strings.Contains(reply, "awaiting_confirmation") || len(e.tools.unresolvedQuoteRequests) != 0 {
		t.Fatal(reply)
	}
	pending := e.tools.quoteRevision
	if pending.Config["三者险"] != "400万" || pending.Config["医保外用药责任险-三者"] != "400万" {
		t.Fatalf("%+v", pending)
	}
	resolved, reply := e.prepareQuoteRevision(&Turn{ID: "approved", Transcript: "没有其他了，算吧"}, revisionCall(confirmationArgs(pending.ID, "confirm")))
	if reply != "" || strings.Contains(resolved.Arguments, "resolved_request_ids") || !strings.Contains(resolved.Arguments, "医保外用药责任险-三者") {
		t.Fatal(resolved, reply)
	}
}

func TestInterruptedQuoteRequestResolutionBoundaries(t *testing.T) {
	e := confirmationEngine()
	for _, turn := range []*Turn{
		{ID: "guess", Transcript: "三者的医保外调成最高"},
		{ID: "question", Transcript: "座位还能调高吗？", committed: true},
		{ID: "saved", Transcript: "三者的医保外调成最高", committed: true, quoteRevisionHandled: true},
	} {
		e.retainQuoteRequest(turn)
	}
	if len(e.tools.unresolvedQuoteRequests) != 0 {
		t.Fatal("invented an unprocessed change")
	}
	e.prepareQuoteRevision(&Turn{ID: "base", Transcript: "三者四百万"}, revisionCall(`{"config":{"三者险":"400万"}}`))
	e.retainQuoteRequest(&Turn{ID: "medical", Transcript: "三者医保外调成最高", committed: true})
	_, reply := e.prepareQuoteRevision(&Turn{ID: "wrong"}, revisionCall(`{"config":{"三者险":"300万"},"resolved_request_ids":["missing"]}`))
	if !strings.Contains(reply, "invalid") || len(e.tools.unresolvedQuoteRequests) != 1 {
		t.Fatal(reply)
	}
	e.prepareQuoteRevision(&Turn{ID: "other"}, revisionCall(`{"config":{"司机责任险":"5万"}}`))
	if len(e.tools.unresolvedQuoteRequests) != 1 {
		t.Fatal("unrelated stage erased request")
	}
	_, reply = e.prepareQuoteRevision(&Turn{ID: "cancel", Transcript: "全部取消"}, revisionCall(confirmationArgs(e.tools.quoteRevision.ID, "cancel")))
	if !strings.Contains(reply, "cancelled") || len(e.tools.unresolvedQuoteRequests) != 0 {
		t.Fatal(reply)
	}
}

func TestInterruptedRequestCanBeCancelledWithoutPendingRevision(t *testing.T) {
	e := confirmationEngine()
	before, _ := e.memorySnapshot()
	e.retainQuoteRequest(&Turn{ID: "medical", Transcript: "三者医保外调成最高", committed: true})
	_, reply := e.prepareQuoteRevision(&Turn{ID: "cancel", Transcript: "刚才医保外不改了"}, revisionCall(`{"revision_action":"cancel","resolved_request_ids":["medical"]}`))
	after, _ := e.memorySnapshot()
	if !strings.Contains(reply, "cancelled") || len(e.tools.unresolvedQuoteRequests) != 0 || string(before) != string(after) {
		t.Fatal(reply)
	}
}

func TestInterruptedQuoteCapturedThroughInterruptionEntryPoints(t *testing.T) {
	for _, soft := range []bool{false, true} {
		e := confirmationEngine()
		e.opts.Cfg = testConfig()
		e.opts.Cfg.Duplex.ResetTTSOnInterrupt = false
		e.tracker = NewTracker(&e.gen)
		e.player = NewPlayer(PlayerConfig{}, func(string, []byte, time.Time) {})
		turn := e.tracker.Begin("三者医保外调成最高", false)
		turn.MarkTranscriptFinal()
		turn.commit(e.deps.Emit)
		if soft {
			e.softInterrupt()
		} else {
			e.interrupt()
		}
		if len(e.tools.unresolvedQuoteRequests) != 1 || e.tools.unresolvedQuoteRequests[0].ID != turn.ID {
			t.Fatalf("soft=%v request not retained", soft)
		}
	}
}

func TestInterruptedRequestBlocksUnchangedPricingWithoutPendingRevision(t *testing.T) {
	for _, args := range []string{`{}`, `{"config":{"三者险":"200万"}}`} {
		e := confirmationEngine()
		e.retainQuoteRequest(&Turn{ID: "medical", Transcript: "三者医保外调成最高", committed: true})
		_, reply := e.prepareQuoteRevision(&Turn{ID: "retry", Transcript: "算吧"}, revisionCall(args))
		if !strings.Contains(reply, "unresolved_changes") || len(e.tools.unresolvedQuoteRequests) != 1 {
			t.Fatal(reply)
		}
	}
}

func TestResolvedUnchangedRequestUsesExistingQuote(t *testing.T) {
	e := confirmationEngine()
	e.retainQuoteRequest(&Turn{ID: "same", Transcript: "三者改成两百万", committed: true})
	_, reply := e.prepareQuoteRevision(&Turn{ID: "resolve"}, revisionCall(`{"config":{"三者险":"200万"},"resolved_request_ids":["same"]}`))
	if !strings.Contains(reply, "unchanged") || len(e.tools.unresolvedQuoteRequests) != 0 {
		t.Fatal(reply)
	}
}

func TestInterruptedRiderCannotBeResolvedByMainCoverageOnly(t *testing.T) {
	for _, args := range []string{
		`{"config":{"三者险":"400万"},"resolved_request_ids":["medical"]}`,
		`{"configs":[{"医保外用药责任险-三者":"400万"},{"三者险":"400万"}],"resolved_request_ids":["medical"]}`,
	} {
		e := confirmationEngine()
		e.retainQuoteRequest(&Turn{ID: "medical", Transcript: "三者的医保外调成最高", committed: true})
		_, reply := e.prepareQuoteRevision(&Turn{ID: "wrong"}, revisionCall(args))
		if !strings.Contains(reply, "unresolved_changes") || len(e.tools.unresolvedQuoteRequests) != 1 || e.tools.quoteRevision != nil {
			t.Fatal(reply)
		}
	}
	for _, text := range []string{"三者的医保外调成最高", "三者医保外调成最高", "医保外用药责任险-三者调成最高"} {
		names := interruptedCoverageNames(text)
		if len(names) != 1 || names[0] != "医保外用药责任险-三者" {
			t.Fatal(text, names)
		}
	}
}

func TestCommittedMedicalRequestSurvivesCompletedUnstagedAnswer(t *testing.T) {
	e := confirmationEngine()
	e.recordedUser = map[string]bool{}
	e.prepareQuoteRevision(&Turn{ID: "base"}, revisionCall(`{"config":{"三者险":"400万"}}`))
	medical := &Turn{ID: "medical", Transcript: "三者的医保外调成最高吧。"}
	e.commitTurn(medical)
	// A natural answer finishing without a tool call must not erase this demand.
	medical.markCompletedNaturally()
	e.commitTurn(&Turn{ID: "seat", Transcript: "座位还能调高吗？"})
	_, reply := e.prepareQuoteRevision(&Turn{ID: "approval", Transcript: "就按两个四百万算吧。"}, revisionCall(confirmationArgs(e.tools.quoteRevision.ID, "confirm")))
	if !strings.Contains(reply, "unresolved_changes") || len(e.tools.unresolvedQuoteRequests) != 1 {
		t.Fatal(reply)
	}
	_, reply = e.prepareQuoteRevision(&Turn{ID: "reconcile"}, revisionCall(`{"config":{"医保外用药责任险-三者":"400万"},"resolved_request_ids":["medical"]}`))
	if !strings.Contains(reply, "awaiting_confirmation") || len(e.tools.unresolvedQuoteRequests) != 0 {
		t.Fatal(reply)
	}
}

func TestPendingRevisionOverridesOlderReadoutInstructions(t *testing.T) {
	e := confirmationEngine()
	e.opts.Cfg = testConfig()
	e.opts.Cfg.Duplex.QuoteInterruptionRecovery = true
	e.tools.quoteDelivery = &quoteDelivery{task: e.tools.lastResult, status: "complete"}
	e.prepareQuoteRevision(&Turn{ID: "stage"}, revisionCall(`{"configs":[{"三者险":"300万"},{"三者险":"400万"}]}`))
	prompt := e.quoteRecoveryPrompt(&Turn{ID: "confirmation", Transcript: "你算吧，我等着。"})
	if !strings.HasSuffix(prompt, e.quoteRevisionRecoveryNote(&Turn{ID: "confirmation", Transcript: "你算吧，我等着。"})) || !strings.Contains(prompt, "revision_action=confirm") || !strings.Contains(prompt, "尚未提交计算") {
		t.Fatal("old readout obscures pending pricing state", prompt)
	}
	for _, turn := range []*Turn{nil, {ID: "tool-result"}, {ID: "stage", Transcript: "三者改三百万"}} {
		if note := e.quoteRevisionRecoveryNote(turn); !strings.Contains(note, "当前禁止再次登记或调用 confirm") {
			t.Fatal("staging continuation invited same-turn confirmation", note)
		}
	}
	e.tools.quoteRevision = nil
	if e.quoteRevisionRecoveryNote(nil) != "" {
		t.Fatal("confirmation note survived the pending request")
	}
}
