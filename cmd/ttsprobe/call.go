package main

// call.go paces synthesis the way one phone call does, which is the question
// the bench cannot answer about a rate limit.
//
// The bench opens a session for every turn. With hedging on that is two
// connections and three or four requests a turn, thirteen turns a minute, and
// it ran into the account's requests-per-minute limit within two minutes. A
// call is one session: two connections for its whole length, and a turn every
// several seconds. Whether *that* fits under the limit decides whether hedging
// can be left on for calls, and it is not something to find out on one.
//
//	ttsprobe -call 30 -every 6s -hedge 2

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/minimax"
)

// callTurns are answers as golive segments them: a short opening clause, the
// one a caller waits on, and the sentence after it.
var callTurns = [][2]string{
	{"好的，", "我这就帮您查一下明天上午飞上海的航班。"},
	{"您点开链接，", "按提示输入车牌号，就能实时看到报价了。"},
	{"理解您的想法，", "不过费用是根据每辆车的车型和出险情况算出来的。"},
	{"已经发过去了，", "您打开微信，在服务通知里看一下。"},
	{"这个真没法估，", "每辆车情况不同，报出来不准反而误导您。"},
}

func runCall(base *minimax.TTS, voice string, rate, turns, hedge int, every, after time.Duration, accountRPM int) error {
	c := *base
	if voice != "" {
		// A voice given by hand is asked of every account as it is.
		c.VoiceID, c.VoiceIDs = voice, nil
	}
	// The hedge says, at debug level, each time it asks the second connection
	// and which one spoke first.
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	// On a pool the first segment is asked of both accounts at once, as a
	// session is; -after then applies to one account's two connections only.
	tts := provider.HedgeTTSPool(&c, hedge, 0, after, provider.TTSPool{AccountRPM: accountRPM}, log)
	if c.Accounts() > 1 {
		fmt.Printf("pool of %d accounts; each trusted with %d requests a minute (0 is no limit)\n", c.Accounts(), accountRPM)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := tts.Open(ctx, provider.TTSOptions{SampleRate: rate, Voice: c.VoiceID})
	if err != nil {
		return err
	}
	defer stream.Close()

	if hedge >= 2 && after > 0 {
		fmt.Printf("ttsprobe -call: one session, %d turns, one every %s, the second connection asked after %s without audio (3x that for a second segment)\n",
			turns, every, after)
		fmt.Printf("requests a minute: about %.0f, plus one for each time the second connection is asked\n\n",
			2*float64(time.Minute)/float64(every))
	} else {
		fmt.Printf("ttsprobe -call: one session, %d turns, one every %s, %d connection(s) asked for each first segment\n",
			turns, every, max(hedge, 1))
		fmt.Printf("requests a minute: about %.0f\n\n", float64(max(hedge, 1)+1)*float64(time.Minute)/float64(every))
	}
	time.Sleep(time.Second) // the second connection dials in the background

	var firsts []int
	failed := 0
	began := time.Now()
	for i := 0; i < turns; i++ {
		tick := time.Now()
		turn := callTurns[i%len(callTurns)]
		ms, bytes, err := synthOnce(provider.WithUrgent(ctx), stream, turn[0])
		line := fmt.Sprintf("%3d  %5.0fs  first segment: first audio %4d ms, %6d bytes", i+1, time.Since(began).Seconds(), ms, bytes)
		if err != nil {
			failed++
			line = fmt.Sprintf("%3d  %5.0fs  first segment FAILED: %v", i+1, time.Since(began).Seconds(), err)
		} else {
			firsts = append(firsts, ms)
		}
		if _, _, err := synthOnce(ctx, stream, turn[1]); err != nil {
			failed++
			line += fmt.Sprintf("   second segment FAILED: %v", err)
		}
		fmt.Println(line)
		if strings.Contains(line, "FAILED") {
			// What a call would do: drop the session and open another.
			stream.Close()
			if stream, err = tts.Open(ctx, provider.TTSOptions{SampleRate: rate, Voice: c.VoiceID}); err != nil {
				return fmt.Errorf("reopening after a failure: %w", err)
			}
		}
		if wait := every - time.Since(tick); wait > 0 && i < turns-1 {
			time.Sleep(wait)
		}
	}

	sort.Ints(firsts)
	fmt.Println()
	if n := len(firsts); n > 0 {
		over := 0
		for _, v := range firsts {
			if v > 300 {
				over++
			}
		}
		fmt.Printf("first audio of the first segment: min %d  p50 %d  p90 %d  max %d ms;  %d of %d over 300\n",
			firsts[0], firsts[n/2], firsts[min(n-1, n*9/10)], firsts[n-1], over, n)
	}
	fmt.Printf("failed syntheses: %d of %d\n", failed, turns*2)
	return nil
}

func synthOnce(ctx context.Context, stream provider.TTSStream, text string) (firstMS, bytes int, err error) {
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	start := time.Now()
	ch, err := stream.Synthesize(sctx, text)
	if err != nil {
		return 0, 0, err
	}
	firstMS = -1
	for chunk := range ch {
		if chunk.Err != nil {
			err = chunk.Err
			continue
		}
		if firstMS < 0 && len(chunk.PCM) > 0 {
			firstMS = int(time.Since(start).Milliseconds())
		}
		bytes += len(chunk.PCM)
	}
	if err == nil && bytes == 0 {
		err = fmt.Errorf("no audio")
	}
	return firstMS, bytes, err
}

// runCallOn paces any vendor's synthesizer like a call: one session, a turn
// every `every`, no hedging. What it says is the vendor's own first audio
// and whether it refuses at this pace.
func runCallOn(tts provider.TTS, voice string, rate, turns int, every time.Duration) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if p, ok := tts.(provider.Prewarmer); ok {
		if err := p.Prewarm(ctx); err != nil {
			fmt.Printf("prewarm failed: %v\n", err)
		}
	}
	stream, err := tts.Open(ctx, provider.TTSOptions{SampleRate: rate, Voice: voice})
	if err != nil {
		return err
	}
	defer stream.Close()
	fmt.Printf("one session, %d turns, one every %s, one request per segment: about %.0f requests a minute\n\n",
		turns, every, 2*float64(time.Minute)/float64(every))
	var firsts, seconds []int
	failed := 0
	began := time.Now()
	for i := 0; i < turns; i++ {
		tick := time.Now()
		turn := callTurns[i%len(callTurns)]
		ms, bytes, err := synthOnce(provider.WithUrgent(ctx), stream, turn[0])
		line := fmt.Sprintf("%3d  %5.0fs  first segment: first audio %4d ms, %6d bytes", i+1, time.Since(began).Seconds(), ms, bytes)
		if err != nil {
			failed++
			line = fmt.Sprintf("%3d  %5.0fs  first segment FAILED: %v", i+1, time.Since(began).Seconds(), err)
		} else {
			firsts = append(firsts, ms)
		}
		ms2, bytes2, err := synthOnce(ctx, stream, turn[1])
		if err != nil {
			failed++
			line += fmt.Sprintf("   second segment FAILED: %v", err)
		} else {
			seconds = append(seconds, ms2)
			line += fmt.Sprintf("   second: %4d ms, %6d bytes", ms2, bytes2)
		}
		fmt.Println(line)
		if wait := every - time.Since(tick); wait > 0 && i < turns-1 {
			time.Sleep(wait)
		}
	}
	for _, set := range []struct {
		name string
		v    []int
	}{{"first segment", firsts}, {"second segment", seconds}} {
		sort.Ints(set.v)
		if n := len(set.v); n > 0 {
			over := 0
			for _, v := range set.v {
				if v > 300 {
					over++
				}
			}
			fmt.Printf("first audio of the %s: min %d  p50 %d  p90 %d  max %d ms;  %d of %d over 300\n",
				set.name, set.v[0], set.v[n/2], set.v[min(n-1, n*9/10)], set.v[n-1], over, n)
		}
	}
	fmt.Printf("failed syntheses: %d of %d\n", failed, turns*2)
	return nil
}
