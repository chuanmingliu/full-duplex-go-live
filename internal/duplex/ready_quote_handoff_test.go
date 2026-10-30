package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/tools"
)

func readyQuoteHandoff(t *testing.T) (*Engine, *collector, *tools.Task, *pendingResponse) {
	t.Helper()
	e, c, task, p := newAckHandoffTest(t)
	e.player.cfg.Paced = false
	p.turn.MarkSpeechEnd(time.Now().Add(-time.Second), 0, 380)
	p.turn.Opened, p.turn.OpenedMS = "opener", 40
	qs, err := tools.ParseQuoteSpeech([]byte(`{"prehook":"wait","posthook":"ready","posthook_status":"quote","template":"Total {{ total }}."}`))
	if err != nil {
		t.Fatal(err)
	}
	task.Tool = tools.Tool{Name: "quote_total", Async: true}.WithSpeech(qs)
	task.Result.Output = []byte(`{"status":"quote","total":100,"default":true}`)
	e.tools.origins = map[string]taskOrigin{task.ID: {turnID: p.turn.ID, speechEndAt: p.turn.Timings().SpeechEndAt}}
	return e, c, task, p
}

func queueUnheardQuoteClause(t *testing.T, e *Engine, p *pendingResponse) {
	t.Helper()
	pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))
	opener := Segment{Gen: p.turn.Generation, TurnID: p.turn.ID, owner: p.turn, Text: p.turn.Opened, PCM: pcm}
	e.player.beginSegment(&e.gen, opener)
	e.player.playAudio(&e.gen, opener)
	e.player.markSegment(&e.gen, opener)
	pipe := &speechPipe{e: e, turn: p.turn, ctx: context.Background()}
	if !pipe.speakCached("unheard calling clause", PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate}) {
		t.Fatal("could not queue the calling clause")
	}
	e.player.Enqueue(Segment{Kind: SegEnd, Gen: p.turn.Generation, TurnID: p.turn.ID, owner: p.turn})
}

func TestReadyQuoteReplacesUnplayedCallingContent(t *testing.T) {
	for _, begun := range []bool{false, true} {
		name := "queued"
		if begun {
			name = "begun_without_audio"
		}
		t.Run(name, func(t *testing.T) { checkReadyQuoteReplacesUnplayedContent(t, begun) })
	}
}

func checkReadyQuoteReplacesUnplayedContent(t *testing.T, begun bool) {
	t.Helper()
	e, c, task, p := readyQuoteHandoff(t)
	var logs bytes.Buffer
	e.log = slog.New(slog.NewJSONHandler(&logs, nil))
	queueUnheardQuoteClause(t, e, p)
	if begun {
		begin := e.player.queue[0]
		e.player.queue = e.player.queue[1:]
		e.player.beginSegment(&e.gen, begin)
	}
	pipe := e.newSpeechPipe(p.turn, false)
	t.Cleanup(pipe.Abort)
	e.continueWith(p)
	e.onTaskDone(task)
	awaitHandoffSignal(t, pipe.done)
	e.wake() // producer wake precedes the player's clean completion callback
	if e.player.Enqueue(Segment{Kind: SegAudio, Gen: p.turn.Generation, TurnID: p.turn.ID, owner: p.turn, content: true, PCM: []byte{1, 2}}) {
		t.Fatal("a late producer chunk was accepted after handoff")
	}
	e.player.OnTurnDone(func(r PlaybackReport) {
		e.onTurnDone(r)
		e.player.Close()
	})
	e.player.Run(&e.gen)
	e.runPosted()
	if e.tools.handoff != nil {
		t.Fatal("completed handoff retained the original floor")
	}
	if got := c.allOutput(); got != "opener" {
		t.Fatalf("ready quote must replace only the unheard clause; heard %q", got)
	}
	if !p.turn.Timings().FirstContentOut.IsZero() || p.turn.Timings().FirstAudioOut.IsZero() {
		t.Fatal("handoff fabricated content or lost the opener's output")
	}
	if !p.turn.Timings().CompletedNaturally {
		t.Fatal("opener-only handoff lost its natural completion proof")
	}
	if c.count(live.ExtAudioTruncated) != 0 {
		t.Fatal("a ready quote handoff was reported as caller interruption")
	}
	if e.tools.delivered != 1 {
		t.Fatal("ready result did not follow the opener's clean completion")
	}
	var metric, link map[string]any
	decoder := json.NewDecoder(&logs)
	for decoder.More() {
		var row map[string]any
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		switch row["msg"] {
		case "turn: response latency":
			metric = row
		case "tools: delivering a task's result":
			link = row
		}
	}
	if metric["first_content_present"] != false || metric["first_audio_present"] != true || metric["truncated"] != false {
		t.Fatalf("untruthful handoff metric: %v", metric)
	}
	result := e.tracker.Current()
	wantOffset := result.Timings().StartedAt.Sub(p.turn.Timings().SpeechEndAt).Milliseconds()
	if link["origin_turn"] != p.turn.ID || link["turn"] != result.ID || link["origin_to_result_start_ms"] != float64(wantOffset) || wantOffset < 1000 {
		t.Fatalf("lost caller origin: %v", link)
	}
	if strings.Contains(p.turn.Spoken, "unheard") {
		t.Fatal("unplayed content entered spoken history")
	}
	if got := c.audioMS(); got != 40 {
		t.Fatalf("played duration includes discarded content: %v", got)
	}
	for _, msg := range e.history {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "unheard") {
			t.Fatal("unplayed content entered assistant history")
		}
	}
}

func awaitHandoffSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("handoff synchronization timed out")
	}
}

func TestReadyQuoteHandoffKeepsSafetyGuards(t *testing.T) {
	for _, guard := range []string{"caller", "pending_final", "floor_hold", "muted", "failed", "not_a_quote", "no_readout", "no_opening", "stale_memory", "different_turn", "scripted_ack", "ack_started", "different_continuation", "no_origin", "no_opener", "no_audio", "content_heard", "other_floor_owner", "closed"} {
		t.Run(guard, func(t *testing.T) {
			e, _, task, p := readyQuoteHandoff(t)
			queueUnheardQuoteClause(t, e, p)
			e.continueWith(p)
			switch guard {
			case "caller":
				e.userOpen = true
			case "pending_final":
				e.asrRun = &asrRun{sendDone: true}
			case "floor_hold":
				e.hold = &floorHold{}
			case "muted":
				e.muted = true
			case "failed":
				task.Result.Err = errors.New("lookup failed")
			case "not_a_quote":
				task.Result.Output = []byte(`{"status":"need","total":100}`)
			case "no_readout":
				task.Tool.Speech = nil
			case "no_opening":
				task.Tool.ResultOpening = ""
			case "stale_memory":
				task.Request.MemoryVersion = 1
			case "different_turn":
				e.tracker.Begin("a newer question", false)
			case "scripted_ack":
				p.turn.Scripted = true
			case "ack_started":
				e.tools.ackGraceStarted = map[string]bool{task.ID: true}
			case "different_continuation":
				p.scriptedTask = "another-task"
			case "no_origin":
				clear(e.tools.origins)
			case "no_opener":
				p.turn.Opened = ""
			case "no_audio":
				e.player.emittedMS = 0
			case "content_heard":
				p.turn.MarkFirstContentOut()
			case "other_floor_owner":
				e.player.Enqueue(Segment{Kind: SegBegin, Gen: p.turn.Generation, TurnID: "another-turn"})
			case "closed":
				ctx, cancel := context.WithCancel(e.ctx)
				cancel()
				e.ctx = ctx
			}
			e.onTaskDone(task)
			if e.player.handoffTurn != "" {
				t.Fatal("ready quote bypassed a handoff guard")
			}
		})
	}
}

// Stop after the player commits its first content output, but before the wire
// sink runs. Ownership and output stamps must already agree at this boundary.
func TestReadyQuoteCannotReplaceReservedFirstContent(t *testing.T) {
	e, c, task, p := readyQuoteHandoff(t)
	queueUnheardQuoteClause(t, e, p)
	reserved, release := make(chan struct{}), make(chan struct{})
	emit := e.player.emit
	e.player.emit = func(id string, pcm []byte, at time.Time) {
		close(reserved)
		<-release
		emit(id, pcm, at)
	}
	e.continueWith(p)
	e.player.OnTurnDone(func(r PlaybackReport) {
		e.onTurnDone(r)
		e.player.Close()
	})
	finished := make(chan struct{})
	go func() { e.player.Run(&e.gen); close(finished) }()
	awaitHandoffSignal(t, reserved)
	if p.turn.Timings().FirstContentOut.IsZero() {
		t.Fatal("committed content has no output stamp")
	}
	e.onTaskDone(task)
	refused := e.player.handoffTurn == ""
	close(release)
	awaitHandoffSignal(t, finished)
	e.runPosted()
	if !refused || c.allOutput() != "openerunheard calling clause" || c.audioMS() != 80 {
		t.Fatal("a clause whose first output was reserved was cut")
	}
}

func TestReadyQuotePreservesOpenerStillPlaying(t *testing.T) {
	e, c, task, p := readyQuoteHandoff(t)
	e.player.cfg.ChunkMS = 20
	pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(80))
	base := Segment{Gen: p.turn.Generation, TurnID: p.turn.ID, owner: p.turn}
	begin, body, mark := base, base, base
	begin.Kind, begin.Text = SegBegin, "opener"
	body.Kind, body.PCM = SegAudio, pcm
	mark.Kind, mark.Text = SegMark, "opener"
	e.player.Enqueue(begin)
	e.player.Enqueue(body)
	e.player.Enqueue(mark)
	pipe := &speechPipe{e: e, turn: p.turn, ctx: context.Background()}
	if !pipe.speakCached("unheard calling clause", PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate}) {
		t.Fatal("could not queue content")
	}
	blocked, release := make(chan struct{}), make(chan struct{})
	chunks := 0
	e.player.emit = func(id string, data []byte, at time.Time) {
		chunks++
		if chunks == 2 {
			close(blocked)
			<-release
		}
		e.emitAudio(id, data, at)
	}
	e.continueWith(p)
	e.player.OnTurnDone(func(r PlaybackReport) {
		e.onTurnDone(r)
		e.player.Close()
	})
	finished := make(chan struct{})
	go func() { e.player.Run(&e.gen); close(finished) }()
	awaitHandoffSignal(t, blocked)
	e.onTaskDone(task)
	close(release)
	awaitHandoffSignal(t, finished)
	e.runPosted()
	if got := c.allOutput(); got != "opener" || c.audioMS() != 80 || c.count(live.ExtAudioTruncated) != 0 {
		t.Fatalf("opener was cut or unheard body leaked: %q, %v ms", got, c.audioMS())
	}
}
