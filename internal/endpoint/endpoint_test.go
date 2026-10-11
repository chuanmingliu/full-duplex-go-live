package endpoint

import "testing"

func TestHesitation(t *testing.T) {
	for text, want := range map[string]bool{
		"呃": true, "呃。": true, "额额": true, "呃……": true, "Um.": true, "uh": true,
		"嗯": false, "啊": false, "呃，多少钱": false, "好的": false, "": false, "。": false, "umbrella": false,
	} {
		if got := Hesitation(text); got != want {
			t.Errorf("Hesitation(%q) = %v; want %v", text, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		text string
		want Class
	}{
		// Nothing to go on.
		{"", Neutral},
		{"   ", Neutral},
		{"明天上海的天气", Neutral},
		{"the weather in Shanghai tomorrow", Neutral},

		// Questions and closed replies.
		{"明天会下雨吗", Complete},
		{"这个多少钱", Complete},
		{"你觉得怎么样", Complete},
		{"好的", Complete},
		{"不用了", Complete},
		{"谢谢", Complete},
		{"那就这样", Complete},
		{"然后呢", Complete}, // a particle outranks the connective before it
		{"okay", Complete},
		{"Thank you", Complete},
		{"yes please", Complete},

		// A recognizer's own punctuation decides.
		{"帮我查一下明天的天气。", Complete},
		{"what time is it?", Complete},
		{"帮我查一下，", Incomplete},
		{"I was wondering,", Incomplete},

		// Trailing off.
		{"我想问一下那个", Incomplete},
		// Heard on a call, and answered as though it were the question.
		{"诶，我想问你一下啊", Incomplete},
		{"我想问您一下", Incomplete},
		{"就是", Incomplete},
		{"帮我查一下明天的天气然后", Incomplete},
		{"我昨天买的那个保险但是", Incomplete},
		{"请问", Incomplete},
		{"这个保单的话", Incomplete},
		{"I want to book a flight to", Incomplete},
		{"it was on Tuesday and", Incomplete},
		{"um", Incomplete},
		{"那个", Incomplete},

		// Words that end a clause as often as they open one are left alone.
		{"我要这个", Neutral},
		{"我要一个", Neutral},
		{"这是我的", Neutral},
		{"嗯", Neutral},

		// English function words match as words, never as the tail of another.
		{"I think so also", Incomplete},
		{"that is the pianist", Neutral},
		{"we went to Oslo", Neutral},
	}
	for _, c := range cases {
		if got := Classify(c.text); got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// The classifier runs on every audio frame of every pause, on the engine's
// loop goroutine. It has to cost nothing.
func BenchmarkClassify(b *testing.B) {
	text := "帮我查一下明天上海的天气怎么样需要带伞吗然后顺便看看后天的"
	for i := 0; i < b.N; i++ {
		Classify(text)
	}
}
