package driver

import "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

type WritableBackendRequestSnapshot struct {
	Registration                              WritableBackendRegistrationSnapshot
	Method, Path, BodySHA256, AssertionSHA256 string
	Response                                  BackendResponse
	ObservedMS                                int64
}

// Physical reply provenance only; it does not authenticate a Portal viewer.
type WritableBackendRequestObservation struct {
	snapshot WritableBackendRequestSnapshot
	valid    bool
}

func (WritableBackendRequestObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}
func (o *WritableBackendRequestObservation) UnmarshalJSON([]byte) error {
	*o = WritableBackendRequestObservation{}
	return ErrContainerUncertain
}
func (o WritableBackendRequestObservation) Snapshot() (WritableBackendRequestSnapshot, error) {
	s := o.snapshot
	if !o.valid || !containerSHA.MatchString(s.BodySHA256) || !containerSHA.MatchString(s.AssertionSHA256) || !appplatform.ValidBackendRequestRoute(s.Method, s.Path) || !appplatform.ValidBackendResponse(s.Response.Status, s.Response.Body) || s.ObservedMS < 0 {
		return WritableBackendRequestSnapshot{}, ErrContainerUncertain
	}
	if _, err := (WritableBackendRegistrationObservation{snapshot: s.Registration, valid: true}).Snapshot(); err != nil {
		return WritableBackendRequestSnapshot{}, err
	}
	s.Response.Body = append([]byte(nil), s.Response.Body...)
	return s, nil
}
