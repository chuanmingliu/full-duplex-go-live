package speko

import (
	"encoding/binary"
	"strings"
	"time"
	"unicode"

	"github.com/chuanmingliu/golive/internal/audio"
)

// goodbyeGate accepts only a small set of complete, reciprocal farewells.
// Transcript fragments alone are never an end-of-turn or a hangup command.
// Unknown/quoted/negated phrases fail closed, and new caller speech cancels it.
type goodbyeGate struct {
	input, output                  string
	inputAt, outputAt, callerAudio time.Time
}

func farewell(text string) bool {
	if strings.ContainsAny(text, "\"'“”‘’「」『』?？") {
		return false
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
	switch text {
	case "拜拜", "嗯拜拜", "好拜拜", "好的拜拜", "谢谢拜拜", "再见", "嗯再见", "好再见", "好的再见", "谢谢再见",
		"bye", "byebye", "goodbye", "okbye", "okaybye", "thankyoubye":
		return true
	}
	return false
}

func (g *goodbyeGate) caller(text string, now time.Time) {
	if now.Sub(g.inputAt) > 1200*time.Millisecond {
		g.input = ""
	}
	// Any additional caller text invalidates an earlier assistant farewell.
	g.output = ""
	g.outputAt = time.Time{}
	if len(g.input)+len(text) <= 1024 {
		g.input += text
	} else {
		g.input = "invalid farewell"
	}
	g.inputAt = now
}

func (g *goodbyeGate) assistant(text string, now time.Time) {
	if !farewell(g.input) || now.Sub(g.inputAt) > 15*time.Second {
		return
	}
	if len(g.output)+len(text) <= 1024 {
		g.output += text
	} else {
		g.output = "invalid farewell"
	}
	g.outputAt = now
}

func (g *goodbyeGate) audio(wire []byte, codec audio.Codec, now time.Time) {
	pcm := codec.ToPCM16(wire)
	var energy int64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		energy += v * v
	}
	if len(wire) == 0 || energy < int64(len(wire))*400*400 {
		return
	}
	g.callerAudio = now
	if g.output != "" {
		g.input, g.output = "", ""
	}
}

func (g *goodbyeGate) ready(now, lastPlayback time.Time, silentQueue bool) bool {
	return silentQueue && !lastPlayback.IsZero() && !lastPlayback.Before(g.outputAt) && farewell(g.input) && farewell(g.output) &&
		now.Sub(g.inputAt) >= time.Second && now.Sub(g.inputAt) <= 15*time.Second &&
		now.Sub(g.outputAt) >= time.Second && now.Sub(g.callerAudio) >= time.Second &&
		now.Sub(lastPlayback) >= 800*time.Millisecond
}
