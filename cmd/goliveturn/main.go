// goliveturn runs golive's turn decision over TurnBench's two-channel
// conversations and writes the predictions file its scorer reads.
//
// TurnBench (github.com/SesameAILabs/turnbench) scores two events per
// speaker on recorded human dialogue: EOT, the speaker's turn is over, and
// INT, the speaker takes the floor while the other holds it. A prediction is
// causal: its time is the audio heard by the moment the system committed.
//
// golive has no model that reads both channels at once; what it has is the
// stack that decides, on one caller's audio, when the turn is taken — the
// VAD, the early final, semantic endpointing, patience, the settled close —
// and a judgement of what is a backchannel. So each speaker's channel runs in
// a golive session of its own, detector only (client delegation: no chat
// model, no synthesizer), both fed in lockstep at real-time pace, and this
// program applies the one rule golive cannot see from inside one channel:
// who holds the floor.
//
//	EOT  = session.delegation.created on this channel (the turn is taken),
//	       unless the words were a backchannel said while the other speaker
//	       held the floor;
//	INT  = this channel's utterance opens while the other holds the floor,
//	       committed at the first words that are not a backchannel (or at
//	       the turn's end, whichever is first);
//	floor: a channel holds it from the onset of an utterance with words
//	       until its EOT — a mid-turn pause does not give it up.
//
// Times are the audio position appended to the session when the event
// arrived (causal by construction; with real-time pacing that is what the
// engine had heard). The score is turnbench's own: scripts/turnbench/score.py
// writes the journal's L1 record from it.
//
//	goliveturn -data DIR [-url ws://127.0.0.1:8090/v1/live] [-parallel 4] [-limit N] -out predictions.json
//
// DIR is what scripts/turnbench/export.py writes: <id>/speaker_1.wav and
// speaker_2.wav (16 kHz mono PCM16) and index.json.
package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
)

func main() {
	var (
		dataDir  = flag.String("data", "", "directory written by scripts/turnbench/export.py")
		url      = flag.String("url", "ws://127.0.0.1:8090/v1/live", "golive's live endpoint (configs/turnbench.json)")
		token    = flag.String("token", "", "bearer token, if the service wants one")
		parallel = flag.Int("parallel", 4, "conversations run at once (two sessions each)")
		limit    = flag.Int("limit", 0, "only the first N conversations (0 = all)")
		only     = flag.String("only", "", "one conversation id")
		out      = flag.String("out", "predictions.json", "predictions file for turnbench.score")
		grace    = flag.Duration("grace", 4*time.Second, "silence fed after the audio ends, for trailing events")
		verbose  = flag.Bool("v", false, "print each event")
		bcList   = flag.String("backchannels", "", "comma-separated backchannel words (default: configs/turnbench.json's)")
		profile  = flag.String("profile", "configs/turnbench.json", "profile to read the backchannel words from")
		dry      = flag.Bool("dry", false, "list the conversations and exit")
	)
	flag.Parse()
	if *dataDir == "" {
		log.Fatal("-data is required")
	}
	convs, err := readIndex(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	if *only != "" {
		var keep []conversation
		for _, c := range convs {
			if c.ID == *only {
				keep = append(keep, c)
			}
		}
		convs = keep
	}
	if *limit > 0 && len(convs) > *limit {
		convs = convs[:*limit]
	}
	if *dry {
		for _, c := range convs {
			fmt.Printf("%s\t%.1fs\n", c.ID, c.DurationS)
		}
		return
	}
	phrases := strings.Split(*bcList, ",")
	if *bcList == "" {
		phrases, err = profileBackchannels(*profile)
		if err != nil {
			log.Fatal(err)
		}
	}
	bc := duplex.NewPhraseSet(phrases)

	results := make([]prediction, len(convs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, *parallel)
	var failed int
	var mu sync.Mutex
	start := time.Now()
	for i, c := range convs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c conversation) {
			defer wg.Done()
			defer func() { <-sem }()
			p, err := runConversation(context.Background(), *url, *token, *dataDir, c, bc, *grace, *verbose)
			if err != nil {
				log.Printf("%s: %v", c.ID, err)
				mu.Lock()
				failed++
				mu.Unlock()
				p = prediction{ConversationID: c.ID, Speaker1: events{EOT: []float64{}, Interruption: []float64{}}, Speaker2: events{EOT: []float64{}, Interruption: []float64{}}}
			}
			results[i] = p
			log.Printf("%s: %.0fs, speaker_1 eot %d int %d, speaker_2 eot %d int %d", c.ID, c.DurationS,
				len(p.Speaker1.EOT), len(p.Speaker1.Interruption), len(p.Speaker2.EOT), len(p.Speaker2.Interruption))
		}(i, c)
	}
	wg.Wait()
	sub := submission{SchemaVersion: 1, Predictions: results}
	data, _ := json.MarshalIndent(sub, "", "  ")
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s: %d conversations (%d failed) in %s", *out, len(results), failed, time.Since(start).Round(time.Second))
	if failed > 0 {
		os.Exit(1)
	}
}

// --- the predictions file ----------------------------------------------------

type submission struct {
	SchemaVersion int          `json:"schema_version"`
	Predictions   []prediction `json:"predictions"`
}

type prediction struct {
	ConversationID string `json:"conversation_id"`
	Speaker1       events `json:"speaker_1"`
	Speaker2       events `json:"speaker_2"`
}

type events struct {
	EOT          []float64 `json:"eot"`
	Interruption []float64 `json:"interruption"`
}

// clean sorts, de-duplicates to the millisecond and clamps to the audio, as
// the scorer requires (strictly increasing, within the duration).
func clean(ts []float64, duration float64) []float64 {
	out := []float64{}
	sort.Float64s(ts)
	for _, t := range ts {
		if t > duration {
			t = duration
		}
		t = float64(int(t*1000+0.5)) / 1000
		if len(out) > 0 && t-out[len(out)-1] <= 1e-3 {
			continue
		}
		out = append(out, t)
	}
	return out
}

// --- the data ------------------------------------------------------------------

type conversation struct {
	ID        string  `json:"conversation_id"`
	DurationS float64 `json:"duration_s"`
}

func readIndex(dir string) ([]conversation, error) {
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	var convs []conversation
	if err := json.Unmarshal(data, &convs); err != nil {
		return nil, err
	}
	if len(convs) == 0 {
		return nil, errors.New("index.json lists no conversations")
	}
	return convs, nil
}

func profileBackchannels(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p struct {
		Duplex struct {
			Phrases []string `json:"user_backchannel_phrases"`
		} `json:"duplex"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p.Duplex.Phrases, nil
}

// readWAV returns the PCM16 mono samples of a 16 kHz WAV file. The exporter
// writes exactly that; anything else is refused rather than guessed at.
func readWAV(path string) ([]byte, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("%s: not a WAV file", path)
	}
	var rate, channels, bits int
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		if body+size > len(data) {
			size = len(data) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, fmt.Errorf("%s: short fmt chunk", path)
			}
			if binary.LittleEndian.Uint16(data[body:]) != 1 {
				return nil, 0, fmt.Errorf("%s: not PCM", path)
			}
			channels = int(binary.LittleEndian.Uint16(data[body+2:]))
			rate = int(binary.LittleEndian.Uint32(data[body+4:]))
			bits = int(binary.LittleEndian.Uint16(data[body+14:]))
		case "data":
			if channels != 1 || bits != 16 {
				return nil, 0, fmt.Errorf("%s: want mono 16-bit, have %d ch %d bit", path, channels, bits)
			}
			return data[body : body+size], rate, nil
		}
		pos = body + size + size%2
	}
	return nil, 0, fmt.Errorf("%s: no data chunk", path)
}

// --- one conversation -----------------------------------------------------------

const frameMS = 20

// channel is one speaker's session and what this program knows of it.
type channel struct {
	n    int
	sess *session
	// Decisions, in seconds of audio appended.
	eot, ints []float64

	// The open utterance, if any: whether it began while the other speaker
	// held the floor (an interruption candidate), and whether it has been
	// judged a backchannel.
	open        bool
	candidate   bool
	backchannel bool
	words       strings.Builder
	intPending  bool
	// floor: this speaker holds it (an utterance with words began and its
	// turn has not been taken).
	floor bool
}

func runConversation(ctx context.Context, url, token, dir string, c conversation, bc *duplex.PhraseSet, grace time.Duration, verbose bool) (prediction, error) {
	var pcm [2][]byte
	rate := 16000
	for i := 0; i < 2; i++ {
		p, r, err := readWAV(filepath.Join(dir, c.ID, fmt.Sprintf("speaker_%d.wav", i+1)))
		if err != nil {
			return prediction{}, err
		}
		if r != rate {
			return prediction{}, fmt.Errorf("speaker_%d.wav is %d Hz, want %d (export.py resamples)", i+1, r, rate)
		}
		pcm[i] = p
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	chans := [2]*channel{{n: 1}, {n: 2}}
	for i := range chans {
		s, err := dial(ctx, url, token, rate, fmt.Sprintf("tb-%s-%d", c.ID, i+1))
		if err != nil {
			return prediction{}, err
		}
		chans[i].sess = s
		defer s.close()
	}
	frame := rate * 2 * frameMS / 1000
	n := len(pcm[0])
	if len(pcm[1]) > n {
		n = len(pcm[1])
	}
	frames := (n+frame-1)/frame + int(grace/time.Millisecond)/frameMS
	silence := make([]byte, frame)
	ticker := time.NewTicker(frameMS * time.Millisecond)
	defer ticker.Stop()
	var appended float64 // seconds of audio sent to both sessions
	var mu sync.Mutex    // guards the channels' state across the two readers

	// The readers: each channel's events, applied under one lock with the
	// audio position at arrival, so the floor is one consistent story.
	handle := func(ch *channel, other *channel, ev incoming) {
		mu.Lock()
		defer mu.Unlock()
		at := appended
		switch ev.kind {
		case live.ExtSpeechStarted:
			ch.open = true
			ch.candidate = other.floor
			ch.backchannel = false
			ch.intPending = ch.candidate
			ch.words.Reset()
			if verbose {
				log.Printf("%s/%d %.2f onset (other floor %v)", c.ID, ch.n, at, other.floor)
			}
		case live.ServerInputTranscriptDelta:
			if !ch.open || ev.delta == "" {
				return
			}
			ch.words.WriteString(ev.delta)
			text := ch.words.String()
			if ch.intPending && !bc.CouldBecome(text) {
				ch.ints = append(ch.ints, at)
				ch.intPending = false
				ch.floor = true
				if verbose {
					log.Printf("%s/%d %.2f INT on %q", c.ID, ch.n, at, trim(text))
				}
			} else if !ch.candidate && !ch.floor && !bc.CouldBecome(text) {
				ch.floor = true
			}
		case live.ExtInputBackchannel:
			ch.backchannel = true
			ch.intPending = false
			if verbose {
				log.Printf("%s/%d %.2f backchannel %q", c.ID, ch.n, at, ev.text)
			}
		case live.ExtSpeechStopped:
			ch.open = false
		case live.ServerDelegationCreated:
			// The turn is taken. A backchannel said over the other speaker's
			// turn is not a turn and has no end.
			words := strings.TrimSpace(ev.transcript)
			if words == "" {
				ch.intPending = false
				if verbose {
					log.Printf("%s/%d %.2f turn with no words; ignored", c.ID, ch.n, at)
				}
				return
			}
			if ch.candidate && (ch.backchannel || bc.Matches(words)) {
				ch.intPending = false
				if verbose {
					log.Printf("%s/%d %.2f backchannel turn %q; no EOT", c.ID, ch.n, at, trim(words))
				}
				return
			}
			if ch.intPending {
				ch.ints = append(ch.ints, at)
				ch.intPending = false
			}
			ch.eot = append(ch.eot, at)
			ch.floor = false
			if verbose {
				log.Printf("%s/%d %.2f EOT %q", c.ID, ch.n, at, trim(words))
			}
		}
	}
	for i := range chans {
		ch, other := chans[i], chans[1-i]
		go func() {
			for ev := range ch.sess.events {
				handle(ch, other, ev)
			}
		}()
	}

	for f := 0; f < frames; f++ {
		select {
		case <-ctx.Done():
			return prediction{}, ctx.Err()
		case <-ticker.C:
		}
		for i, ch := range chans {
			buf := silence
			if off := f * frame; off < len(pcm[i]) {
				end := off + frame
				if end > len(pcm[i]) {
					buf = make([]byte, frame)
					copy(buf, pcm[i][off:])
				} else {
					buf = pcm[i][off:end]
				}
			}
			if err := ch.sess.append(buf); err != nil {
				return prediction{}, fmt.Errorf("speaker_%d: %w", i+1, err)
			}
			if err := ch.sess.err(); err != nil {
				return prediction{}, fmt.Errorf("speaker_%d: %w", i+1, err)
			}
		}
		mu.Lock()
		appended = float64(f+1) * frameMS / 1000
		if appended > c.DurationS {
			appended = c.DurationS
		}
		mu.Unlock()
	}
	mu.Lock()
	defer mu.Unlock()
	return prediction{
		ConversationID: c.ID,
		Speaker1:       events{EOT: clean(chans[0].eot, c.DurationS), Interruption: clean(chans[0].ints, c.DurationS)},
		Speaker2:       events{EOT: clean(chans[1].eot, c.DurationS), Interruption: clean(chans[1].ints, c.DurationS)},
	}, nil
}

func trim(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

// --- a session on /v1/live ---------------------------------------------------------

type incoming struct {
	kind       string
	delta      string
	text       string
	transcript string
}

type session struct {
	conn   *websocket.Conn
	sendMu sync.Mutex
	events chan incoming
	errMu  sync.Mutex
	lastErr error
}

func dial(ctx context.Context, url, token string, rate int, id string) (*session, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", url, err)
	}
	s := &session{conn: conn, events: make(chan incoming, 256)}
	quiet := ""
	start := live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: id},
		Session: live.SessionConfig{
			Instructions: "detector only",
			Audio:        &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: rate}},
			Delegation:   &live.DelegationConfig{Type: live.DelegationClient},
			Golive:       &live.GoliveConfig{Greeting: &quiet, ClientDelegation: "always"},
		},
	}
	if err := s.send(start); err != nil {
		conn.Close()
		return nil, err
	}
	ready := make(chan struct{})
	go s.read(ready)
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		conn.Close()
		return nil, errors.New("no session.started")
	case <-ctx.Done():
		conn.Close()
		return nil, ctx.Err()
	}
	return s, nil
}

func (s *session) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *session) append(pcm []byte) error {
	return s.send(map[string]any{"type": live.ClientInputAudioAppend, "audio": base64.StdEncoding.EncodeToString(pcm)})
}

func (s *session) err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.lastErr
}

func (s *session) close() {
	_ = s.send(map[string]any{"type": live.ClientSessionClose})
	s.conn.Close()
}

func (s *session) read(ready chan struct{}) {
	defer close(s.events)
	var once sync.Once
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		var env struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case live.ServerSessionStarted:
			once.Do(func() { close(ready) })
		case live.ExtSpeechStarted, live.ExtSpeechStopped:
			s.events <- incoming{kind: env.Type}
		case live.ServerInputTranscriptDelta:
			var ev live.TranscriptDelta
			_ = json.Unmarshal(data, &ev)
			s.events <- incoming{kind: env.Type, delta: ev.Delta}
		case live.ExtInputBackchannel:
			var ev live.InputBackchannelEvent
			_ = json.Unmarshal(data, &ev)
			s.events <- incoming{kind: env.Type, text: ev.Text}
		case live.ServerDelegationCreated:
			var ev live.DelegationCreatedEvent
			_ = json.Unmarshal(data, &ev)
			s.events <- incoming{kind: env.Type, transcript: ev.Delegation.Transcript}
		case live.ServerError:
			var ev struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &ev)
			s.errMu.Lock()
			s.lastErr = errors.New(ev.Error.Message)
			s.errMu.Unlock()
		}
	}
}
