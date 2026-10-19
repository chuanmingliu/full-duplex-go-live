package simulator

// settlePendingAudioBoundaries preserves the last received/delivered audio
// boundaries before ending cancels their silence timers. It never manufactures
// a response onset or resolves a speech-only observation without audio evidence.
// The caller holds the controller lock and has stopped accepting observations.
func (r *run) settlePendingAudioBoundaries() {
	customer := r.customerIdle != nil && r.observer.customerSpeaking
	remote := r.remoteIdle != nil && r.observer.assistantSpeaking
	// Apply ends chronologically so silence debounce does not leave false
	// overlap when the other speaker started after this speaker's final frame.
	if remote && (!customer || r.lastRemoteAudioMS < r.lastCustomerAudioMS) {
		r.observer.endRemote(r.lastRemoteAudioMS)
		remote = false
	}
	if customer {
		r.observer.endCustomer(r.lastCustomerAudioMS)
	}
	if remote {
		r.observer.endRemote(r.lastRemoteAudioMS)
	}
}
