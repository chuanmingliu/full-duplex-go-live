package journal

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// TargetMS is the goal the journal judges turns against: the caller's last
// sound to the first audio leaving golive, at p95.
const TargetMS = 1200

// MinTurnsForP95 is how many turns it takes before a p95 is more than the
// worst turn or the one before it.
const MinTurnsForP95 = 40

// defaultPauseMS is the end-of-turn silence assumed for a turn whose log line
// does not say, as scripts/call-latency.sh assumes it.
const defaultPauseMS = 380

// Stage is one part of the wait between the caller's last sound and the first
// audio of the answer. A turn's stages add up to its total.
type Stage struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	MS    int    `json:"ms"`
}

// Turn is one answered turn of a call.
type Turn struct {
	ID string `json:"id"`
	N  int    `json:"n"`
	// LastSoundS and AudioOutS place the turn in the call's recording, in
	// seconds from its start; negative when the log could not place it.
	LastSoundS float64 `json:"last_sound_s"`
	AudioOutS  float64 `json:"audio_out_s"`
	// TotalMS is last sound → first audio; ReplyMS is turn judged over → first
	// audio, which is what the owner's latency console calls the reply.
	TotalMS int     `json:"total_ms"`
	ReplyMS int     `json:"reply_ms"`
	PauseMS int     `json:"pause_ms"`
	Over    bool    `json:"over"`
	Stages  []Stage `json:"stages"`

	CallerText    string `json:"caller_text"`
	AssistantText string `json:"assistant_text"`

	Speculative bool `json:"speculative"`
	CacheHit    bool `json:"cache_hit"`
	Interrupted bool `json:"interrupted"`
	HeardMS     int  `json:"heard_ms,omitempty"`

	LLMFirstTokenMS int `json:"llm_first_token_ms"`
	TTSFirstAudioMS int `json:"tts_first_audio_ms"`
	WinnerCopy      int `json:"winner_copy,omitempty"`
	Copies          int `json:"copies,omitempty"`

	HedgeAsked   bool `json:"hedge_asked,omitempty"`
	HedgeRefused bool `json:"hedge_refused,omitempty"`
	HedgeWinner  int  `json:"hedge_winner,omitempty"`

	// Cause is the journal's own reading of why a turn over target was slow:
	// the stage carrying the excess. It is a first guess from the numbers,
	// not a diagnosis; the retrospective is where a cause is argued.
	Cause string `json:"cause,omitempty"`
}

// Marker is a moment in a call worth seeing on its timeline.
type Marker struct {
	TS    float64 `json:"t_s"`
	Kind  string  `json:"kind"`
	Label string  `json:"label"`
}

// Said is one line of the conversation: something the caller or the assistant
// said, in the order it was said.
type Said struct {
	// TS is where the line sits in the call. For the assistant it is the
	// first audio of the line. For the caller it is the end of their words —
	// their last sound, which is where the wait for an answer is measured
	// from — and StartS, when the log has it, is where they began.
	TS     float64 `json:"t_s"`
	StartS float64 `json:"start_s,omitempty"`
	Who    string  `json:"who"` // "caller" or "assistant"
	// Text is what was said. For an answer the caller cut short it is what
	// had been heard when they did, not what was going to be said.
	Text string `json:"text"`
	// Kind is "greeting" for the opening line, "answer" for an answered turn's
	// reply, and empty for the caller.
	Kind string `json:"kind,omitempty"`
	// Turn is the answered turn this line belongs to — the caller's words that
	// were answered, or the answer — and zero for words nobody answered: an
	// interruption, a hesitation left with the caller.
	Turn        int  `json:"turn,omitempty"`
	TotalMS     int  `json:"total_ms,omitempty"`
	Over        bool `json:"over,omitempty"`
	Interrupted bool `json:"interrupted,omitempty"`
}

// Stats summarises a set of turns the way scripts/call-latency.sh does.
type Stats struct {
	Turns    int `json:"turns"`
	P50      int `json:"p50"`
	P90      int `json:"p90"`
	P95      int `json:"p95"`
	Max      int `json:"max"`
	Over     int `json:"over"`
	ReplyP50 int `json:"reply_p50"`
	ReplyP90 int `json:"reply_p90"`
	ReplyP95 int `json:"reply_p95"`
	// MissLow and MissHigh bound the true share of turns over target, in
	// percent, at 95%: a p95 target is a claim about that share, and a
	// handful of turns says little about it.
	MissLow  float64 `json:"miss_low"`
	MissHigh float64 `json:"miss_high"`
	// Verdict is "met", "provisional", "not met" or "too few".
	Verdict string `json:"verdict"`
}

// Events counts what happened on a call besides its answered turns.
type Events struct {
	Utterances     int `json:"utterances"`
	EarlyFinals    int `json:"early_finals"`
	ClosedOnFinal  int `json:"closed_on_final"`
	GuessesHeld    int `json:"guesses_held"`
	GuessesDropped int `json:"guesses_dropped"`
	Interruptions  int `json:"interruptions"`
	CachePlays     int `json:"cache_plays"`
	HedgeAsked     int `json:"hedge_asked"`
	HedgeWon       int `json:"hedge_won"`
	HedgeRefused   int `json:"hedge_refused"`
	// AccountWins is, for segments asked of more than one synthesis account,
	// how often each spoke first; PoolMoves how often a session left an
	// account that had had its minute.
	AccountWins    []int   `json:"account_wins,omitempty"`
	PoolMoves      int     `json:"pool_moves,omitempty"`
	Segments       int     `json:"segments"`
	AbandonedTasks int     `json:"abandoned_tasks"`
	CopyWins       []int   `json:"copy_wins"`
	LLMFirstTokens []int   `json:"llm_first_tokens"`
	TTSFirstAudios []int   `json:"tts_first_audios"`
	PausesInside   []int   `json:"pauses_inside"`
	PrefixWarmMS   []int   `json:"prefix_warm_ms"`
	EchoReturns    []int   `json:"echo_returns"`
	EchoNotTimed   int     `json:"echo_not_timed"`
	NoiseFloorDB   float64 `json:"noise_floor_db"`
	IdleLevelDB    float64 `json:"idle_level_db"`
	HasIdleLevel   bool    `json:"has_idle_level"`
}

// Warning is a WARN or ERROR line logged during a call.
type Warning struct {
	TS    float64 `json:"t_s"`
	Level string  `json:"level"`
	Text  string  `json:"text"`
	Count int     `json:"count"`
}

// Call is everything the journal knows about one phone call.
type Call struct {
	ID         string    `json:"id"`
	Session    string    `json:"session"`
	StartedAt  time.Time `json:"started_at"`
	AnsweredAt time.Time `json:"answered_at,omitempty"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	DurationS  float64   `json:"duration_s"`
	Build      string    `json:"build"`
	Stored     bool      `json:"stored"`

	PromptChars int    `json:"prompt_chars"`
	PromptSHA   string `json:"prompt_sha,omitempty"`
	LLMCopies   int    `json:"llm_copies,omitempty"`
	Keys        int    `json:"keys,omitempty"`
	Lanes       int    `json:"lanes,omitempty"`
	Greeting    string `json:"greeting,omitempty"`

	Summary      Stats   `json:"summary"`
	StageMedians []Stage `json:"stage_medians"`
	Turns        []Turn  `json:"turns"`
	Transcript   []Said  `json:"transcript"`
	// AudioIn is the caller's audio as it arrived, a mark every few seconds.
	AudioIn  []AudioMark `json:"audio_in,omitempty"`
	Markers  []Marker    `json:"markers"`
	Events   Events      `json:"events"`
	Warnings []Warning   `json:"warnings"`
}

// --- reading the log ---

type turnBuild struct {
	id           string
	callerText   string
	said         []string
	heardText    string
	heardMS      int
	interrupted  bool
	firstAudioAt time.Time
	firstIsCache bool
	sawFirst     bool
	ttsMS        int
	winner, of   int
	headersMS    int
	askPause     int
	asrTook      int
	hedgeAsked   bool
	hedgeRefused bool
	hedgeWinner  int
	latency      *Line
}

type heardLine struct {
	at    time.Time
	began time.Time // when the detector opened on these words; zero when unknown
	text  string
}

// AudioMark is how much of the caller's audio had arrived at a moment of the
// call: Frames chunks of Bytes each by TS. The log has one every 250 chunks.
// It is what says whether audio arrived faster than time passed, and what a
// recording made before the recorder corrected for that is put right with.
type AudioMark struct {
	TS     float64 `json:"t_s"`
	Frames int     `json:"frames"`
	Bytes  int     `json:"bytes"`
}

// openLeadS is how long the detector listens before it says an utterance has
// opened: the line is logged that long after the caller began.
const openLeadS = 0.25

type callBuild struct {
	call  Call
	phone bool
	turns map[string]*turnBuild
	order []string
	noise []float64
	idle  []float64
	// heard is what the caller said, each utterance as it stood when it ended.
	heard []heardLine
	// openedAt is when the detector last opened on speech that no line has
	// claimed yet.
	openedAt time.Time
	warn     map[string]*Warning
	warnOrd  []string

	lastAskPause int
	lastFinal    struct {
		at   time.Time
		took int
	}
	pendingWinner, pendingOf int
	pendingHedge             struct {
		asked, refused bool
		winner         int
	}
	copyWins map[int]int

	// logFrom and logTo are where this call's lines begin and end in the log,
	// in bytes: what the archive copies as the call's own slice of it.
	logFrom, logTo int64
}

func (cb *callBuild) turn(id string) *turnBuild {
	t := cb.turns[id]
	if t == nil {
		t = &turnBuild{id: id}
		cb.turns[id] = t
		cb.order = append(cb.order, id)
	}
	return t
}

func (cb *callBuild) at(t time.Time) float64 {
	if cb.call.StartedAt.IsZero() || t.IsZero() {
		return -1
	}
	return math.Round(t.Sub(cb.call.StartedAt).Seconds()*1000) / 1000
}

// hear records what the caller said, once per utterance: the log reports an
// utterance's words where it ends, and sometimes in two lines a moment apart.
func (cb *callBuild) hear(at time.Time, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if n := len(cb.heard); n > 0 && cb.heard[n-1].text == text && at.Sub(cb.heard[n-1].at) < time.Second {
		return
	}
	line := heardLine{at: at, text: text}
	if !cb.openedAt.IsZero() && cb.openedAt.Before(at) {
		line.began = cb.openedAt.Add(-time.Duration(openLeadS * float64(time.Second)))
	}
	cb.openedAt = time.Time{}
	cb.heard = append(cb.heard, line)
}

func (cb *callBuild) mark(t time.Time, kind, label string) {
	cb.call.Markers = append(cb.call.Markers, Marker{TS: cb.at(t), Kind: kind, Label: label})
}

// Indexer turns golive's log into calls. Feed it lines in order.
type Indexer struct {
	build       string
	keys, lanes int
	sessions    map[string]*callBuild
	order       []string
	byCall      map[string]*callBuild
	active      *callBuild

	// The copies of the completion in flight, which the log reports without
	// saying whose they are: one call is on the line at a time.
	opens    []int
	opensFor *turnBuild

	// lineFrom and lineTo are the byte range of the line being fed.
	lineFrom, lineTo int64
}

// LogRange is the byte range of a call's lines in the log that was read: from
// the first line that names its session to the last, with whatever other
// lines fell between them.
func (ix *Indexer) LogRange(callID string) (from, to int64, ok bool) {
	cb := ix.byCall[callID]
	if cb == nil || cb.logTo <= cb.logFrom {
		return 0, 0, false
	}
	return cb.logFrom, cb.logTo, true
}

// NewIndexer returns an empty indexer.
func NewIndexer() *Indexer {
	return &Indexer{sessions: map[string]*callBuild{}, byCall: map[string]*callBuild{}}
}

// ReadLog feeds every line of a log.
func (ix *Indexer) ReadLog(r io.Reader) error {
	br := bufio.NewReaderSize(r, 1<<20)
	var off int64
	for {
		raw, err := br.ReadString('\n')
		if len(raw) > 0 {
			ix.lineFrom, ix.lineTo = off, off+int64(len(raw))
			off = ix.lineTo
			if l, ok := ParseLine(strings.TrimRight(raw, "\r\n")); ok {
				ix.Feed(l)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (ix *Indexer) session(id string) *callBuild {
	cb := ix.sessions[id]
	if cb == nil {
		cb = &callBuild{turns: map[string]*turnBuild{}, warn: map[string]*Warning{}, copyWins: map[int]int{}}
		cb.call.Session = id
		cb.call.Build = ix.build
		cb.logFrom = ix.lineFrom
		cb.call.Keys, cb.call.Lanes = ix.keys, ix.lanes
		ix.sessions[id] = cb
		ix.order = append(ix.order, id)
	}
	return cb
}

// Feed takes one line.
func (ix *Indexer) Feed(l Line) {
	switch l.Msg {
	case "listening":
		ix.build = l.Get("version")
		return
	case "llm: pool ready":
		ix.keys, ix.lanes = l.N("keys"), l.N("lanes")
		if ix.active != nil {
			ix.active.call.Keys, ix.active.call.Lanes = ix.keys, ix.lanes
		}
		return
	case "realtime: call answered; session opened":
		if cb := ix.byCall[l.Get("call")]; cb != nil {
			cb.call.AnsweredAt = l.Time
		}
		return
	case "llm: stream open":
		if ix.opensFor != nil {
			ix.opens = append(ix.opens, l.N("ms"))
		}
		return
	case "minimax tts: resynced an abandoned task":
		if ix.active != nil {
			ix.active.call.Events.AbandonedTasks++
		}
		return
	case "http":
		// The gateway's socket closing is the end of the call.
		path := l.Get("path")
		if i := strings.Index(path, "call="); i >= 0 && strings.HasPrefix(path, "/v1/realtime") {
			id := path[i+5:]
			if j := strings.IndexAny(id, "&# "); j >= 0 {
				id = id[:j]
			}
			if cb := ix.byCall[id]; cb != nil {
				cb.call.EndedAt = l.Time
				cb.logTo = ix.lineTo
				if ix.active == cb {
					ix.active = nil
				}
			}
		}
		return
	}

	sid := l.Get("session")
	var cb *callBuild
	if sid != "" {
		cb = ix.session(sid)
	} else if ix.active != nil && (l.Level == "WARN" || l.Level == "ERROR") {
		cb = ix.active
	}
	if cb == nil {
		return
	}
	if ix.lineTo > cb.logTo {
		cb.logTo = ix.lineTo
	}

	if l.Level == "WARN" || l.Level == "ERROR" {
		if !strings.Contains(l.Msg, "bidirectional-only") {
			w := cb.warn[l.Msg]
			if w == nil {
				w = &Warning{TS: cb.at(l.Time), Level: l.Level, Text: l.Msg}
				cb.warn[l.Msg] = w
				cb.warnOrd = append(cb.warnOrd, l.Msg)
			}
			w.Count++
		}
	}

	ev := &cb.call.Events
	switch l.Msg {
	case "realtime: session.update":
		cb.call.PromptChars = l.N("instructions_chars")
		cb.call.PromptSHA = l.Get("instructions_sha")
		if cb.call.StartedAt.IsZero() {
			cb.call.StartedAt = l.Time
		}
	case "session started":
		cb.call.StartedAt = l.Time
		cb.call.Stored = l.Get("stored") == "true"
		if l.Get("realtime") == "true" && l.Get("call") != "" {
			cb.phone = true
			cb.call.ID = l.Get("call")
			ix.byCall[cb.call.ID] = cb
			ix.active = cb
		}
	case "speak: opening line":
		cb.call.Greeting = l.Get("text")
	case "client audio":
		if n, b := l.N("frames"), l.N("bytes"); n > 0 && b > 0 {
			cb.call.AudioIn = append(cb.call.AudioIn, AudioMark{TS: cb.at(l.Time), Frames: n, Bytes: b})
		}
	case "listen: utterance opened":
		ev.Utterances++
		if cb.openedAt.IsZero() {
			cb.openedAt = l.Time
		}
		if f, ok := l.Float("noise_floor_db"); ok {
			cb.noise = append(cb.noise, f)
		}
	case "asr: asking for a final on the words so far; a continuation is listening":
		ev.EarlyFinals++
		cb.lastAskPause = l.N("pause_ms")
	case "asr: final for the words so far":
		cb.lastFinal.at, cb.lastFinal.took = l.Time, l.N("took_ms")
	case "asr: the turn closed on words already final":
		ev.ClosedOnFinal++
		cb.hear(l.Time, l.Get("text"))
	case "asr: final":
		cb.hear(l.Time, l.Get("text"))
	case "think: delegating":
		t := cb.turn(l.Get("turn"))
		t.callerText = l.Get("transcript")
		ix.opens, ix.opensFor = nil, t
		if l.Get("reason") == "speculative" {
			t.askPause = cb.lastAskPause
			t.asrTook = 0
			if !cb.lastFinal.at.IsZero() && l.Time.Sub(cb.lastFinal.at) < 100*time.Millisecond {
				t.asrTook = cb.lastFinal.took
			}
		} else {
			t.askPause, t.asrTook = 0, 0
		}
	case "llm hedge: a copy answered first":
		cb.pendingWinner, cb.pendingOf = l.N("copy"), l.N("of")
		ev.LLMFirstTokens = append(ev.LLMFirstTokens, l.N("ms"))
		cb.copyWins[l.N("copy")]++
		if n := l.N("of"); n > cb.call.LLMCopies {
			cb.call.LLMCopies = n
		}
	case "think: first token":
		t := cb.turn(l.Get("turn"))
		t.winner, t.of = cb.pendingWinner, cb.pendingOf
		cb.pendingWinner, cb.pendingOf = 0, 0
		if cb.call.LLMCopies == 0 {
			ev.LLMFirstTokens = append(ev.LLMFirstTokens, l.N("ms"))
		}
		t.headersMS = 0
		if ix.opensFor == t {
			for _, ms := range ix.opens {
				if ms > 0 && (t.headersMS == 0 || ms < t.headersMS) {
					t.headersMS = ms
				}
			}
		}
	case "think: speculation held; keeping the audio already in flight":
		ev.GuessesHeld++
	case "think: abandoning the speculative turn":
		ev.GuessesDropped++
		cb.mark(l.Time, "dropped", "A prepared answer was dropped: the caller carried on")
	case "tts hedge: asking the second connection too", "tts hedge: asking both connections at once":
		ev.HedgeAsked++
		cb.pendingHedge.asked = true
	case "tts hedge: a copy produced audio first":
		cb.pendingHedge.winner = l.N("copy")
		if l.N("copy") == 2 {
			ev.HedgeWon++
		}
		// With a pool of synthesis accounts, which one spoke first.
		if a := l.N("account"); a >= 1 && a <= 9 {
			for len(ev.AccountWins) < a {
				ev.AccountWins = append(ev.AccountWins, 0)
			}
			ev.AccountWins[a-1]++
		}
	case "tts pool: this account is at its limit; the session moves to the other":
		ev.PoolMoves++
	case "tts hedge: no room this minute for a second request":
		ev.HedgeRefused++
		cb.pendingHedge.refused = true
		cb.mark(l.Time, "hedge_refused", "A second synthesis request was wanted and refused by the request budget")
	case "speak: first audio for segment":
		t := cb.turn(l.Get("turn"))
		ms := l.N("ms")
		ev.TTSFirstAudios = append(ev.TTSFirstAudios, ms)
		if !t.sawFirst {
			t.sawFirst, t.firstAudioAt, t.ttsMS = true, l.Time, ms
			t.hedgeAsked, t.hedgeRefused, t.hedgeWinner = cb.pendingHedge.asked, cb.pendingHedge.refused, cb.pendingHedge.winner
		}
		cb.pendingHedge = struct {
			asked, refused bool
			winner         int
		}{}
		if ms > 300 {
			cb.mark(l.Time.Add(-time.Duration(ms)*time.Millisecond), "tts_slow",
				fmt.Sprintf("The synthesizer took %d ms to its first audio", ms))
		}
	case "speak: segment synthesized":
		t := cb.turn(l.Get("turn"))
		t.said = append(t.said, l.Get("text"))
		ev.Segments++
	case "speak: segment played from the phrase cache":
		t := cb.turn(l.Get("turn"))
		t.said = append(t.said, l.Get("text"))
		ev.CachePlays++
		if !t.sawFirst {
			t.sawFirst, t.firstAudioAt, t.firstIsCache = true, l.Time, true
		}
	case "speak: turn truncated":
		t := cb.turn(l.Get("turn"))
		t.interrupted, t.heardText, t.heardMS = true, l.Get("heard_text"), l.N("played_ms")
		ev.Interruptions++
		cb.mark(l.Time, "interrupted", "The caller spoke over the answer; it was cut")
	case "turn: response latency":
		line := l
		cb.turn(l.Get("turn")).latency = &line
	case "listen: the caller paused inside the utterance":
		ev.PausesInside = append(ev.PausesInside, l.N("pause_ms"))
	case "listen: the assistant's voice came back":
		ev.EchoReturns = append(ev.EchoReturns, l.N("ms"))
	case "listen: the assistant's voice coming back was not timed":
		ev.EchoNotTimed++
		if f, ok := l.Float("level_db"); ok && strings.Contains(l.Get("reason"), "not been quiet") {
			cb.idle = append(cb.idle, f)
		}
	case "think: prefix warmed":
		ev.PrefixWarmMS = append(ev.PrefixWarmMS, l.N("ms"))
	case "session: response latency summary":
		if cb.call.EndedAt.IsZero() {
			cb.call.EndedAt = l.Time
		}
	}
}

// Calls returns the phone calls read so far, newest first.
func (ix *Indexer) Calls() []*Call {
	var out []*Call
	for _, sid := range ix.order {
		cb := ix.sessions[sid]
		if !cb.phone {
			continue
		}
		out = append(out, cb.finish())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

func (cb *callBuild) finish() *Call {
	c := cb.call
	c.Turns, c.Markers = nil, append([]Marker(nil), cb.call.Markers...)
	if !c.EndedAt.IsZero() {
		c.DurationS = math.Round(c.EndedAt.Sub(c.StartedAt).Seconds()*10) / 10
	}
	n := 0
	for _, id := range cb.order {
		t := cb.turns[id]
		if t.latency == nil {
			continue
		}
		l := *t.latency
		asr, llm := l.N("asr_final_ms"), l.N("llm_first_token_ms")
		if asr == 0 && llm == 0 {
			continue // an opening line: nobody was waiting
		}
		n++
		c.Turns = append(c.Turns, cb.turnOf(t, n))
	}
	c.Transcript = cb.transcript(c.Turns)
	c.Summary = Summarise(c.Turns)
	c.StageMedians = stageMedians(c.Turns)
	for i := range c.Turns {
		if c.Turns[i].Over {
			c.Turns[i].Cause = guessCause(c.Turns[i], c.StageMedians)
			c.Markers = append(c.Markers, Marker{TS: c.Turns[i].LastSoundS, Kind: "over",
				Label: fmt.Sprintf("Turn %d: %d ms", c.Turns[i].N, c.Turns[i].TotalMS)})
		}
	}
	sort.SliceStable(c.Markers, func(i, j int) bool { return c.Markers[i].TS < c.Markers[j].TS })

	ev := &c.Events
	if len(cb.copyWins) > 0 {
		ev.CopyWins = make([]int, c.LLMCopies)
		for copyN, wins := range cb.copyWins {
			if copyN >= 1 && copyN <= len(ev.CopyWins) {
				ev.CopyWins[copyN-1] = wins
			}
		}
	}
	ev.NoiseFloorDB = medianF(cb.noise)
	if len(cb.idle) > 0 {
		ev.IdleLevelDB, ev.HasIdleLevel = medianF(cb.idle), true
	}
	c.Warnings = nil
	for _, k := range cb.warnOrd {
		c.Warnings = append(c.Warnings, *cb.warn[k])
	}
	return &c
}

// transcript is the conversation in order: everything the assistant said,
// answered turn or not, and everything the caller said, answered or not.
func (cb *callBuild) transcript(turns []Turn) []Said {
	byID := map[string]*Turn{}
	for i := range turns {
		byID[turns[i].ID] = &turns[i]
	}
	var out []Said
	for _, id := range cb.order {
		t := cb.turns[id]
		text := strings.Join(t.said, "")
		if t.interrupted && t.heardText != "" {
			text = t.heardText
		}
		if text == "" || t.firstAudioAt.IsZero() {
			continue
		}
		line := Said{TS: cb.at(t.firstAudioAt), Who: "assistant", Text: text, Kind: "greeting", Interrupted: t.interrupted}
		if turn := byID[id]; turn != nil {
			line.Kind, line.Turn, line.TotalMS, line.Over = "answer", turn.N, turn.TotalMS, turn.Over
			if turn.AudioOutS >= 0 {
				line.TS = turn.AudioOutS
			}
		} else if t.latency == nil {
			line.Kind = "answer"
		}
		out = append(out, line)
	}
	// The caller's words that an answered turn was the answer to are the last
	// utterance to end before that turn's answer began and after the one
	// before it.
	for _, h := range cb.heard {
		line := Said{TS: cb.at(h.at), Who: "caller", Text: h.text}
		if began := cb.at(h.began); began > 0 {
			line.StartS = began
		}
		out = append(out, line)
	}
	inOrder := func() {
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].TS != out[j].TS {
				return out[i].TS < out[j].TS
			}
			return out[i].Who == "caller" && out[j].Who != "caller"
		})
	}
	inOrder()
	for i := range out {
		if out[i].Who != "assistant" || out[i].Turn == 0 {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if out[j].Who == "assistant" {
				break
			}
			if out[j].Turn == 0 {
				out[j].Turn, out[j].TotalMS, out[j].Over = out[i].Turn, out[i].TotalMS, out[i].Over
				if turn := turns[out[i].Turn-1]; turn.LastSoundS >= 0 && turn.LastSoundS < out[j].TS {
					out[j].TS = turn.LastSoundS
				}
				if out[j].StartS >= out[j].TS {
					out[j].StartS = 0
				}
				break
			}
		}
	}
	// A caller's line is logged where their utterance ended and has just been
	// moved to their last sound, which can be before a line it was after.
	inOrder()
	return out
}

func (cb *callBuild) turnOf(t *turnBuild, n int) Turn {
	l := *t.latency
	pause := l.N("end_silence_ms")
	if pause <= 0 {
		pause = defaultPauseMS
	}
	start, hasStart := l.Int("turn_start_ms")
	llm, seg := l.N("llm_first_token_ms"), l.N("first_segment_ms")
	tts, out := l.N("tts_first_audio_ms"), l.N("first_audio_out_ms")
	spec := l.Get("speculative") == "true"

	turn := Turn{
		ID: t.id, N: n, PauseMS: pause,
		ReplyMS: out, TotalMS: out + pause, Over: out+pause > TargetMS,
		CallerText: t.callerText, AssistantText: strings.Join(t.said, ""),
		Speculative: spec, CacheHit: t.firstIsCache,
		Interrupted: t.interrupted, HeardMS: t.heardMS,
		WinnerCopy: t.winner, Copies: t.of,
		HedgeAsked: t.hedgeAsked, HedgeRefused: t.hedgeRefused, HedgeWinner: t.hedgeWinner,
		LastSoundS: -1, AudioOutS: -1,
	}
	if !hasStart {
		start = 0
	}
	turn.LLMFirstTokenMS = llm - start
	turn.TTSFirstAudioMS = tts - seg
	if turn.TTSFirstAudioMS < 0 {
		turn.TTSFirstAudioMS = 0
	}

	// From the caller's last sound: when the answer was begun, then what each
	// vendor and each wait took. The parts are made to add up to the total;
	// whatever the log cannot account for is shown as such, not hidden.
	begun := pause + start
	if begun < 0 {
		begun = 0
	}
	add := func(key, label string, ms int) {
		if ms > 0 {
			turn.Stages = append(turn.Stages, Stage{Key: key, Label: label, MS: ms})
		}
	}
	if spec && start < 0 {
		wait := t.askPause
		if wait <= 0 || wait > begun {
			wait = begun - t.asrTook
		}
		if wait < 0 {
			wait = 0
		}
		rest := begun - wait
		add("wait", "Waiting into the pause before asking the recognizer", wait)
		add("asr", "Recognizer: the final for the words so far", rest)
	} else {
		add("pause", "Waiting for the pause that ends the turn", begun)
	}
	ttft := llm - start
	headers := t.headersMS
	if headers <= 0 || headers > ttft {
		headers = 0
	}
	add("llm_headers", "Chat model: request to response headers", headers)
	add("llm_token", "Chat model: to its first token", ttft-headers)
	add("clause", "Collecting the first clause", seg-llm)
	if t.firstIsCache {
		add("cache", "First clause played from memory", 0)
	}
	add("tts", "Synthesizer: first audio", tts-seg)
	add("hold", "Held until the turn was confirmed", out-tts)
	sum := 0
	for _, s := range turn.Stages {
		sum += s.MS
	}
	if diff := turn.TotalMS - sum; diff > 0 {
		add("other", "Not accounted for", diff)
	} else if diff < 0 && len(turn.Stages) > 0 {
		// Work that finished before the pause ran out: the wait was for the
		// pause, and the first stage absorbs it.
		turn.Stages[0].MS += diff
		if turn.Stages[0].MS < 0 {
			turn.Stages[0].MS = 0
		}
	}

	if !t.firstAudioAt.IsZero() {
		rel := tts
		if t.firstIsCache {
			rel = out
		}
		closed := t.firstAudioAt.Add(-time.Duration(rel) * time.Millisecond)
		turn.LastSoundS = cb.at(closed.Add(-time.Duration(pause) * time.Millisecond))
		turn.AudioOutS = cb.at(closed.Add(time.Duration(out) * time.Millisecond))
	}
	if t.interrupted && t.heardText != "" {
		turn.AssistantText = t.heardText
	}
	return turn
}

// guessCause names the stage that carries a slow turn's excess.
func guessCause(t Turn, medians []Stage) string {
	typical := map[string]int{}
	for _, s := range medians {
		typical[s.Key] = s.MS
	}
	worstKey, worstLabel, worst := "", "", 0
	for _, s := range t.Stages {
		key := s.Key
		if key == "llm_headers" || key == "llm_token" {
			continue
		}
		if excess := s.MS - typical[key]; excess > worst {
			worstKey, worstLabel, worst = key, s.Label, excess
		}
	}
	llmTypical := typical["llm_headers"] + typical["llm_token"]
	if excess := t.LLMFirstTokenMS - llmTypical; excess > worst {
		worstKey, worst = "llm", excess
	}
	switch {
	case worst < 100:
		return "Every stage a little slow; no single one stands out"
	case worstKey == "llm":
		return fmt.Sprintf("The chat model's first token took %d ms, %d more than this call's median", t.LLMFirstTokenMS, worst)
	case worstKey == "tts":
		note := ""
		if t.HedgeRefused {
			note = "; a second request was wanted and refused by the request budget"
		} else if t.HedgeAsked {
			note = "; a second connection was asked and did not win"
		}
		return fmt.Sprintf("The synthesizer's first audio took %d ms, %d more than this call's median%s", t.TTSFirstAudioMS, worst, note)
	case worstKey == "pause":
		return "No head start: the answer was begun only when the turn closed"
	default:
		return fmt.Sprintf("%s: %d ms more than this call's median", worstLabel, worst)
	}
}

// --- arithmetic ---

func percentile(sorted []int, p float64) int {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	k := int(float64(n)*p + 0.999)
	if k < 1 {
		k = 1
	}
	if k > n {
		k = n
	}
	return sorted[k-1]
}

func median(v []int) int {
	n := len(v)
	if n == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func medianF(v []float64) float64 {
	n := len(v)
	if n == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// Summarise computes a set of turns' figures and the verdict on the target.
func Summarise(turns []Turn) Stats {
	var total, reply []int
	st := Stats{Turns: len(turns)}
	for _, t := range turns {
		total = append(total, t.TotalMS)
		reply = append(reply, t.ReplyMS)
		if t.Over {
			st.Over++
		}
	}
	if st.Turns == 0 {
		st.Verdict = "too few"
		return st
	}
	sort.Ints(total)
	sort.Ints(reply)
	st.P50, st.P90, st.P95, st.Max = median(total), percentile(total, 0.9), percentile(total, 0.95), total[len(total)-1]
	st.ReplyP50, st.ReplyP90, st.ReplyP95 = median(reply), percentile(reply, 0.9), percentile(reply, 0.95)
	st.MissLow, st.MissHigh = missInterval(st.Over, st.Turns)
	st.Verdict = verdict(st)
	return st
}

// verdict applies the rule the work has been run by: a p95 target is "at most
// one turn in twenty over". Fewer than forty turns cannot say; a pooled figure
// inside the target is provisional until even the unlucky end of what the
// sample allows is inside it too.
func verdict(st Stats) string {
	switch {
	case st.Turns < MinTurnsForP95:
		if st.Turns >= 20 && float64(st.Over)/float64(st.Turns) > 0.15 {
			return "not met"
		}
		return "too few"
	case st.MissHigh <= 5.0:
		return "met"
	case st.P95 <= TargetMS:
		return "provisional"
	default:
		return "not met"
	}
}

// missInterval is the exact (Clopper–Pearson) 95% interval for the true share
// of turns over target given k misses in n, in percent. It is the interval the
// work's own rule was written with: no misses in 72 turns, or one in 110, is
// what it takes for the upper end to come inside 5%.
func missInterval(k, n int) (low, high float64) {
	if n == 0 {
		return 0, 100
	}
	// cdf is P(X <= k) for X ~ Binomial(n, p).
	cdf := func(k int, p float64) float64 {
		if k < 0 {
			return 0
		}
		if p <= 0 {
			return 1
		}
		if p >= 1 {
			if k >= n {
				return 1
			}
			return 0
		}
		logp, logq := math.Log(p), math.Log(1-p)
		sum := 0.0
		for i := 0; i <= k; i++ {
			lg, _ := math.Lgamma(float64(n + 1))
			la, _ := math.Lgamma(float64(i + 1))
			lb, _ := math.Lgamma(float64(n - i + 1))
			sum += math.Exp(lg - la - lb + float64(i)*logp + float64(n-i)*logq)
		}
		return sum
	}
	solve := func(f func(p float64) bool) float64 {
		lo, hi := 0.0, 1.0
		for i := 0; i < 60; i++ {
			mid := (lo + hi) / 2
			if f(mid) {
				lo = mid
			} else {
				hi = mid
			}
		}
		return (lo + hi) / 2
	}
	high = 1
	if k < n {
		// The largest p at which seeing k or fewer is still not implausible.
		high = solve(func(p float64) bool { return cdf(k, p) > 0.025 })
	}
	if k > 0 {
		// The smallest p at which seeing k or more is not implausible.
		low = solve(func(p float64) bool { return 1-cdf(k-1, p) < 0.025 })
	}
	return math.Round(low*1000) / 10, math.Round(high*1000) / 10
}

var stageOrder = []struct{ key, label string }{
	{"wait", "Waiting into the pause before asking the recognizer"},
	{"asr", "Recognizer: the final for the words so far"},
	{"pause", "Waiting for the pause that ends the turn"},
	{"llm_headers", "Chat model: request to response headers"},
	{"llm_token", "Chat model: to its first token"},
	{"clause", "Collecting the first clause"},
	{"tts", "Synthesizer: first audio"},
	{"hold", "Held until the turn was confirmed"},
	{"other", "Not accounted for"},
}

// stageMedians is each stage's median over the turns that have it.
func stageMedians(turns []Turn) []Stage {
	by := map[string][]int{}
	for _, t := range turns {
		for _, s := range t.Stages {
			by[s.Key] = append(by[s.Key], s.MS)
		}
	}
	var out []Stage
	for _, so := range stageOrder {
		if v := by[so.key]; len(v)*2 >= len(turns) && len(v) > 0 {
			out = append(out, Stage{Key: so.key, Label: so.label, MS: median(v)})
		}
	}
	return out
}
