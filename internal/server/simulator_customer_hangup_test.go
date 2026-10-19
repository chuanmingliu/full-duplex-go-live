package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/simulator"
)

func TestSimulatorPrivateGoalToolPreparesFarewellAndHangsUp(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "default", false: "opt_out"}[enabled], func(t *testing.T) {
			scenario := simulator.Scenario{SchemaVersion: 1, ID: "goal-customer", Language: "en", Persona: "A synthetic customer.", Greeting: "Hello.", Goal: "Complete the synthetic task.", MaxDurationMS: 30000, MaxRemoteTurns: 10}
			if !enabled {
				scenario.HangupAfterGoal = &enabled
			}
			ts, receiver, controller, snap := simulatorScenarioHarness(t, scenario)
			type engineCapture struct {
				options duplex.Options
				emit    func(any)
			}
			captures := make(chan engineCapture, 1)
			receiver.Factory = func(options duplex.Options, deps duplex.Deps) *duplex.Engine {
				captures <- engineCapture{options, deps.Emit}
				return duplex.New(options, deps)
			}
			client := dialRealtime(t, ts, simulatorPath(snap))
			client.send(map[string]any{"type": "session.update", "session": map[string]any{"tools": []any{map[string]any{"type": "function", "name": "injected_tool"}}}})
			client.first("session.updated", time.Second)
			capture := <-captures
			tools, _ := json.Marshal(capture.options.Backend.Tools)
			if strings.Contains(string(tools), "injected_tool") || strings.Contains(string(tools), simulator.CustomerGoalToolName) != enabled {
				t.Fatalf("unexpected customer tool configuration: %s", tools)
			}
			bound, _ := controller.Get(context.Background(), snap.RunID)
			if err := controller.Handle(context.Background(), simulator.Event{Kind: "answered", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
				t.Fatal(err)
			}
			client.first("response.audio.done", 3*time.Second)
			capture.emit(live.ResponseEventEnvelope{Event: json.RawMessage(`{"type":"response.output_item.done","item":{"type":"function_call","name":"complete_customer_goal"}}`)})
			if enabled {
				await(t, "farewell then customer hangup", 7*time.Second, func() bool {
					current, _ := controller.Get(context.Background(), snap.RunID)
					return current.Phase == simulator.Ending
				})
			} else {
				current, _ := controller.Get(context.Background(), snap.RunID)
				if current.Phase != simulator.Connected {
					t.Fatalf("disabled goal signal ended call: %+v", current)
				}
			}
			if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
				t.Fatal(err)
			}
			report, err := controller.Report(context.Background(), snap.RunID)
			if err != nil {
				t.Fatal(err)
			}
			farewellCompleted := false
			for _, outcome := range report.Actions {
				if outcome.ID == simulator.CustomerFarewellActionID && outcome.Result == "completed" {
					farewellCompleted = true
				}
			}
			if farewellCompleted != enabled || enabled && report.TerminationReason != "customer_hangup" {
				t.Fatalf("private goal did not complete farewell and hangup: %+v", report)
			}
		})
	}
}
