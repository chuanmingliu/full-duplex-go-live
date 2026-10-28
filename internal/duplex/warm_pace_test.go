package duplex

// The tests warm phrases in bursts against the mock synthesizer; the pace
// is for a real vendor's rate limit.
func init() { warmPace = 0 }
