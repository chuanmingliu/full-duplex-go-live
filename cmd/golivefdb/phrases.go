package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// The caller's vocabulary. Against the mock recognizer every utterance is a
// tone keyed to one of these (mock.PhraseHz), so what the engine hears is
// decided by the audio alone: the same path a phone caller's words take, with
// nothing passed on the side. Against a real recognizer (-speak) the same
// phrases are synthesized as speech by the service's own voice.
//
// The order is the key. Adding a phrase at the end is safe; reordering
// changes what every existing recording means, and configs/fdb-sim.env must
// be regenerated (-print-env) either way.
var phrases = []string{
	"帮我查一下车险一年大概多少钱", // 0  a question that asks for a lookup ("查" makes the mock backend call a tool)
	"我想问一下",    // 1  an opening that is not finished
	"车险一年多少钱",  // 2  the rest of it, or a question on its own
	"嗯",        // 3  a murmur
	"对对",       // 4  a murmur
	"呃",        // 5  a hesitation, the vocal tic
	"等一下我接个电话", // 6  said to someone else
	"那交强险呢",    // 7  a follow-up question, used to interrupt
	"你好",       // 8  an opener
	"我的车是去年买的大概二十万", // 9  a statement that invites more
	"可以",           // 10 a short reply
	"好的那商业险呢",      // 11 a follow-up question
	"先别说",          // 12 said to someone else, short
	"我想换个方案把车损险去掉", // 13 a request that changes the plan
	"好",            // 14 a murmur
}

// Phrase indices, so scenarios read as what is said.
const (
	pLookup = iota
	pOpening
	pRest
	pMm
	pDuidui
	pEr
	pAside
	pFollowUp
	pHello
	pStatement
	pOK
	pFollowUp2
	pAsideShort
	pChange
	pHao
)

// msPerRune is the mock recognizer's pace: one character per this much
// voiced audio. A phrase spoken for less than its runes need is heard short.
const msPerRune = 180

// phraseText looks a phrase up and panics on a bad index, which is a
// programming error in a scenario, not a run-time condition.
func phraseText(k int) string {
	if k < 0 || k >= len(phrases) {
		panic(fmt.Sprintf("no phrase %d", k))
	}
	return phrases[k]
}

// naturalMS is how long a phrase takes to say at the recognizer's pace, with
// a floor so a one-character murmur still lasts as long as a real one does
// (and clears the detector's barge-in minimum of 320 ms).
func naturalMS(k int) int {
	ms := len([]rune(phraseText(k))) * msPerRune
	if ms < 400 {
		ms = 400
	}
	return ms
}

// envLine is the GOLIVE_MOCK_PHRASES setting the recognizer needs to hear
// this vocabulary; configs/fdb-sim.env carries it.
func envLine() string {
	return "GOLIVE_MOCK_PHRASES=" + strings.Join(phrases, "|")
}

// toneFor is ms of the caller "saying" phrase k: a sine at the phrase's key
// pitch under a syllable-rate envelope that never reaches zero (so the
// zero-crossing count stays exactly the pitch), at about -14 dBFS RMS — well
// over the detector's bar even with the barge-in margin raised.
func toneFor(k int, ms, rate int) []byte {
	hz := mock.PhraseHz(k)
	n := rate * ms / 1000
	buf := make([]float32, n)
	for i := range buf {
		t := float64(i) / float64(rate)
		env := 0.6 + 0.4*math.Sin(2*math.Pi*4.5*t)
		// A short fade at either end keeps the detector from seeing a click.
		fade := 1.0
		if edge := 0.01 * float64(rate); float64(i) < edge {
			fade = float64(i) / edge
		} else if rem := float64(n - 1 - i); rem < edge {
			fade = rem / edge
		}
		buf[i] = float32(0.3 * env * fade * math.Sin(2*math.Pi*hz*t))
	}
	return audio.EncodePCM16(buf)
}
