//go:build !linux

package driver

import "context"

func (*CandidateRuntime) EnableDataWriter(context.Context, DataWriterRequest) (DataWriterObservation, error) {
	return DataWriterObservation{}, ErrUnsupported
}
