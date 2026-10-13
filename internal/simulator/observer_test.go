package simulator

import (
	"testing"
)

func TestAssistantGapUsesCustomerEnd(t *testing.T) {
	g := AssistantGap(BoundaryTurn{CustomerEndMS: 1000, AssistantStartMS: 1800, HasCustomerEnd: true, HasAssistantStart: true})
	if !g.Valid || g.ValueMS != 800 {
		t.Fatal(g)
	}
	for _, v := range []BoundaryTurn{{HasCustomerEnd: true}, {HasCustomerEnd: true, HasAssistantStart: true, CustomerEndMS: 100, AssistantStartMS: 50}, {HasCustomerEnd: true, HasAssistantStart: true, Overlapped: true}, {HasCustomerEnd: true, HasAssistantStart: true, Failed: true}} {
		g := AssistantGap(v)
		if g.Valid || g.Reason == "" {
			t.Fatal(g)
		}
	}
}
func TestObserverSilenceDuplicatesMissing(t *testing.T) {
	o := newObserver()
	o.observe(Observation{ID: "c", Kind: "customer_audio", AtMS: 10, NonSilent: true})
	o.observe(Observation{ID: "e", Kind: "customer_audio", AtMS: 100, End: true})
	o.observe(Observation{ID: "silence", Kind: "assistant_audio", AtMS: 150})
	if len(o.assistantGaps()) != 1 || o.assistantGaps()[0].Valid {
		t.Fatal("silence became response")
	}
	o.observe(Observation{ID: "a", Kind: "assistant_audio", AtMS: 300, NonSilent: true})
	o.observe(Observation{ID: "a", Kind: "assistant_audio", AtMS: 300, NonSilent: true})
	if len(o.assistantGaps()) != 1 || o.assistantGaps()[0].ValueMS != 200 {
		t.Fatal(o.assistantGaps())
	}
	if o.observe(Observation{Kind: "assistant_audio", AtMS: 299}) == nil {
		t.Fatal("backwards timestamp")
	}
}
