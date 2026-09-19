package prosody

// Warm is a rule set for a service agent talking to a customer.
//
// It exists because the obvious rule set is wrong for this job. Default()
// answers bad news with (sighs), which is right for a companion or an
// assistant speaking about a shared problem — and wrong on a support line,
// where a sigh is aimed at whoever is listening. A caller who has just been
// told their flight is full hears the agent sigh and concludes the agent is
// tired of them. The tag is doing exactly what it should; the situation is
// the problem.
//
// So this set carries no (sighs) at all, and answers bad news with silence
// instead. A beat *before* the sentence reads as care — the agent gathering
// themselves to say something they would rather not. The same beat after it
// reads as reaction. That is the whole difference, and it is a placement, not
// a tag.
//
// The tags it does use are the involuntary, unperformed ones: a breath before
// a long sentence, a thinking sound while something is being looked up, a
// small laugh where the caller has been light. None of them is an opinion
// about the caller.
//
// Both languages are covered, because the trigger words are the part that
// silently does nothing when the agent speaks a language the rule set does not.
func Warm() Config {
	return Config{
		Mode:               ModeRules,
		MaxMarkersPerReply: 2,
		PauseClampMS:       [2]int{150, 800},
		// Rarer than Default's three: on a support call a tag that recurs is
		// noticed, and being noticed is the failure.
		TagCooldownTurns: 4,
		Rules: []Rule{
			// A beat before bad news. Never a sigh after it.
			{When: WhenContainsAny, Arg: []any{
				"抱歉", "不好意思", "恐怕", "无法", "没办法", "已经满", "不能",
				"unfortunately", "afraid", "can't", "can’t", "cannot", "unable", "sorry",
			}, Insert: "<#0.5#>", At: AtBeforeMatch},

			// Reassurance, slowed down. "Don't worry about it" said at full
			// speed is heard as brushing the worry aside.
			{When: WhenContainsAny, Arg: []any{
				"别担心", "不用担心", "放心", "我来处理", "交给我",
				"don't worry", "no problem", "happy to", "of course",
			}, Insert: "<#0.3#>", At: AtAfterFirstClause},

			// The caller has been waiting on something. A thinking sound is
			// what a person makes coming back from a lookup.
			{When: WhenToolLatencyOver, Arg: 800,
				Insert: "(emm)", At: AtAfterFirstClause},

			// A breath before the last sentence of a long answer. This is the
			// one that does most of the work: it is what stops a four-sentence
			// reply sounding read out.
			{When: WhenSentenceCountOver, Arg: 2,
				Insert: "(breath)", At: AtBeforeLastSentence},

			// An opening acknowledgement, given room.
			{When: WhenReplyStartsWith, Arg: []any{
				"好的", "嗯", "那么", "okay", "sure", "right",
			}, Insert: "<#0.25#>", At: AtAfterMatch},

			// A question that hands the floor back. The rising contour is
			// already there; the beat before it is what makes the offer sound
			// like an offer rather than a formality.
			{When: WhenEndsWithQuestion,
				Insert: "<#0.4#>", At: AtBeforeFinalClause},
		},
	}
}

// Presets are the rule sets that ship with the package, by name.
//
// A profile carries its own rules; these are for starting from something that
// has been thought about rather than from an empty array.
func Presets() map[string]Config {
	return map[string]Config{
		"default": Default(),
		"warm":    Warm(),
	}
}
