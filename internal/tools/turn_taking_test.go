package tools

import "testing"

func TestTurnTakingCueMatchesOnlyAtUtteranceStart(t *testing.T) {
	c, err := ParseTurnTaking([]byte(DefaultTurnTaking))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input, rest string
		match       bool
	}{
		{"等下", "", true}, {"你先别说话！", "", true}, {"你听我说。", "", true},
		{"等一下，三者险多少？", "三者险多少？", true},
		{"我等下去看。", "我等下去看。", false},
		{"等下班我再看。", "等下班我再看。", false},
		{"“等下”是什么意思？", "“等下”是什么意思？", false},
	} {
		if rest, match := c.SplitCue(tc.input); match != tc.match || rest != tc.rest {
			t.Errorf("SplitCue(%q) = %q, %t", tc.input, rest, match)
		}
	}
}
