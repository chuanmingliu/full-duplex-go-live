// Command golivectl drives a golive service from the command line: it opens a
// session, streams audio in, saves the audio that comes back, and prints the
// event trace.
//
// It exists because the interesting behaviour of a duplex engine is timing, and
// timing is miserable to verify by ear. With -barge-in-at you can cut the
// assistant off at a chosen millisecond and read back exactly how much of the
// turn it believes the listener heard.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "golivectl:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		url        = flag.String("url", "ws://127.0.0.1:8080/v1/live", "golive websocket endpoint")
		inPath     = flag.String("in", "", "16-bit PCM WAV file to stream as microphone input")
		outPath    = flag.String("out", "golivectl-out.wav", "where to write the assistant audio")
		recPath    = flag.String("record", "", "optional stereo recording: input left, output right")
		rate       = flag.Int("rate", 24000, "session PCM rate (8000, 16000, 24000 or 48000)")
		speakMS    = flag.Int("speak-ms", 1800, "length of synthetic speech when -in is not given")
		silenceMS  = flag.Int("silence-ms", 900, "trailing silence that closes the utterance")
		bargeInAt  = flag.Int("barge-in-at", 0, "ms after the first output audio to interrupt with new speech (0 disables)")
		bargeMS    = flag.Int("barge-ms", 1200, "length of the interrupting speech")
		delegation = flag.String("delegation", "responses", "delegation mode: responses or client")
		instr      = flag.String("instructions", "", "session instructions")
		asr        = flag.String("asr", "", "override the ASR provider for this session")
		llm        = flag.String("llm", "", "override the LLM provider for this session")
		tts        = flag.String("tts", "", "override the TTS provider for this session")
		verbose    = flag.Bool("v", false, "print every server event, not just the interesting ones")
		waitMS     = flag.Int("wait-ms", 6000, "how long to keep listening after the last audio is sent")
		token      = flag.String("token", "", "bearer token (GOLIVE_AUTH_TOKEN); also read from GOLIVE_AUTH_TOKEN")
	)
	flag.Parse()

	if *token == "" {
		*token = os.Getenv("GOLIVE_AUTH_TOKEN")
	}
	header := http.Header{}
	if *token != "" {
		header.Set("Authorization", "Bearer "+*token)
	}
	conn, _, err := websocket.DefaultDialer.Dial(*url, header)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", *url, err)
	}
	defer conn.Close()

	c := &client{conn: conn, rate: *rate, verbose: *verbose, firstAudio: make(chan struct{})}
	go c.readLoop()

	session := live.SessionConfig{
		Audio: &live.AudioConfig{
			Format: &live.AudioFormat{Type: "audio/pcm", Rate: *rate},
		},
		Delegation: &live.DelegationConfig{Type: *delegation},
		Golive:     &live.GoliveConfig{ASR: *asr, LLM: *llm, TTS: *tts},
	}
	if *instr != "" {
		session.Instructions = *instr
	}
	if err := c.send(live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "start_1"},
		Session:  session,
	}); err != nil {
		return err
	}

	// Gather the microphone side.
	var mic []byte
	if *inPath != "" {
		pcm, srcRate, err := audio.ReadWAV(*inPath)
		if err != nil {
			return err
		}
		mic, err = audio.ResamplePCM16(pcm, srcRate, *rate)
		if err != nil {
			return err
		}
	} else {
		mic = syntheticSpeech(*rate, *speakMS)
	}
	mic = append(mic, silence(*rate, *silenceMS)...)

	fmt.Printf("→ streaming %.2fs of input at %d Hz\n", audio.PCM16(*rate).DurationMS(mic)/1000, *rate)
	if err := c.streamPaced(mic); err != nil {
		return err
	}

	if *bargeInAt > 0 {
		select {
		case <-c.firstAudio:
		case <-time.After(10 * time.Second):
			return fmt.Errorf("no assistant audio arrived; nothing to interrupt")
		}
		time.Sleep(time.Duration(*bargeInAt) * time.Millisecond)
		fmt.Printf("→ interrupting after %dms of assistant audio\n", *bargeInAt)
		interrupt := append(syntheticSpeech(*rate, *bargeMS), silence(*rate, *silenceMS)...)
		if err := c.streamPaced(interrupt); err != nil {
			return err
		}
		mic = append(mic, interrupt...)
	}

	time.Sleep(time.Duration(*waitMS) * time.Millisecond)
	_ = c.send(live.Envelope{Type: live.ClientSessionClose, EventID: "close_1"})
	time.Sleep(200 * time.Millisecond)

	out := c.audio()
	if len(out) == 0 {
		fmt.Println("! no assistant audio was received")
	} else if err := audio.WriteWAV(*outPath, out, *rate); err != nil {
		return err
	} else {
		fmt.Printf("← wrote %s (%.2fs)\n", *outPath, audio.PCM16(*rate).DurationMS(out)/1000)
	}

	if *recPath != "" {
		if err := audio.WriteWAVStereo(*recPath, mic, out, *rate); err != nil {
			return err
		}
		fmt.Printf("← wrote %s (input left, output right)\n", *recPath)
	}

	c.summary()
	return nil
}

type client struct {
	conn    *websocket.Conn
	rate    int
	verbose bool

	mu         sync.Mutex
	writeMu    sync.Mutex
	out        []byte
	transcript strings.Builder
	reply      strings.Builder
	events     map[string]int
	firstOnce  sync.Once
	firstAudio chan struct{}
}

func (c *client) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// streamPaced sends audio in 20 ms frames at real time, the way a microphone
// would. Bursting it instead would let the engine finish a turn before the
// client has "spoken" it, and every timing observation would be meaningless.
func (c *client) streamPaced(pcm []byte) error {
	frame := audio.PCM16(c.rate).BytesForMS(20)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for off := 0; off < len(pcm); off += frame {
		end := off + frame
		if end > len(pcm) {
			end = len(pcm)
		}
		<-ticker.C
		if err := c.send(live.InputAudioAppendEvent{
			Envelope: live.Envelope{Type: live.ClientInputAudioAppend},
			Audio:    base64.StdEncoding.EncodeToString(pcm[off:end]),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *client) audio() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.out...)
}

func (c *client) readLoop() {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env live.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}

		c.mu.Lock()
		if c.events == nil {
			c.events = map[string]int{}
		}
		c.events[env.Type]++
		c.mu.Unlock()

		switch env.Type {
		case live.ServerOutputAudioDelta:
			var ev live.OutputAudioDelta
			if json.Unmarshal(data, &ev) == nil {
				pcm, err := base64.StdEncoding.DecodeString(ev.Audio)
				if err == nil {
					c.mu.Lock()
					c.out = append(c.out, pcm...)
					c.mu.Unlock()
					c.firstOnce.Do(func() { close(c.firstAudio) })
				}
			}
			if !c.verbose {
				continue
			}
		case live.ServerInputTranscriptDelta:
			var ev live.TranscriptDelta
			if json.Unmarshal(data, &ev) == nil {
				c.mu.Lock()
				if ev.Replace {
					c.transcript.Reset()
				}
				c.transcript.WriteString(ev.Content)
				c.mu.Unlock()
				if ev.Final {
					fmt.Printf("  user: %s\n", ev.Content)
				}
				if !c.verbose {
					continue
				}
			}
		case live.ServerOutputTranscriptDelta:
			var ev live.TranscriptDelta
			if json.Unmarshal(data, &ev) == nil {
				c.mu.Lock()
				c.reply.WriteString(ev.Content)
				c.mu.Unlock()
			}
			if !c.verbose {
				continue
			}
		case live.ExtChannelState:
			if !c.verbose {
				continue
			}
		}
		fmt.Printf("← %s\n", compact(data))
	}
}

func (c *client) summary() {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Println("\n--- summary ---")
	fmt.Printf("heard from user : %s\n", c.transcript.String())
	fmt.Printf("assistant text  : %s\n", c.reply.String())
	fmt.Printf("assistant audio : %.2fs\n", audio.PCM16(c.rate).DurationMS(c.out)/1000)
	fmt.Println("events:")
	for _, name := range sortedKeys(c.events) {
		fmt.Printf("  %-40s %d\n", name, c.events[name])
	}
}

func compact(data []byte) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(data)
	}
	s := string(out)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// syntheticSpeech makes a signal the energy VAD accepts as voiced: a low
// fundamental with harmonics and a syllable-rate envelope. It is not speech,
// but it has speech's energy and zero-crossing profile, which is all the
// detector looks at.
func syntheticSpeech(rate, ms int) []byte {
	n := rate * ms / 1000
	samples := make([]float32, n)
	for i := range samples {
		t := float64(i) / float64(rate)
		env := 0.62 + 0.38*math.Sin(2*math.Pi*4.5*t) // ~4.5 syllables/second
		v := 0.45 * env * (math.Sin(2*math.Pi*140*t) +
			0.5*math.Sin(2*math.Pi*280*t) +
			0.25*math.Sin(2*math.Pi*560*t)) / 1.75
		samples[i] = float32(v)
	}
	return audio.EncodePCM16(samples)
}

func silence(rate, ms int) []byte {
	return make([]byte, audio.PCM16(rate).BytesForMS(ms))
}
