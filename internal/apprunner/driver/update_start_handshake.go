package driver

import "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

type UpdateStartBackendHandshakeSnapshot struct {
	Registration            UpdateStartRegistrationSnapshot
	ChallengeSHA256, Answer string
	ObservedMS              int64
}

type UpdateStartBackendHandshakeObservation struct {
	snapshot UpdateStartBackendHandshakeSnapshot
	valid    bool
}

func (UpdateStartBackendHandshakeObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}

func (o *UpdateStartBackendHandshakeObservation) UnmarshalJSON([]byte) error {
	*o = UpdateStartBackendHandshakeObservation{}
	return ErrContainerUncertain
}

func (o UpdateStartBackendHandshakeObservation) Snapshot() (UpdateStartBackendHandshakeSnapshot, error) {
	s := o.snapshot
	if !o.valid || !containerSHA.MatchString(s.ChallengeSHA256) ||
		!appplatform.ValidBackendHandshakeToken(s.Answer) || s.ObservedMS < 0 {
		return UpdateStartBackendHandshakeSnapshot{}, ErrContainerUncertain
	}
	if _, err := (UpdateStartRegistrationObservation{
		snapshot: s.Registration, valid: true,
	}).Snapshot(); err != nil {
		return UpdateStartBackendHandshakeSnapshot{}, err
	}
	return s, nil
}
