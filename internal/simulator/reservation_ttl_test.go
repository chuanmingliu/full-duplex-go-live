package simulator

import (
	"context"
	"testing"
	"time"
)

func TestPreparedReservationRemainsAvailableForThirtyMinutes(t *testing.T) {
	ctx := context.Background()
	c, clock, _ := testController(t)
	preparedAt := clock.Now()
	snap, err := c.Prepare(ctx, sampleScenario())
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.ReservationExpiresAt.Sub(preparedAt); got < 30*time.Minute {
		t.Fatalf("reservation TTL = %s, want at least thirty minutes", got)
	}
	clock.advance(30*time.Minute - time.Millisecond)
	current, err := c.Get(ctx, snap.RunID)
	if err != nil || current.Phase != Waiting {
		t.Fatalf("prepared reservation expired too soon: %+v %v", current, err)
	}
	clock.advance(snap.ReservationExpiresAt.Sub(clock.Now()))
	current, err = c.Get(ctx, snap.RunID)
	if err != nil || current.Phase != Expired {
		t.Fatalf("reservation did not expire at its deadline: %+v %v", current, err)
	}
}

func TestControllerAcceptsLongReservationTTL(t *testing.T) {
	for _, ttl := range []time.Duration{30 * time.Minute, time.Hour} {
		t.Run(ttl.String(), func(t *testing.T) {
			ports := &fakePorts{}
			c, err := NewController(Options{Telephony: ports, Voice: ports, Token: "test-token", Destination: "receiver", GatewayIdentity: "fixture", ReservationTTL: ttl})
			if err != nil {
				t.Fatal(err)
			}
			if c.opts.ReservationTTL != ttl {
				t.Fatalf("configured reservation TTL changed: %s", c.opts.ReservationTTL)
			}
		})
	}
}
