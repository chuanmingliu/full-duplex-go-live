package duplex

import (
	"context"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

func TestSimulatorFarewellKeepsTranscriptsWithoutAnotherReply(t *testing.T) {
	for _, ending := range []bool{true, false} {
		t.Run(map[bool]string{true: "farewell", false: "ordinary_script"}[ending], func(t *testing.T) {
			e, events := simulatorEngine(t, true)
			due := time.Now().Add(time.Hour)
			request := simulatorReservation(t, e, "script", due)
			request.EndConversation = ending
			if err := e.ScheduleSimulatorSpeech(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			// The first reply arrives during the farewell; the second arrives
			// after it. Both must remain visible as recognized input.
			simulatorLoop(t, e, func() {
				e.advanceSimulator(due)
				e.asrRun = &asrRun{id: 1, itemID: "during-script", openedAt: time.Now()}
				e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Text: "Synthetic closing reply.", Final: true}})
				e.asrRun = nil
			})
			waitFor(t, "script completion", time.Second, func() bool { return simulatorStatus(events, "script", "completed") })
			simulatorLoop(t, e, func() {
				e.asrRun = &asrRun{id: 2, itemID: "after-script", openedAt: time.Now()}
				e.onASREvent(asrEvent{run: 2, res: provider.ASRResult{Text: "Another synthetic closing reply.", Final: true}})
				e.asrRun = nil
			})
			if got := events.count(live.ServerInputTranscriptDelta); got != 2 {
				t.Fatalf("closing replies lost transcript evidence: got %d", got)
			}
			want := 2
			if ending {
				want = 0
			}
			if got := events.count(live.ServerDelegationCreated); got != want {
				t.Fatalf("generated replies after script: got %d, want %d", got, want)
			}
			if events.audioMS() == 0 {
				t.Fatal("farewell itself was suppressed")
			}
		})
	}
}
