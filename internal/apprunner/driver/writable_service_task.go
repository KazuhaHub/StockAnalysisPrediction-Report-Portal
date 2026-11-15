package driver

import (
	"bytes"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type WritableServiceTaskSnapshot struct {
	Registration                      WritableBackendRegistrationSnapshot
	Handler                           string
	BodySHA256, ServiceIdentitySHA256 string
	Response                          BackendResponse
	ObservedMS                        int64
}

// Physical reply provenance only; it does not authenticate the Portal task.
type WritableServiceTaskObservation struct {
	snapshot WritableServiceTaskSnapshot
	valid    bool
}

func (WritableServiceTaskObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}
func (o *WritableServiceTaskObservation) UnmarshalJSON([]byte) error {
	*o = WritableServiceTaskObservation{}
	return ErrContainerUncertain
}
func (o WritableServiceTaskObservation) Snapshot() (WritableServiceTaskSnapshot, error) {
	s := o.snapshot
	if _, ok := appplatform.TaskHandlerPath(s.Handler); !ok || !o.valid ||
		!containerSHA.MatchString(s.BodySHA256) ||
		!containerSHA.MatchString(s.ServiceIdentitySHA256) ||
		!appplatform.ValidBackendResponse(s.Response.Status, s.Response.Body) ||
		s.ObservedMS < 0 {
		return WritableServiceTaskSnapshot{}, ErrContainerUncertain
	}
	if _, err := (WritableBackendRegistrationObservation{snapshot: s.Registration, valid: true}).Snapshot(); err != nil {
		return WritableServiceTaskSnapshot{}, err
	}
	s.Response.Body = bytes.Clone(s.Response.Body)
	return s, nil
}
