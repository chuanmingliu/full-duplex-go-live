// Package endpoint guesses, from the words so far, whether a speaker who has
// just gone quiet has finished or is still mid-thought.
//
// A full-duplex speech model answers this from everything at once — the words,
// the pitch of the last syllable, whether the breath was a pause or an ending —
// and it is most of what makes such a model feel like it is listening. A
// cascade has a timer: so many milliseconds of silence and the turn is over,
// the same number for "几点了" as for "我想问一下那个". The timer is tuned for the
// first and wrong for the second, or the other way round.
//
// This package recovers a little of that judgement from the one signal a
// cascade does have early, the partial transcript. It is deliberately a short
// list of closed-class words rather than a model: it runs on every audio frame
// of every pause, it must never be the slow part, and the engine is built so
// that its mistakes are cheap. A wrong "finished" starts the backend early on a
// guess that is then abandoned; a wrong "not finished" waits a few hundred
// milliseconds longer before answering. Neither ever cuts a caller off, because
// nothing here can shorten the silence a turn is given — only lengthen it.
package endpoint

import (
	"strings"
	"unicode"
)

// Class is the verdict on a transcript that has stopped growing.
type Class int

const (
	// Neutral means the words give no strong signal either way.
	Neutral Class = iota
	// Complete means the utterance reads as finished: a question, a closed
	// reply, something a person would expect an answer to.
	Complete
	// Incomplete means the speaker stopped on a word people do not stop on —
	// a connective, a hesitation, a clause still open.
	Incomplete
)

func (c Class) String() string {
	switch c {
	case Complete:
		return "complete"
	case Incomplete:
		return "incomplete"
	default:
		return "neutral"
	}
}

// Replies that are whole turns in themselves. Matched against the entire
// utterance, so "好的" is finished and "好的那个" is not decided here.
var closedReplies = setOf(
	"好", "好的", "好吧", "好啊", "行", "行吧", "可以", "可以的", "对", "对的", "对对", "对对对",
	"是", "是的", "是啊", "没错", "不是", "不对", "不行", "不用", "不用了", "不要", "不需要",
	"没有", "没了", "没问题", "没事", "有", "有的", "要", "要的", "需要", "知道了", "明白了",
	"明白", "懂了", "谢谢", "谢谢你", "多谢", "再见", "拜拜", "你好", "您好", "喂", "算了",
	"yes", "yeah", "yep", "no", "nope", "ok", "okay", "sure", "right", "correct",
	"thanks", "thankyou", "bye", "goodbye", "hello", "hi", "nevermind", "gotit", "isee",
)

// Endings that close a sentence: final particles, and the question words that
// Chinese puts last. Matched as suffixes.
var completeSuffixes = []string{
	"吗", "呢", "吧", "呀", "啦", "嘛", "么办", "怎么样", "怎么办", "是什么", "什么意思",
	"多少钱", "多少", "几点", "几号", "几个", "哪里", "哪儿", "哪个", "是谁", "为什么",
	"可不可以", "行不行", "好不好", "对不对", "是不是", "有没有", "能不能", "要不要",
	"谢谢", "谢谢你", "再见", "就这样", "就这些", "没有了", "好了", "可以了", "行了",
	"please", "thanks", "thankyou",
}

// Endings nobody stops on: connectives, hesitations, and the openers of a
// clause that has not arrived yet. Matched as suffixes, after the complete ones.
//
// The list is short on purpose. Every entry is a word the engine will wait
// longer after, so an entry that is sometimes a perfectly good last word costs
// a slower answer each time it is — "我要这个" must not wait on 这个, which is
// why the demonstratives are only in the whole-utterance set below.
var incompleteSuffixes = []string{
	"然后", "然后就", "但是", "可是", "不过", "因为", "所以", "如果", "要是", "假如", "虽然",
	"而且", "并且", "或者", "还是说", "还有", "以及", "另外", "比如", "比如说", "就是说",
	"也就是", "那就是", "就是", "还有就是", "的话", "的时候", "一下那个", "就是那个",
	"跟", "与", "把", "被", "对于", "关于", "除了",
	"我想", "我想要", "我想问", "我想问一下", "我想说", "我觉得", "我是说",
	"帮我", "请问", "想问一下", "问一下", "问你一下", "问您一下", "想问你", "想问您",
	"问一下啊", "问你一下啊", "问您一下啊", "问一下哈", "问你一下哈", "请教一下", "咨询一下",
	"呃", "额", "唔",
}

// Whole utterances that are openers rather than turns.
var incompleteWhole = setOf("那个", "这个", "我要", "麻烦", "麻烦你", "那", "还有", "然后")

// Whole utterances that are a murmur. Whether one of these takes the floor is
// the engine's backchannel question, not an endpointing one.
var murmurs = setOf("嗯", "嗯嗯", "嗯嗯嗯", "哦", "噢", "啊", "mm", "mhm", "uhhuh")

var incompleteWords = setOf(
	"and", "but", "so", "because", "or", "if", "when", "that", "which", "the", "a", "an",
	"to", "of", "for", "with", "my", "your", "i", "um", "uh", "er", "like", "then", "also",
	"about", "is", "are", "was", "can", "could", "would", "should", "want", "need", "its",
)

// Classify judges a transcript whose speaker has paused. It looks only at how
// the text ends, so it costs the same for a word as for a paragraph.
func Classify(text string) Class {
	text = strings.TrimSpace(text)
	if text == "" {
		return Neutral
	}

	// Punctuation, when the recognizer supplies it, outranks any word list: it
	// is the recognizer's own opinion, and it had the audio.
	runes := []rune(text)
	switch runes[len(runes)-1] {
	case '。', '？', '！', '?', '!':
		return Complete
	case '，', ',', '、', '；', ';', '：', ':', '—', '…':
		return Incomplete
	}

	norm := normalize(text)
	if norm == "" {
		return Neutral
	}
	if murmurs[norm] {
		return Neutral
	}
	if closedReplies[norm] {
		return Complete
	}
	if incompleteWhole[norm] {
		return Incomplete
	}
	for _, s := range completeSuffixes {
		if strings.HasSuffix(norm, s) {
			return Complete
		}
	}
	for _, s := range incompleteSuffixes {
		if strings.HasSuffix(norm, s) {
			return Incomplete
		}
	}
	if w := lastLatinWord(text); w != "" && incompleteWords[w] {
		return Incomplete
	}
	return Neutral
}

// normalize lower-cases and drops spaces and punctuation, the same reduction
// the engine applies before comparing transcripts.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// lastLatinWord returns the final space-delimited word when it is written in
// Latin letters, lower-cased, and "" otherwise. English function words only
// mean anything as whole words — "so" must not match the end of "also".
func lastLatinWord(s string) string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	})
	if len(fields) == 0 {
		return ""
	}
	w := fields[len(fields)-1]
	for _, r := range w {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || r == '\'') {
			return ""
		}
	}
	return strings.ReplaceAll(w, "'", "")
}

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
