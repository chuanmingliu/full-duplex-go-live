package journal

import (
	"strings"
	"testing"
)

func TestLaterSystemOpeningPreservesCallGreeting(t *testing.T) {
	ix := NewIndexer()
	err := ix.ReadLog(strings.NewReader(`time=2026-11-01T16:00:00+08:00 level=INFO msg="session started" session=sess_greeting call=call_greeting realtime=true
time=2026-11-01T16:00:01+08:00 level=INFO msg="speak: opening line" session=sess_greeting turn=item_open text="Initial greeting"
time=2026-11-01T16:00:10+08:00 level=INFO msg="speak: opening line" session=sess_greeting turn=item_farewell text="Goodbye"
`))
	if err != nil {
		t.Fatal(err)
	}
	calls := ix.Calls()
	if len(calls) != 1 || calls[0].Greeting != "Initial greeting" {
		t.Fatalf("later system utterance replaced the greeting: %+v", calls)
	}
	// Both system utterances remain classified outside caller-response SLA.
	for _, id := range []string{"item_open", "item_farewell"} {
		if !ix.byCall["call_greeting"].turn(id).outputNonanswer {
			t.Errorf("%s lost its non-answer classification", id)
		}
	}
}
