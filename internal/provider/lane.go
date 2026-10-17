package provider

import (
	"context"
	"sync/atomic"
)

type laneKey struct{}

// WithLane asks a provider that keeps a pool — of credentials, of connections —
// to serve this request from the given place in it. Requests with different
// lanes share nothing the provider can keep apart; requests with the same lane
// land on the same account and the same connection every time.
//
// It is what makes copies of one request worth sending. Four copies down one
// connection, on one account, are four streams that stall on the same lost
// packet and queue behind the same account's limits: they are slow together.
// And it is what makes a provider's prompt cache reachable: the cache belongs
// to an account, so a session that wanders across accounts finds it cold.
//
// A provider with no pool ignores it.
func WithLane(ctx context.Context, lane int) context.Context {
	return context.WithValue(ctx, laneKey{}, lane)
}

// Lane returns the lane ctx was given, and whether it was given one.
func Lane(ctx context.Context) (int, bool) {
	v, ok := ctx.Value(laneKey{}).(int)
	return v, ok
}

var laneCursor atomic.Int64

// ReserveLanes returns the first of n consecutive lanes nobody else has been
// given. A session takes its lanes once and keeps them, so that sessions spread
// across a pool and each one stays where its cache is.
func ReserveLanes(n int) int {
	if n < 1 {
		n = 1
	}
	return int(laneCursor.Add(int64(n))) - n
}

type everyCopyKey struct{}

// WithEveryCopy asks a provider that sends copies of a request and keeps the
// first to answer to let every copy run to its end instead.
//
// It is for a request whose point is to have been made — warming a cache that
// belongs to an account, when the copies go to different accounts. Racing
// those would warm the fastest and cancel the rest half way.
func WithEveryCopy(ctx context.Context) context.Context {
	return context.WithValue(ctx, everyCopyKey{}, true)
}

// EveryCopy reports whether ctx was marked by WithEveryCopy.
func EveryCopy(ctx context.Context) bool {
	v, _ := ctx.Value(everyCopyKey{}).(bool)
	return v
}
