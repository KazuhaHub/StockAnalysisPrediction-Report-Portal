package driver

import "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

type BackendHandshakeSnapshot struct {
	Launch                                                           CandidateLaunchSnapshot
	NativeID, BindingSHA256, DocumentSHA256, ChallengeSHA256, Answer string
	Device, Inode                                                    uint64
	ObservedMS                                                       int64
}

// This is physical transport provenance only; the host must separately verify
// the protocol MAC, exact challenge and boot identity before using the answer.
type BackendHandshakeObservation struct {
	snapshot BackendHandshakeSnapshot
	valid    bool
}

func (BackendHandshakeObservation) MarshalJSON() ([]byte, error) { return nil, ErrContainerUncertain }
func (o *BackendHandshakeObservation) UnmarshalJSON([]byte) error {
	*o = BackendHandshakeObservation{}
	return ErrContainerUncertain
}
func (o BackendHandshakeObservation) Snapshot() (BackendHandshakeSnapshot, error) {
	s := o.snapshot
	if !o.valid || !containerSHA.MatchString(s.NativeID) || !containerSHA.MatchString(s.BindingSHA256) || !containerSHA.MatchString(s.DocumentSHA256) || !containerSHA.MatchString(s.ChallengeSHA256) || !appplatform.ValidBackendHandshakeToken(s.Answer) || s.Inode == 0 || s.ObservedMS < 0 {
		return BackendHandshakeSnapshot{}, ErrContainerUncertain
	}
	return s, nil
}
