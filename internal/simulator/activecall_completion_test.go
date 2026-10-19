package simulator

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestActiveCallRemoteHangupCompletesReport(t *testing.T) {
	for _, ending := range []string{"native-bye", "normal-close", "empty-close", "sip-track-end", "track-end-only", "customer-native-bye", "customer-normal-close"} {
		t.Run(ending, func(t *testing.T) {
			h := newActiveCallHarness(t)
			clock := &fakeClock{now: time.Now()}
			c, err := NewController(Options{Telephony: h.adapter, Voice: &fakePorts{}, Token: "test-token", Destination: "simulator-receiver", GatewayIdentity: "isolated-active-call", Clock: clock, DataDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			h.controller.onIncoming = func(in IncomingCall) error { return c.Incoming(ctx, in) }
			h.controller.onEvent = func(event Event) error { return c.Handle(ctx, event) }
			scenario := sampleScenario()
			snap, err := c.Prepare(ctx, scenario)
			if err != nil {
				t.Fatal(err)
			}
			const dialog = "completed-dialog"
			if h.webhook(dialog, "sip:"+snap.RunID+"@simulator.invalid").Code != http.StatusNoContent {
				t.Fatal("invite failed")
			}
			conn := activeCallReceive(t, h.attached)
			clock.advance(time.Duration(scenario.AnswerDelayMS) * time.Millisecond)
			if command := activeCallReceive(t, h.commands); command["command"] != "accept" {
				t.Fatal("missing accept")
			}
			if err = conn.WriteJSON(activeCallEvent{Event: "answer", TrackID: dialog}); err != nil {
				t.Fatal(err)
			}
			answer := activeCallReceive(t, h.controller.events)
			b := answer.Binding
			b.SessionID = "synthetic-session"
			if err = c.Handle(ctx, Event{Kind: "voice_ready", Binding: b}); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"customer_speech_started", "customer_speech_ended", "remote_speech_started", "remote_speech_ended"} {
				clock.advance(50 * time.Millisecond)
				if err = c.Observe(ctx, b, Observation{Kind: kind, AtMS: c.RelativeMS()}); err != nil {
					t.Fatal(err)
				}
			}
			wantReason := "remote_hangup"
			if strings.HasPrefix(ending, "customer-") {
				wantReason = "customer_hangup"
				if err = c.Handle(ctx, Event{Kind: "stop", Binding: b, Reason: wantReason}); err != nil {
					t.Fatal(err)
				}
				if command := activeCallReceive(t, h.commands); command["command"] != "hangup" {
					t.Fatal("customer stop did not request gateway BYE")
				}
			}
			switch strings.TrimPrefix(ending, "customer-") {
			case "native-bye":
				initiator, reason := "caller", "ByCaller"
				if wantReason == "customer_hangup" {
					initiator, reason = "callee", "ByCallee"
				}
				err = conn.WriteJSON(activeCallEvent{Event: "hangup", TrackID: dialog, Initiator: initiator, Reason: reason})
			case "normal-close":
				if err = conn.WriteJSON(map[string]any{"event": "trackEnd", "trackId": dialog}); err != nil {
					t.Fatal(err)
				}
				err = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			case "empty-close":
				if err = conn.WriteJSON(map[string]any{"event": "trackEnd", "trackId": dialog}); err != nil {
					t.Fatal(err)
				}
				err = conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(time.Second))
			case "sip-track-end", "track-end-only":
				err = conn.WriteJSON(map[string]any{"event": "trackEnd", "trackId": dialog})
			}
			if err != nil {
				t.Fatal(err)
			}
			if ending != "track-end-only" {
				_ = conn.Close()
			}
			if event := activeCallReceive(t, h.controller.events); event.Kind != "ended" {
				t.Fatalf("normal far-end termination became %s: %s", event.Kind, event.Reason)
			}
			clock.advance(time.Duration(scenario.IdleTimeoutMS)*time.Millisecond + c.opts.SetupTimeout)
			report, err := c.Report(ctx, snap.RunID)
			if err != nil || report.Phase != Completed || report.TerminationReason != wantReason || len(report.Failures) != 0 {
				t.Fatalf("normal BYE lost terminal report: %+v %v", report, err)
			}
			if len(report.AssistantGaps) != 1 || !report.AssistantGaps[0].Valid || report.AssistantGaps[0].ValueMS != 50 {
				t.Fatalf("normal BYE invalidated response gaps: %+v", report.AssistantGaps)
			}
			for _, name := range []string{"answered_once", "greeting_once", "call_limits", "clean_termination"} {
				if reportChecks(report)[name] != "pass" {
					t.Fatalf("normal BYE check %s: %+v", name, report.Checks)
				}
			}
			assertArtifactMatches(t, c, snap.RunID)
			restored, err := NewController(c.opts)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := restored.Report(ctx, snap.RunID)
			if err != nil || saved.Phase != Completed || saved.TerminationReason != wantReason || !saved.AssistantGaps[0].Valid {
				t.Fatalf("completed report lost on restart: %+v %v", saved, err)
			}
		})
	}
}

func TestActiveCallUnprovenControlCloseStillFails(t *testing.T) {
	for _, variant := range []string{"abrupt", "normal-close", "empty-close", "going-away", "before-answer", "foreign-track", "playback-track", "empty-playback-track", "error-before-close"} {
		t.Run(variant, func(t *testing.T) {
			h := newActiveCallHarness(t)
			const dialog = "unconfirmed-dialog"
			if h.webhook(dialog, "sip:"+activeCallTestRun+"@simulator.invalid").Code != http.StatusNoContent {
				t.Fatal("invite failed")
			}
			conn := activeCallReceive(t, h.attached)
			if variant != "before-answer" {
				if err := h.adapter.Accept(context.Background(), h.controller.calls()[0].CallID); err != nil {
					t.Fatal(err)
				}
				_ = activeCallReceive(t, h.commands)
				_ = conn.WriteJSON(activeCallEvent{Event: "answer", TrackID: dialog})
				_ = activeCallReceive(t, h.controller.events)
			}
			switch variant {
			case "normal-close":
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			case "empty-close":
				_ = conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(time.Second))
			case "going-away":
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, ""), time.Now().Add(time.Second))
			case "before-answer":
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			case "foreign-track":
				_ = conn.WriteJSON(map[string]any{"event": "trackEnd", "trackId": "foreign"})
			case "playback-track":
				_ = conn.WriteJSON(map[string]any{"event": "trackEnd", "trackId": dialog, "playId": "playback"})
			case "empty-playback-track":
				_ = conn.WriteJSON(map[string]any{"event": "trackEnd", "trackId": dialog, "playId": ""})
			case "error-before-close":
				_ = conn.WriteJSON(map[string]any{"event": "trackEnd", "trackId": dialog})
				_ = conn.WriteJSON(activeCallEvent{Event: "error", TrackID: dialog})
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			}
			_ = conn.Close()
			if event := activeCallReceive(t, h.controller.events); event.Kind != "failed" {
				t.Fatalf("unproven termination reported success: %+v", event)
			}
		})
	}
}
