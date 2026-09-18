package prosody

import (
	"strings"
	"testing"
)

const model28 = "speech-2.8-turbo"

func TestSanitizeDropsUnknownParenthesisedTokens(t *testing.T) {
	c := Default()
	for _, in := range []string{
		"That is odd (yawns).",
		"That is odd (excitedly).",
		"That is odd (pause).",
	} {
		tts, plain := c.Sanitize(in, model28)
		if strings.Contains(tts, "(") {
			t.Fatalf("unknown tag survived: %q -> %q", in, tts)
		}
		if !strings.Contains(plain, "That is odd") {
			t.Fatalf("speakable text lost: %q -> %q", in, plain)
		}
	}
}

func TestSanitizeKeepsKnownTagsOnlyOn28(t *testing.T) {
	c := Default()
	const in = "Not in the plan (sighs), but fixable."
	if tts, _ := c.Sanitize(in, model28); !strings.Contains(tts, "(sighs)") {
		t.Fatalf("known tag dropped on 2.8: %q", tts)
	}
	if tts, _ := c.Sanitize(in, "speech-2.6-turbo"); strings.Contains(tts, "(sighs)") {
		t.Fatalf("tag survived on 2.6, it would be read aloud: %q", tts)
	}
}

// The API rejects two markers in a row outright, so a run has to be summed.
func TestSanitizeCollapsesConsecutivePauses(t *testing.T) {
	c := Default()
	tts, _ := c.Sanitize("one<#0.3#><#0.4#>two", model28)
	if n := strings.Count(tts, "<#"); n != 1 {
		t.Fatalf("want one marker, got %d in %q", n, tts)
	}
	if !strings.Contains(tts, "<#0.70#>") {
		t.Fatalf("want summed marker, got %q", tts)
	}
}

func TestSanitizeClampsPauseToProfile(t *testing.T) {
	c := Default() // [150, 800]
	tts, _ := c.Sanitize("wait<#5.00#>for it", model28)
	if !strings.Contains(tts, "<#0.80#>") {
		t.Fatalf("pause not clamped: %q", tts)
	}
	tts, _ = c.Sanitize("wait<#0.02#>for it", model28)
	if !strings.Contains(tts, "<#0.15#>") {
		t.Fatalf("pause not raised to floor: %q", tts)
	}
}

// Stripping markdown across the whole string would destroy every pause marker,
// because a marker is made of '#'. This is the regression that guards it.
func TestSanitizeStripsMarkdownWithoutEatingMarkers(t *testing.T) {
	c := Default()
	tts, plain := c.Sanitize("**Right**<#0.40#>_here_ it is", model28)
	if strings.ContainsAny(tts, "*_") {
		t.Fatalf("markdown survived: %q", tts)
	}
	if !strings.Contains(tts, "<#0.40#>") {
		t.Fatalf("marker destroyed by markdown stripping: %q", tts)
	}
	if strings.Contains(plain, "#") {
		t.Fatalf("markup leaked into history text: %q", plain)
	}
}

// A segment of nothing but markup produces no audio. The adapter reports that
// as a zero-chunk synthesis, which is indistinguishable from a cancelled one,
// so it must never be sent in the first place.
func TestSanitizeRefusesMarkupOnlySegment(t *testing.T) {
	c := Default()
	for _, in := range []string{"<#0.60#>", " (sighs) ", "<#0.30#>(breath)"} {
		tts, plain := c.Sanitize(in, model28)
		if tts != "" || plain != "" {
			t.Fatalf("markup-only segment passed through: %q -> %q / %q", in, tts, plain)
		}
	}
}

func TestSanitizeTrimsMarkersAtSegmentEdges(t *testing.T) {
	c := Default()
	tts, _ := c.Sanitize("<#0.40#>hello there<#0.40#>", model28)
	if strings.HasPrefix(tts, "<#") || strings.HasSuffix(tts, "#>") {
		t.Fatalf("edge marker kept, API requires text on both sides: %q", tts)
	}
}

func TestSanitizeKeepsHistoryFreeOfMarkup(t *testing.T) {
	c := Default()
	_, plain := c.Sanitize("I checked<#0.40#>and it is booked (sighs).", model28)
	if strings.Contains(plain, "<#") || strings.Contains(plain, "(") {
		t.Fatalf("history text carries markup: %q", plain)
	}
	if plain != "I checked and it is booked." {
		t.Fatalf("history text should read as plain prose, got %q", plain)
	}
}

// Tokenize is the base every other function stands on, so it must not lose or
// invent a byte.
func TestTokenizeReassemblesExactly(t *testing.T) {
	for _, in := range []string{
		"plain text with no markup at all",
		"a<#0.30#>b (sighs) c",
		"(yawns) leading tag<#1.00#>",
		"",
		"中文也要<#0.25#>正常分词",
	} {
		var b strings.Builder
		for _, tok := range Tokenize(in) {
			b.WriteString(tok.Text)
		}
		if b.String() != in {
			t.Fatalf("round trip lost bytes: %q -> %q", in, b.String())
		}
	}
}

func TestSafeBoundaryRefusesMidMarker(t *testing.T) {
	const buf = "checking<#0.60#>now (sighs) done"
	if SafeBoundary(buf, len("checking<#0")) {
		t.Fatal("boundary allowed inside a pause marker")
	}
	if SafeBoundary(buf, len("checking<#0.60#>now (sig")) {
		t.Fatal("boundary allowed inside a sound tag")
	}
	if !SafeBoundary(buf, len("checking<#0.60#>now ")) {
		t.Fatal("boundary refused at a safe point")
	}
}

// The truncation correction: a pause is time without characters and a tag is
// characters without text, so a character proportion reports a prefix nobody
// said.
func TestSpokenPrefixCountsDurationNotCharacters(t *testing.T) {
	const tts = "Hello there<#0.80#>friend"
	if got := SpokenPrefix(tts, 1500, 100); got != "Hello there" {
		t.Fatalf("pause silence credited as speech: %q", got)
	}
	if got := SpokenPrefix(tts, 500, 100); got != "Hello" {
		t.Fatalf("want prefix Hello, got %q", got)
	}
	if got := SpokenPrefix("Ok (sighs) done", 100, 50); got != "Ok" {
		t.Fatalf("tag characters credited as speech: %q", got)
	}
}

func TestValidateRejectsBadProfiles(t *testing.T) {
	bad := []Config{
		{Mode: "loud"},
		{Mode: ModeRules, PauseClampMS: [2]int{900, 150}},
		{Mode: ModeRules, MaxMarkersPerReply: -1},
		{Mode: ModeRules, Rules: []Rule{{When: "vibes", At: AtAfterMatch, Insert: "<#0.3#>"}}},
		{Mode: ModeRules, Rules: []Rule{{When: WhenContainsAny, At: "somewhere", Insert: "<#0.3#>"}}},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("profile %d accepted but should not be", i)
		}
	}
	if err := Default().Validate(); err != nil {
		t.Fatalf("default profile rejected: %v", err)
	}
}

func TestPromptAdditionFollowsMode(t *testing.T) {
	if (Config{Mode: ModeRules}).PromptAddition() != "" {
		t.Fatal("rules mode should not spend prompt characters")
	}
	if (Config{Mode: ModeBoth}).PromptAddition() == "" {
		t.Fatal("both mode needs the prompt block")
	}
}

// A tag that silently disappears is the hardest thing about operating this
// layer, so every removal has to be reportable.
func TestSanitizeReportExplainsEveryRemoval(t *testing.T) {
	c := Default()

	_, _, rep := c.SanitizeReport("Odd (yawns) but fine.", model28)
	if len(rep.Dropped) != 1 || rep.Dropped[0].Reason != DropUnknownTag {
		t.Fatalf("invented tag not explained: %+v", rep.Dropped)
	}

	_, _, rep = c.SanitizeReport("Odd (sighs) but fine.", "speech-2.6-turbo")
	if len(rep.Dropped) != 1 || rep.Dropped[0].Reason != DropModelNoTags {
		t.Fatalf("unrenderable tag not explained: %+v", rep.Dropped)
	}

	_, _, rep = c.SanitizeReport("<#0.40#>hello there", model28)
	if len(rep.Dropped) != 1 || rep.Dropped[0].Reason != DropSegmentEdge {
		t.Fatalf("edge marker not explained: %+v", rep.Dropped)
	}

	_, _, rep = c.SanitizeReport("(sighs)", model28)
	if len(rep.Dropped) == 0 || rep.Dropped[len(rep.Dropped)-1].Reason != DropNoSpeakable {
		t.Fatalf("empty segment not explained: %+v", rep.Dropped)
	}

	_, _, rep = c.SanitizeReport("one<#0.30#><#0.40#>two<#5.00#>three", model28)
	if rep.Collapsed != 1 {
		t.Fatalf("collapse not counted: %+v", rep)
	}
	if rep.Clamped == 0 {
		t.Fatalf("clamp not counted: %+v", rep)
	}
	if rep.Pauses != 2 {
		t.Fatalf("kept pauses not counted: %+v", rep)
	}

	_, _, rep = c.SanitizeReport("Not in the plan (sighs), but fixable.", model28)
	if rep.TagNames() != "sighs" {
		t.Fatalf("kept tag not named: %q", rep.TagNames())
	}
	if rep.Notable() {
		t.Fatal("a clean segment should not be worth a log line")
	}
}

func TestInjectNamesTheRulesThatFired(t *testing.T) {
	c := Default()
	res := c.Inject("Well, unfortunately that is booked.",
		TurnContext{LastTagTurn: -1, FirstSegment: true})
	if len(res.Applied) == 0 {
		t.Fatal("no rules reported as fired")
	}
	if got := res.AppliedSummary(); !strings.Contains(got, "→") {
		t.Fatalf("applied summary unreadable: %q", got)
	}
}

// A model writes "can't", "can’t" or "cannot" as it pleases. A rule that
// matches only the straight apostrophe misses most of what it exists for.
func TestDefaultRulesMatchBothApostrophes(t *testing.T) {
	c := Default()
	turn := TurnContext{LastTagTurn: -1, FirstSegment: true}
	for _, reply := range []string{
		"I'm afraid that flight is already full.",
		"I can't move it to yesterday.",
		"I can’t move it to yesterday.",
		"I cannot move that booking.",
		"Unfortunately that is gone.",
		"I am unable to do that.",
	} {
		if got := c.Inject(reply, turn); got.Tags != 1 {
			t.Fatalf("no tag for %q", reply)
		}
	}
	if got := c.Inject("That booking is no longer changeable.", turn); got.Tags != 0 {
		t.Fatalf("tag on a neutral reply: %q", got.Text)
	}
}
