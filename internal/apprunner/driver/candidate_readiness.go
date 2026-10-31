package driver

import (
	"errors"
)

var ErrCandidateNotReady = errors.New("candidate application is not ready")

type CandidateReadinessSnapshot struct {
	Launch     CandidateLaunchSnapshot `json:"launch"`
	NativeID   string                  `json:"native_id"`
	Protocol   string                  `json:"protocol"`
	DataFormat string                  `json:"data_format"`
	ObservedMS int64                   `json:"observed_ms"`
}

type CandidateReadinessObservation struct {
	snapshot CandidateReadinessSnapshot
	valid    bool
}

func (CandidateReadinessObservation) MarshalJSON() ([]byte, error) { return nil, ErrCandidateNotReady }
func (o *CandidateReadinessObservation) UnmarshalJSON([]byte) error {
	*o = CandidateReadinessObservation{}
	return ErrCandidateNotReady
}
func (o CandidateReadinessObservation) Snapshot() (CandidateReadinessSnapshot, error) {
	if !o.valid || !containerSHA.MatchString(o.snapshot.NativeID) || o.snapshot.Protocol != "portal.backend/1" || !positiveContainerCounter(o.snapshot.DataFormat) || o.snapshot.ObservedMS < 0 {
		return CandidateReadinessSnapshot{}, ErrCandidateNotReady
	}
	return o.snapshot, nil
}
