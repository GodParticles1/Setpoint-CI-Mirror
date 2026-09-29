package deploymenttopology

// WithParticipantTrust marks the local Agent host as local and every discovered
// non-local participant as untrusted. Discovery never grants peer authority.
func WithParticipantTrust(result Result) Result {
	for index := range result.Participants {
		if result.Participants[index].Local {
			result.Participants[index].Trust = ParticipantTrustLocal
		} else {
			result.Participants[index].Trust = ParticipantTrustUntrusted
		}
	}
	return result
}
