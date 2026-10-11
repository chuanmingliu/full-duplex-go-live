package provider

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedLLM answers its nth request the way the nth script says.
type scriptedLLM struct {
	scripts []hedgeScript

	mu        sync.Mutex
	calls     int
	cancelled []bool
}

type hedgeScript struct {
	wait    time.Duration // before the first word
	words   []string
	openErr error
	midErr  error
	lead    []LLMDelta // sent before the wait: deltas that say nothing
}

func (s *scriptedLLM) Name() string { return "scripted" }

func (s *scriptedLLM) Stream(ctx context.Context, _ LLMRequest) (<-chan LLMDelta, error) {
	s.mu.Lock()
	n := s.calls
	s.calls++
	s.cancelled = append(s.cancelled, false)
	s.mu.Unlock()
	sc := s.scripts[len(s.scripts)-1]
	if n < len(s.scripts) {
		sc = s.scripts[n]
	}
	if sc.openErr != nil {
		return nil, sc.openErr
	}
	out := make(chan LLMDelta)
	go func() {
		defer close(out)
		send := func(d LLMDelta) bool {
			select {
			case out <- d:
				return true
			case <-ctx.Done():
				s.mu.Lock()
				s.cancelled[n] = true
				s.mu.Unlock()
				return false
			}
		}
		for _, d := range sc.lead {
			if !send(d) {
				return
			}
		}
		select {
		case <-time.After(sc.wait):
		case <-ctx.Done():
			s.mu.Lock()
			s.cancelled[n] = true
			s.mu.Unlock()
			return
		}
		if sc.midErr != nil {
			send(LLMDelta{Err: sc.midErr})
			return
		}
		for _, w := range sc.words {
			if !send(LLMDelta{Text: w}) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return out, nil
}

func (s *scriptedLLM) state() (calls int, cancelled []bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]bool(nil), s.cancelled...)
}

func collect(t *testing.T, ch <-chan LLMDelta) (text string, err error, took time.Duration) {
	t.Helper()
	start := time.Now()
	var b strings.Builder
	first := time.Duration(-1)
	for d := range ch {
		if d.Err != nil {
			err = d.Err
		}
		if d.Text != "" && first < 0 {
			first = time.Since(start)
		}
		b.WriteString(d.Text)
	}
	return b.String(), err, first
}

// The point of it: a completion's first word arrives when the faster copy's
// does, and the completion is that copy's and nobody else's.
func TestHedgedCompletionIsTheCopyThatSpokeFirst(t *testing.T) {
	inner := &scriptedLLM{scripts: []hedgeScript{
		{wait: 400 * time.Millisecond, words: []string{"慢", "的", "回", "答"}},
		{wait: 30 * time.Millisecond, words: []string{"快", "的", "回", "答"}},
	}}

	plain, _ := inner.Stream(context.Background(), LLMRequest{})
	if _, _, took := collect(t, plain); took < 350*time.Millisecond {
		t.Fatalf("the slow copy answered in %v; the test proves nothing", took)
	}
	inner.calls, inner.cancelled = 0, nil

	ch, err := HedgeLLM(inner, 2, 0, nil).Stream(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	text, serr, took := collect(t, ch)
	if serr != nil {
		t.Fatalf("stream error: %v", serr)
	}
	if text != "快的回答" {
		t.Errorf("the completion reads %q; want the faster copy's, whole and unmixed", text)
	}
	if took > 200*time.Millisecond {
		t.Errorf("first word after %v; the faster copy had it at 30 ms", took)
	}
	time.Sleep(50 * time.Millisecond)
	calls, cancelled := inner.state()
	if calls != 2 {
		t.Fatalf("%d requests were made; want 2", calls)
	}
	if !cancelled[0] {
		t.Error("the copy that lost was left running")
	}
	if cancelled[1] {
		t.Error("the copy that won was cancelled")
	}
}

func TestHedgeWaitsBeforeSendingTheSecondCopy(t *testing.T) {
	quick := &scriptedLLM{scripts: []hedgeScript{{wait: 20 * time.Millisecond, words: []string{"好"}}}}
	ch, _ := HedgeLLM(quick, 2, 150*time.Millisecond, nil).Stream(context.Background(), LLMRequest{})
	if text, _, _ := collect(t, ch); text != "好" {
		t.Fatalf("got %q", text)
	}
	if calls, _ := quick.state(); calls != 1 {
		t.Errorf("%d requests for a completion that answered inside the hedge delay; want 1", calls)
	}

	slow := &scriptedLLM{scripts: []hedgeScript{
		{wait: 600 * time.Millisecond, words: []string{"慢"}},
		{wait: 20 * time.Millisecond, words: []string{"快"}},
	}}
	ch, _ = HedgeLLM(slow, 2, 100*time.Millisecond, nil).Stream(context.Background(), LLMRequest{})
	text, _, took := collect(t, ch)
	if text != "快" || took > 400*time.Millisecond {
		t.Errorf("got %q after %v; want the second copy's answer about 120 ms in", text, took)
	}
}

func TestHedgeSurvivesACopyThatFails(t *testing.T) {
	refused := errors.New("connection refused")
	for name, first := range map[string]hedgeScript{
		"refused at once":       {openErr: refused},
		"fails before a word":   {wait: 10 * time.Millisecond, midErr: refused},
		"closes without a word": {wait: 10 * time.Millisecond},
	} {
		inner := &scriptedLLM{scripts: []hedgeScript{first, {wait: 40 * time.Millisecond, words: []string{"好", "的"}}}}
		// A long hedge delay: the replacement must not wait it out.
		ch, err := HedgeLLM(inner, 2, 5*time.Second, nil).Stream(context.Background(), LLMRequest{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text, serr, took := collect(t, ch)
		if serr != nil || text != "好的" {
			t.Errorf("%s: got %q, err %v; want the other copy's answer", name, text, serr)
		}
		if took > time.Second {
			t.Errorf("%s: the replacement copy waited out the hedge delay (%v)", name, took)
		}
	}

	inner := &scriptedLLM{scripts: []hedgeScript{{openErr: refused}}}
	ch, _ := HedgeLLM(inner, 2, 0, nil).Stream(context.Background(), LLMRequest{})
	if _, serr, _ := collect(t, ch); !errors.Is(serr, refused) {
		t.Errorf("every copy failed and the completion reported %v", serr)
	}
}

// A delta that says nothing — a role, a usage record — is not speaking first.
func TestHedgeJudgesByOutputNotByTheFirstDelta(t *testing.T) {
	inner := &scriptedLLM{scripts: []hedgeScript{
		{lead: []LLMDelta{{}}, wait: 300 * time.Millisecond, words: []string{"慢"}},
		{wait: 40 * time.Millisecond, words: []string{"快"}},
	}}
	ch, _ := HedgeLLM(inner, 2, 0, nil).Stream(context.Background(), LLMRequest{})
	if text, _, _ := collect(t, ch); text != "快" {
		t.Errorf("got %q; the copy that sent an empty delta first is not the one that spoke first", text)
	}
}

func TestHedgeOfOneIsTheModelItself(t *testing.T) {
	inner := &scriptedLLM{}
	if got := HedgeLLM(inner, 1, 0, nil); got != LLM(inner) {
		t.Error("a hedge of one copy should be the model, unwrapped")
	}
}

func TestHedgeStopsWhenTheCallerDoes(t *testing.T) {
	inner := &scriptedLLM{scripts: []hedgeScript{{wait: 5 * time.Second, words: []string{"x"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := HedgeLLM(inner, 2, 0, nil).Stream(ctx, LLMRequest{})
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("output after the caller cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("the stream did not close when the caller cancelled")
	}
	time.Sleep(30 * time.Millisecond)
	if _, cancelled := inner.state(); len(cancelled) != 2 || !cancelled[0] || !cancelled[1] {
		t.Errorf("copies left running after the caller cancelled: %v", cancelled)
	}
}
