package main

// pool.go asks every account of the pool for the same clause at the same
// moment, round after round, and prints each one's time to first audio.
//
// It answers what a second account is worth before a call depends on it:
//
//   - can the account speak in the configured voice at all (a cloned or
//     designed voice belongs to the account that made it, so each account is
//     asked for it by its own id: MINIMAX_TTS_VOICE_ID_2 for the second);
//   - do the accounts sound the same: -save writes the same sentence from
//     each, to be listened to before a caller hears both;
//   - how fast is each account by itself, and how fast is the faster of the
//     two — the figure a turn would wait for if both were asked;
//   - when one account is slow, is the other slow in the same round. If they
//     are, the slowness is the vendor's or the path's and a second account
//     does not cure it; if they are not, it does.
//
//	ttsprobe -pool 30 -every 8s
//
// Each round is one request to each account, so -every 8s is 7.5 requests a
// minute to each: under an allowance of 10.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/minimax"
)

// slowFirstAudio is where a first audio starts to be heard as a slow turn:
// about twice the usual.
const slowFirstAudio = 400

func runPool(base *minimax.TTS, voice string, rate, rounds int, every time.Duration, saveDir string) error {
	n := base.Accounts()
	// With no -voice each account is asked for the configured voice by its own
	// name for it (MINIMAX_TTS_VOICE_ID_2 …), as a session is.
	fmt.Printf("ttsprobe -pool: %d account(s), %d rounds, one every %s (%.1f requests a minute to each)\n\n",
		n, rounds, every, float64(time.Minute)/float64(every))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streams := make([]provider.TTSStream, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		s, err := base.OnAccount(i).Open(ctx, provider.TTSOptions{SampleRate: rate, Voice: voice})
		if err != nil {
			fmt.Printf("account %d: cannot open a session: %v\n", i+1, err)
			if !base.OwnVoice(i) && voice == "" {
				fmt.Printf("account %d: it was asked for the first account's voice id; if the voice was made on that account, make it on this one too and set MINIMAX_TTS_VOICE_ID_%d\n", i+1, i+1)
			}
			continue
		}
		defer s.Close()
		streams[i] = s
		whose := "its own id for the voice"
		if voice != "" {
			whose = "the voice given with -voice"
		} else if !base.OwnVoice(i) {
			whose = "the first account's voice id (no MINIMAX_TTS_VOICE_ID_" + fmt.Sprint(i+1) + ")"
		}
		fmt.Printf("account %d: session open in %d ms, with %s\n", i+1, time.Since(start).Milliseconds(), whose)
	}
	fmt.Println()
	usable := 0
	for _, s := range streams {
		if s != nil {
			usable++
		}
	}
	if usable == 0 {
		return fmt.Errorf("no account could open a session")
	}

	per := make([][]int, n)
	var fastest, firstAlone []int
	failed := make([]int, n)
	bothSlow, anySlow := 0, 0
	var notes []string
	began := time.Now()
	for r := 0; r < rounds; r++ {
		tick := time.Now()
		text := callTurns[r%len(callTurns)][0]
		ms := make([]int, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i, s := range streams {
			if s == nil {
				ms[i] = -1
				continue
			}
			wg.Add(1)
			go func(i int, s provider.TTSStream) {
				defer wg.Done()
				ms[i], _, errs[i] = synthOnce(ctx, s, text)
			}(i, s)
		}
		wg.Wait()

		line := fmt.Sprintf("%3d  %5.0fs ", r+1, time.Since(began).Seconds())
		best, slow, answered := -1, 0, 0
		for i := range streams {
			switch {
			case streams[i] == nil:
				line += "       —  "
			case errs[i] != nil || ms[i] < 0:
				failed[i]++
				line += "  FAILED  "
				if errs[i] != nil {
					notes = append(notes, fmt.Sprintf("account %d, round %d: %v", i+1, r+1, errs[i]))
				}
			default:
				answered++
				per[i] = append(per[i], ms[i])
				line += fmt.Sprintf("  %5d ms", ms[i])
				if best < 0 || ms[i] < best {
					best = ms[i]
				}
				if ms[i] >= slowFirstAudio {
					slow++
				}
			}
		}
		if best >= 0 {
			fastest = append(fastest, best)
			line += fmt.Sprintf("   fastest %4d", best)
		}
		if errs[0] == nil && ms[0] >= 0 {
			firstAlone = append(firstAlone, ms[0])
		}
		if slow > 0 {
			anySlow++
			if slow == answered && answered > 1 {
				bothSlow++
				line += "   every account slow"
			}
		}
		fmt.Println(line)
		if wait := every - time.Since(tick); wait > 0 && r < rounds-1 {
			time.Sleep(wait)
		}
	}

	fmt.Println()
	for _, note := range notes {
		fmt.Println(note)
	}
	for i := range streams {
		if streams[i] == nil {
			fmt.Printf("account %d: no session\n", i+1)
			continue
		}
		fmt.Printf("account %d: %s;  failed %d\n", i+1, spread(per[i]), failed[i])
	}
	if usable > 1 {
		fmt.Printf("the faster of them, each round: %s\n", spread(fastest))
		fmt.Printf("account 1 alone, the same rounds: %s\n", spread(firstAlone))
		fmt.Printf("rounds with a first audio of %d ms or more on some account: %d of %d;  on every account at once: %d\n",
			slowFirstAudio, anySlow, rounds, bothSlow)
	}
	if saveDir != "" {
		// The same sentence from each account, to be listened to side by side:
		// two clones are two voices, and which account speaks changes from
		// turn to turn on a call.
		if err := os.MkdirAll(saveDir, 0o700); err != nil {
			return err
		}
		fmt.Println()
		for i, s := range streams {
			if s == nil {
				continue
			}
			pcm, err := synthAll(ctx, s, saveSentence)
			if err != nil || len(pcm) == 0 {
				fmt.Printf("account %d: nothing to save: %v\n", i+1, err)
				continue
			}
			path := filepath.Join(saveDir, fmt.Sprintf("account-%d.wav", i+1))
			if err := audio.WriteWAV(path, pcm, s.SampleRate()); err != nil {
				return err
			}
			fmt.Printf("account %d: %.1f s of the same sentence saved to %s\n", i+1, float64(len(pcm))/2/float64(s.SampleRate()), path)
		}
	}
	return nil
}

// saveSentence is what each account says for -save: long enough to hear a
// voice in, and the kind of thing the assistant says.
const saveSentence = "好的，您点开链接，按提示输入车牌号，就能实时看到报价了。如果还有不清楚的地方，随时跟我说。"

func synthAll(ctx context.Context, stream provider.TTSStream, text string) ([]byte, error) {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ch, err := stream.Synthesize(sctx, text)
	if err != nil {
		return nil, err
	}
	var pcm []byte
	for chunk := range ch {
		if chunk.Err != nil {
			err = chunk.Err
			continue
		}
		pcm = append(pcm, chunk.PCM...)
	}
	return pcm, err
}

func spread(v []int) string {
	if len(v) == 0 {
		return "no first audio"
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	n := len(s)
	return fmt.Sprintf("first audio min %d  p50 %d  p90 %d  max %d ms (%d)", s[0], s[n/2], s[min(n-1, n*9/10)], s[n-1], n)
}
