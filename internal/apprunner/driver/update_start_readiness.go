package driver

type UpdateStartReadinessSnapshot struct {
	Launch     UpdateStartLaunchSnapshot `json:"launch"`
	NativeID   string                    `json:"native_id"`
	Protocol   string                    `json:"protocol"`
	DataFormat string                    `json:"data_format"`
	ObservedMS int64                     `json:"observed_ms"`
}

type UpdateStartReadinessObservation struct {
	snapshot UpdateStartReadinessSnapshot
	valid    bool
}

func (UpdateStartReadinessObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrCandidateNotReady
}
func (o *UpdateStartReadinessObservation) UnmarshalJSON([]byte) error {
	*o = UpdateStartReadinessObservation{}
	return ErrCandidateNotReady
}
func (o UpdateStartReadinessObservation) Snapshot() (UpdateStartReadinessSnapshot, error) {
	s := o.snapshot
	if !o.valid || !validUpdateStartSnapshot(s.Launch) ||
		!containerSHA.MatchString(s.NativeID) || s.Protocol != "portal.backend/1" ||
		!positiveContainerCounter(s.DataFormat) || s.ObservedMS < 0 {
		return UpdateStartReadinessSnapshot{}, ErrCandidateNotReady
	}
	return s, nil
}
