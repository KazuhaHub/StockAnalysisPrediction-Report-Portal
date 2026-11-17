//go:build !linux

package driver

import "context"

func (*CandidateRuntime) EnableDataWriter(context.Context, DataWriterRequest) (DataWriterObservation, error) {
	return DataWriterObservation{}, ErrUnsupported
}

func (*CandidateRuntime) ObserveDataWriter(context.Context, DataWriterRequest) (DataWriterRecoveryObservation, error) {
	return DataWriterRecoveryObservation{}, ErrUnsupported
}
