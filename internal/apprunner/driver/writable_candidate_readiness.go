package driver

type WritableCandidateReadinessSnapshot struct {
	Launch     WritableCandidateLaunchSnapshot `json:"launch"`
	NativeID   string                          `json:"native_id"`
	Protocol   string                          `json:"protocol"`
	DataFormat string                          `json:"data_format"`
	ObservedMS int64                           `json:"observed_ms"`
}

// Opaque native+private-socket evidence. Parsing a healthy JSON response alone
// never constructs this type or establishes a current journal authorization.
type WritableCandidateReadinessObservation struct {
	snapshot WritableCandidateReadinessSnapshot
	valid    bool
}

func (WritableCandidateReadinessObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrCandidateNotReady
}
func (o *WritableCandidateReadinessObservation) UnmarshalJSON([]byte) error {
	*o = WritableCandidateReadinessObservation{}
	return ErrCandidateNotReady
}
func (o WritableCandidateReadinessObservation) Snapshot() (WritableCandidateReadinessSnapshot, error) {
	s := o.snapshot
	if !o.valid || !validWritableCandidateSnapshot(s.Launch) || !containerSHA.MatchString(s.NativeID) || s.Protocol != "portal.backend/1" || !positiveContainerCounter(s.DataFormat) || s.ObservedMS < 0 {
		return WritableCandidateReadinessSnapshot{}, ErrCandidateNotReady
	}
	return s, nil
}
