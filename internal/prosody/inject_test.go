package prosody

import (
	"strings"
	"testing"
)

func noTags() TurnContext { return TurnContext{LastTagTurn: -1} }

func TestInjectPlacesMarkers(t *testing.T) {
	c := Default()
	cases := []struct {
		name  string
		reply string
		turn  TurnContext
		want  string
	}{
		{
			name:  "opening discourse marker",
			reply: "Well, let me check that for you.",
			turn:  noTags(),
			want:  "Well,<#0.3#>let me check that for you.",
		},
		{
			name:  "bad news earns a sigh",
			reply: "I checked, and unfortunately that flight is fully booked. I can look at the next morning.",
			turn:  noTags(),
			want:  "I checked, and unfortunately that flight is fully booked (sighs). I can look at the next morning.",
		},
		{
			name:  "slow tool work earns a beat",
			reply: "Right, I have the booking here.",
			turn:  TurnContext{ToolLatencyMS: 1200, LastTagTurn: -1},
			want:  "Right,<#0.5#>I have the booking here.",
		},
		{
			name:  "nothing to react to",
			reply: "Your balance is four hundred dollars.",
			turn:  noTags(),
			want:  "Your balance is four hundred dollars.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Inject(tc.reply, tc.turn); got.Text != tc.want {
				t.Fatalf("got  %q\nwant %q", got.Text, tc.want)
			}
		})
	}
}

func TestInjectIsPure(t *testing.T) {
	c := Default()
	const reply = "Well, unfortunately that is booked. Shall I look at the morning?"
	first := c.Inject(reply, noTags())
	for i := 0; i < 5; i++ {
		if again := c.Inject(reply, noTags()); again.Text != first.Text {
			t.Fatalf("not deterministic: %q then %q", first.Text, again.Text)
		}
	}
}

// Every marker can be removed again. This is what makes the whole layer safe to
// switch off: nothing it does is destructive.
func TestInjectOnlyAddsMarkup(t *testing.T) {
	c := Default()
	for _, reply := range []string{
		"Well, let me check that for you.",
		"Unfortunately that is gone. One. Two. Three. Four.",
		"So, shall I book the later one instead?",
		"短句。第二句。第三句。第四句。",
	} {
		injected := c.Inject(reply, noTags()).Text
		_, plain := c.Sanitize(injected, model28)
		want := strings.Join(strings.Fields(reply), " ")
		if got := strings.Join(strings.Fields(plain), " "); got != want {
			t.Fatalf("injection changed the words:\n got  %q\n want %q", got, want)
		}
	}
}

func TestInjectRespectsBudget(t *testing.T) {
	c := Default()
	c.MaxMarkersPerReply = 1
	const reply = "Well, unfortunately that flight is booked. One. Two. Three. Shall I look again?"
	got := c.Inject(reply, noTags())
	if got.Markers() != 1 {
		t.Fatalf("budget of 1 produced %d markers: %q", got.Markers(), got.Text)
	}
}

func TestInjectCooldownStopsRepeatSighing(t *testing.T) {
	c := Default()
	const reply = "Unfortunately that one is gone too."

	cold := c.Inject(reply, TurnContext{TurnIndex: 9, LastTagTurn: -1})
	if cold.Tags != 1 {
		t.Fatalf("first tag should be allowed, got %q", cold.Text)
	}
	hot := c.Inject(reply, TurnContext{TurnIndex: 9, LastTagTurn: 8})
	if hot.Tags != 0 || hot.Text != reply {
		t.Fatalf("tag emitted inside cooldown: %q", hot.Text)
	}
	past := c.Inject(reply, TurnContext{TurnIndex: 9, LastTagTurn: 4})
	if past.Tags != 1 {
		t.Fatalf("tag refused after cooldown expired: %q", past.Text)
	}
}

// Mode both: the model's own markers win, rules only fill gaps.
func TestInjectDefersToModelMarkers(t *testing.T) {
	c := Default()
	c.Mode = ModeBoth
	const reply = "Well,<#0.40#> let me check that for you."
	got := c.Inject(reply, noTags())
	if got.Text != reply {
		t.Fatalf("rule piled onto the model's own marker: %q", got.Text)
	}
	if got.FromModel != 1 {
		t.Fatalf("want FromModel 1, got %d", got.FromModel)
	}
}

func TestInjectDisabledModes(t *testing.T) {
	const reply = "Well, unfortunately that is booked."
	for _, m := range []Mode{ModeOff, ModePrompt, ""} {
		c := Default()
		c.Mode = m
		if got := c.Inject(reply, noTags()); got.Text != reply {
			t.Fatalf("mode %q rewrote the reply: %q", m, got.Text)
		}
	}
}

// Whatever the rules produce must survive the sanitizer, or the two halves of
// the package disagree about what is legal.
func TestInjectOutputSurvivesSanitize(t *testing.T) {
	c := Default()
	for _, reply := range []string{
		"Well, unfortunately that flight is booked. Shall I try the morning?",
		"So, I have it here. One. Two. Three. Four.",
		"Okay, I'm afraid that is gone.",
	} {
		injected := c.Inject(reply, noTags()).Text
		tts, plain := c.Sanitize(injected, model28)
		if tts == "" || plain == "" {
			t.Fatalf("sanitizer rejected injected text: %q", injected)
		}
		if strings.Contains(plain, "<#") || strings.Contains(plain, "(") {
			t.Fatalf("markup reached history: %q", plain)
		}
	}
}
