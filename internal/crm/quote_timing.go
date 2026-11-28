package crm

import "time"

// quoteAPITiming contains only durations and counters, never request fields,
// response bodies, customer identifiers or credentials.
type quoteAPITiming struct {
	Total, Before, Submit, PollHTTP, PollWait time.Duration
	Polls, PollErrors, Stale, Unpriced        int
	BeforeFailed                              bool
	Phase                                     string
}

func (t quoteAPITiming) fields() []any {
	return []any{
		"total_ms", t.Total.Milliseconds(), "before_ms", t.Before.Milliseconds(),
		"submit_ms", t.Submit.Milliseconds(), "poll_http_ms", t.PollHTTP.Milliseconds(),
		"poll_wait_ms", t.PollWait.Milliseconds(), "polls", t.Polls,
		"poll_errors", t.PollErrors, "stale_results", t.Stale, "unpriced_results", t.Unpriced,
		"before_failed", t.BeforeFailed, "phase", t.Phase,
	}
}
