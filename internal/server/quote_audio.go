package server

import "regexp"

// A closed call keeps its claim for this server lifetime: a reconnect must not
// silently obtain another preparation budget. At capacity only this optional
// optimization is skipped. Normal calls and synthesis remain available.
const maxQuoteAudioCalls = 1024

var quoteAudioCallID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (s *Server) claimQuoteAudio(call string) bool {
	if s.cfg.Duplex.QuoteReadoutWarmMax <= 0 || !quoteAudioCallID.MatchString(call) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.quoteAudioCalls[call]; exists || len(s.quoteAudioCalls) >= maxQuoteAudioCalls {
		return false
	}
	if s.quoteAudioCalls == nil {
		s.quoteAudioCalls = make(map[string]struct{})
	}
	s.quoteAudioCalls[call] = struct{}{}
	return true
}
