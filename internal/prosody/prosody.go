// Package prosody turns a reply into speech markup: the pause markers and
// interjection tags MiniMax renders, and the sanitizer that keeps everything
// else out of the text handed to a TTS provider.
//
// The package exists because inline markup is the only per-sentence prosody
// control the T2A API offers. voice_setting — speed, pitch, emotion — is fixed
// for the life of a synthesis task, so a persistent task_start/task_continue
// connection cannot vary it between turns without being torn down and paid for
// again. Everything expressive therefore has to travel inside the text.
//
// Three modes produce that markup:
//
//	prompt — the model writes the markers, instructed by PromptBlock.
//	rules  — Inject adds them deterministically, from a profile rule set.
//	both   — the model writes them and rules fill the gaps.
//
// All three converge on Sanitize, which is the function that must never be
// skipped. An unrecognised parenthesised word reaches the caller's ear as
// "open paren yawns close paren"; two adjacent pause markers are rejected by
// the API outright; and a segment that is nothing but markup synthesizes to no
// audio at all, which the adapter reports as a zero-chunk synthesis and is
// indistinguishable from a cancelled one.
package prosody

import (
	"fmt"
	"sort"
	"strings"
)

// Mode selects which layer writes the markup.
type Mode string

const (
	ModeOff    Mode = "off"
	ModePrompt Mode = "prompt"
	ModeRules  Mode = "rules"
	ModeBoth   Mode = "both"
)

// PromptBlock is appended to the system prompt in ModePrompt and ModeBoth.
//
// It is deliberately short. Measured on this stack, time-to-first-token tracks
// prompt size closely enough that every hundred characters is worth arguing
// about, and the persona prompt is already the largest latency term in the
// cascade. The last two lines carry most of the value: without "most replies
// need none" a model tags every sentence, and the agent sounds unwell.
const PromptBlock = `Speech markup. Your text is spoken aloud. You may use:
  <#N#>   pause of N seconds, N between 0.15 and 0.8, two decimals max.
          Place between words, never two in a row, never at the start or end.
  (sighs) (laughs) (chuckle) (breath) (clear-throat) (emm)
          A real sound, not a word. Max one per reply. Place after the
          clause it reacts to.
Use at most two markers per reply. Most replies need none.
Never use any other parenthesised word. Never use markdown.`

// API limits on a pause marker, in milliseconds. The format allows two decimal
// places of a second, so anything finer is silently lost.
const (
	APIMinPauseMS = 10
	APIMaxPauseMS = 99990
)

// tagDurationMS is how long each sound tag takes to speak.
//
// These are placeholders, and the only numbers in this package that should not
// be trusted as written: they vary by voice. Measure them for the voice you
// ship with `ttsprobe -tag-timing`, which synthesizes a carrier sentence with
// and without each tag and reports the delta. They matter because SpokenPrefix
// uses them to decide what the caller actually heard before a barge-in, and an
// over-estimate writes words into history that were never spoken.
var tagDurationMS = map[string]float64{
	"laughs":       700,
	"chuckle":      550,
	"sighs":        600,
	"breath":       350,
	"inhale":       350,
	"exhale":       400,
	"clear-throat": 500,
	"coughs":       450,
	"gasps":        350,
	"groans":       600,
	"sniffs":       350,
	"emm":          400,
}

// defaultTagDurationMS is used for a known tag with no measurement yet.
const defaultTagDurationMS = 450

// KnownTag reports whether name is a sound tag this package will let through
// to a provider. Everything else is stripped, whatever wrote it.
func KnownTag(name string) bool {
	_, ok := tagDurationMS[strings.ToLower(name)]
	return ok
}

// Tags returns the sound tags this package will let through, sorted. It is
// what `ttsprobe -tag-timing` iterates over to measure them.
func Tags() []string {
	out := make([]string, 0, len(tagDurationMS))
	for name := range tagDurationMS {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TagDurationMS returns the expected spoken length of a sound tag.
func TagDurationMS(name string) float64 {
	if ms, ok := tagDurationMS[strings.ToLower(name)]; ok {
		return ms
	}
	return defaultTagDurationMS
}

// TagsSupported reports whether a TTS model renders sound tags. Only the
// speech-2.8 line does; on 2.6 and 02 a tag is read out as words, so the
// sanitizer needs to know which model the text is bound for.
func TagsSupported(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "speech-2.8")
}

// Config is the profile's prosody block. It carries no credentials and no
// endpoint, so it is committable alongside the rest of a profile.
type Config struct {
	Mode Mode `json:"mode"`
	// MaxMarkersPerReply caps markers per reply, counting any the model wrote
	// itself. Two is about the ceiling before a reply sounds performed.
	MaxMarkersPerReply int `json:"max_markers_per_reply"`
	// PauseClampMS bounds every pause marker, whoever wrote it. The upper
	// bound is the one that matters live: a pause is dead air already queued
	// in the player, and barge-in cannot shorten it.
	PauseClampMS [2]int `json:"pause_clamp_ms"`
	// TagCooldownTurns is how many replies must pass between sound tags. It is
	// what stops the agent sighing three turns running, which is the usual
	// complaint about rule-injected prosody.
	TagCooldownTurns int `json:"tag_cooldown_turns"`
	// Rules are evaluated in order; the earliest wins a contested position.
	Rules []Rule `json:"rules"`
}

// Default is the starting point: rules on, conservative budget, the five rules
// that cover most of the value without needing a model that cooperates.
func Default() Config {
	return Config{
		Mode:               ModeRules,
		MaxMarkersPerReply: 2,
		PauseClampMS:       [2]int{150, 800},
		TagCooldownTurns:   3,
		Rules: []Rule{
			{When: WhenReplyStartsWith, Arg: []any{"well", "so", "actually", "okay"},
				Insert: "<#0.3#>", At: AtAfterMatch},
			{When: WhenToolLatencyOver, Arg: 800,
				Insert: "<#0.5#>", At: AtAfterFirstClause},
			{When: WhenContainsAny, Arg: []any{"unfortunately", "i'm afraid", "can't", "unable"},
				Insert: "(sighs)", At: AtEndOfMatchedSentence},
			{When: WhenEndsWithQuestion,
				Insert: "<#0.4#>", At: AtBeforeFinalClause},
			{When: WhenSentenceCountOver, Arg: 3,
				Insert: "<#0.45#>", At: AtBeforeLastSentence},
		},
	}
}

// Enabled reports whether any markup layer is active.
func (c Config) Enabled() bool { return c.Mode != "" && c.Mode != ModeOff }

// PromptAddition returns the text to append to the system prompt, or "" when
// the mode does not ask the model for markup.
func (c Config) PromptAddition() string {
	if c.Mode == ModePrompt || c.Mode == ModeBoth {
		return PromptBlock
	}
	return ""
}

// Validate rejects a profile that would fail at runtime rather than at load.
func (c Config) Validate() error {
	switch c.Mode {
	case "", ModeOff, ModePrompt, ModeRules, ModeBoth:
	default:
		return fmt.Errorf("prosody: mode %q must be off, prompt, rules or both", c.Mode)
	}
	lo, hi := c.PauseClampMS[0], c.PauseClampMS[1]
	if lo != 0 || hi != 0 {
		if lo > hi {
			return fmt.Errorf("prosody: pause_clamp_ms %d exceeds %d", lo, hi)
		}
		if lo < APIMinPauseMS || hi > APIMaxPauseMS {
			return fmt.Errorf("prosody: pause_clamp_ms must sit within [%d, %d] ms", APIMinPauseMS, APIMaxPauseMS)
		}
	}
	if c.MaxMarkersPerReply < 0 {
		return fmt.Errorf("prosody: max_markers_per_reply must not be negative")
	}
	if c.TagCooldownTurns < 0 {
		return fmt.Errorf("prosody: tag_cooldown_turns must not be negative")
	}
	for i, r := range c.Rules {
		if err := r.validate(); err != nil {
			return fmt.Errorf("prosody: rule %d: %v", i, err)
		}
	}
	return nil
}
