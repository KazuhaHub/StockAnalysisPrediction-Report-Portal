package driver

import (
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type UpdateStartBackendRequestSnapshot struct {
	Registration                              UpdateStartRegistrationSnapshot
	Method, Path, BodySHA256, AssertionSHA256 string
	Response                                  BackendResponse
	ObservedMS                                int64
}

type UpdateStartBackendRequestObservation struct {
	snapshot UpdateStartBackendRequestSnapshot
	valid    bool
}

func (UpdateStartBackendRequestObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}

func (o *UpdateStartBackendRequestObservation) UnmarshalJSON([]byte) error {
	*o = UpdateStartBackendRequestObservation{}
	return ErrContainerUncertain
}

func (o UpdateStartBackendRequestObservation) Snapshot() (UpdateStartBackendRequestSnapshot, error) {
	s := o.snapshot
	if !o.valid || !containerSHA.MatchString(s.BodySHA256) ||
		!containerSHA.MatchString(s.AssertionSHA256) ||
		!appplatform.ValidBackendRequestRoute(s.Method, s.Path) ||
		!appplatform.ValidBackendResponse(s.Response.Status, s.Response.Body) ||
		s.ObservedMS < 0 {
		return UpdateStartBackendRequestSnapshot{}, ErrContainerUncertain
	}
	if _, err := (UpdateStartRegistrationObservation{
		snapshot: s.Registration, valid: true,
	}).Snapshot(); err != nil {
		return UpdateStartBackendRequestSnapshot{}, err
	}
	return s, nil
}
