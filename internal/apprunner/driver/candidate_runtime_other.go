//go:build !linux

package driver

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

type CandidateRuntime struct{}
type candidateLaunchState struct{}

func OpenCandidateRuntime(context.Context, string, string, string, *data.Root, data.RootIdentity) (*CandidateRuntime, error) {
	return nil, ErrUnsupported
}
func (*CandidateRuntime) Close() error { return nil }
func (CandidateLaunch) Close() error   { return nil }
func (CandidateLaunch) Snapshot() (CandidateLaunchSnapshot, error) {
	return CandidateLaunchSnapshot{}, ErrUnsupported
}
func (*CandidateRuntime) PrepareCandidate(context.Context, CandidateLaunchRequest) (CandidateLaunch, error) {
	return CandidateLaunch{}, ErrUnsupported
}
func (*CandidateRuntime) StartCandidate(context.Context, CandidateLaunch) (CandidateRunningObservation, error) {
	return CandidateRunningObservation{}, ErrUnsupported
}

func (*CandidateRuntime) CheckConfiguration(context.Context) error { return ErrUnsupported }
