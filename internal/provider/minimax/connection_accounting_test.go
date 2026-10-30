package minimax

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
)

func TestObservedConnectionsReportEveryActualAccount(t *testing.T) {
	for _, test := range []struct {
		name string
		fail []string
		keys []string
		lane int
		want []int
	}{
		{"spare-failover", []string{"key-b"}, []string{"key-a", "key-b"}, 1, []int{1, 0}},
		{"multiple-refusals", []string{"key-a", "key-b"}, []string{"key-a", "key-b", "key-c"}, 0, []int{0, 1, 2}},
		{"all-refused", []string{"key-a", "key-b", "key-c"}, []string{"key-a", "key-b", "key-c"}, 0, []int{0, 1, 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, endpoint := newAccountsT2A(t, test.fail...)
			c := poolClient(endpoint, test.keys...)
			var got []int
			s, err := c.OpenObserved(provider.WithLane(context.Background(), test.lane), provider.TTSOptions{}, func(n int) { got = append(got, n) })
			if s != nil {
				defer s.Close()
			}
			if (err != nil) != (len(test.fail) == 3) {
				t.Fatalf("unexpected open result: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("reported %v; want %v", got, test.want)
			}
			if len(f.dialled()) != len(got) {
				t.Fatal("physical dials and reports differ")
			}
		})
	}
}

func TestObservedConnectionsCountReconnectAndCancelledDial(t *testing.T) {
	_, endpoint := newFakeT2A(t, false)
	c := testClient(endpoint)
	var mu sync.Mutex
	count := 0
	record := func(int) { mu.Lock(); count++; mu.Unlock() }
	s, err := c.OpenObserved(context.Background(), provider.TTSOptions{}, record)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw := s.(*stream)
	raw.dropConn()
	if err := raw.Prewarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw.dropConn()
	ch, err := raw.Synthesize(context.Background(), "test.")
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.OpenObserved(ctx, provider.TTSOptions{}, record); err == nil {
		t.Fatal("cancelled dial succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 3 {
		t.Fatalf("connection attempts %d; want initial+prewarm+Synthesize, no cancelled dial", count)
	}
}
