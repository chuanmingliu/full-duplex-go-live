package journal

import (
	"strings"
	"testing"
)

func TestQuotationRecoveryTrace(t *testing.T) {
	ix := NewIndexer()
	err := ix.ReadLog(strings.NewReader(`time=2026-11-14T12:00:00+08:00 level=INFO msg="session started" session=s-recovery call=call-recovery realtime=true
time=2026-11-14T12:00:01+08:00 level=INFO msg="quote: delivery progress" session=s-recovery task=q-1 turn=t-2 status=interrupted reason=playback memory_version=3 completed="[1]" remaining="[2 3]" interruptions=1
 `))
	if err != nil {
		t.Fatal(err)
	}
	calls := ix.Calls()
	if len(calls) != 1 || len(calls[0].Quotation) != 1 {
		t.Fatal("missing recovery trace")
	}
	q := calls[0].Quotation[0]
	if q.Kind != "recovery" || q.Task != "q-1" || q.Turn != "t-2" || q.Status != "interrupted" || q.Completed != "[1]" || q.Remaining != "[2 3]" || q.Interruptions != 1 || q.Version != 3 {
		t.Fatalf("incomplete trace: %+v", q)
	}
}
