package driver

import "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

type BackendResponse struct {
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}
type BackendRequestSnapshot struct {
	Binding                                   BackendRegistrationSnapshot
	Method, Path, BodySHA256, AssertionSHA256 string
	Response                                  BackendResponse
	ObservedMS                                int64
}

// A response is native exchange evidence, never authority to act as its viewer.
type BackendRequestObservation struct {
	snapshot BackendRequestSnapshot
	valid    bool
}

func (BackendRequestObservation) MarshalJSON() ([]byte, error) { return nil, ErrContainerUncertain }
func (o *BackendRequestObservation) UnmarshalJSON([]byte) error {
	*o = BackendRequestObservation{}
	return ErrContainerUncertain
}
func (o BackendRequestObservation) Snapshot() (BackendRequestSnapshot, error) {
	s := o.snapshot
	if !o.valid || !containerSHA.MatchString(s.Binding.NativeID) || !containerSHA.MatchString(s.BodySHA256) || !containerSHA.MatchString(s.AssertionSHA256) || !appplatform.ValidBackendRequestRoute(s.Method, s.Path) || !appplatform.ValidBackendResponse(s.Response.Status, s.Response.Body) || s.ObservedMS < 0 {
		return BackendRequestSnapshot{}, ErrContainerUncertain
	}
	s.Response.Body = append([]byte(nil), s.Response.Body...)
	return s, nil
}
