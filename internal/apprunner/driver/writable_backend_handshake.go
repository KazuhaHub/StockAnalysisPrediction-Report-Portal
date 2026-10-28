package driver

import "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

type WritableBackendHandshakeSnapshot struct {
	Registration            WritableBackendRegistrationSnapshot
	ChallengeSHA256, Answer string
	ObservedMS              int64
}

// Physical exchange provenance only. The host still independently verifies the
// answer MAC, exact challenge, time window and current backend boot identity.
type WritableBackendHandshakeObservation struct {
	snapshot WritableBackendHandshakeSnapshot
	valid    bool
}

func (WritableBackendHandshakeObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}
func (o *WritableBackendHandshakeObservation) UnmarshalJSON([]byte) error {
	*o = WritableBackendHandshakeObservation{}
	return ErrContainerUncertain
}
func (o WritableBackendHandshakeObservation) Snapshot() (WritableBackendHandshakeSnapshot, error) {
	s := o.snapshot
	if !o.valid || !containerSHA.MatchString(s.ChallengeSHA256) || !appplatform.ValidBackendHandshakeToken(s.Answer) || s.ObservedMS < 0 {
		return WritableBackendHandshakeSnapshot{}, ErrContainerUncertain
	}
	if _, err := (WritableBackendRegistrationObservation{snapshot: s.Registration, valid: true}).Snapshot(); err != nil {
		return WritableBackendHandshakeSnapshot{}, err
	}
	return s, nil
}
