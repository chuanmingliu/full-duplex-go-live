package duplex

// The standard waiting script repeats a request to wait after announcing the
// calculation. When the caller has already heard a completed opener, keep just
// that first cached clause. The shorter turn completes normally, so history,
// caller interruptions and result/posthook ownership need no synthetic truncation.
// Custom panel text and acknowledgments without a completed opener stay intact.
func quoteAcknowledgment(text string, followsOpener bool) string {
	if followsOpener && text == "这会儿正在给您计算保费，请您稍等。" {
		return "这会儿正在给您计算保费，"
	}
	return text
}
