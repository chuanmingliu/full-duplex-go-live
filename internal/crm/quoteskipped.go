package crm

// quoteWithSkipped adds this request's intake exclusions to its answer. The
// cached quotation describes only the effective selection: a later request
// may ask for a different excluded coverage, or none. Copy both the result
// and its skipped slice so neither request can change the cached answer.
func quoteWithSkipped(q any, skipped []string) any {
	if len(skipped) == 0 {
		return q
	}
	switch q := q.(type) {
	case *Quote:
		if q != nil {
			out := *q
			out.Skipped = append(append([]string(nil), q.Skipped...), skipped...)
			return &out
		}
	case *APIQuote:
		if q != nil {
			out := *q
			out.Skipped = append(append([]string(nil), q.Skipped...), skipped...)
			return &out
		}
	}
	return q
}
