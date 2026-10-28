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
func (*CandidateRuntime) ProvisionCandidateSockets(context.Context, CandidateLaunchRequest) error {
	return ErrUnsupported
}
func (*CandidateRuntime) StartCandidate(context.Context, CandidateLaunch) (CandidateRunningObservation, error) {
	return CandidateRunningObservation{}, ErrUnsupported
}
func (*CandidateRuntime) CheckCandidateReadiness(context.Context, CandidateLaunch, string, string) (CandidateReadinessObservation, error) {
	return CandidateReadinessObservation{}, ErrUnsupported
}

func (*CandidateRuntime) CheckConfiguration(context.Context) error { return ErrUnsupported }

func (*CandidateRuntime) CheckSharedRootConfiguration(context.Context, *data.Root, data.RootIdentity) error {
	return ErrUnsupported
}

func (*CandidateRuntime) ObserveSharedConfiguration(context.Context, *data.Root, data.RootIdentity) (RuntimeConfiguration, error) {
	return RuntimeConfiguration{}, ErrUnsupported
}

func (*CandidateRuntime) DeliverCandidateRegistration(context.Context, CandidateLaunch, string, string, []byte) (BackendRegistrationObservation, error) {
	return BackendRegistrationObservation{}, ErrUnsupported
}
func (*CandidateRuntime) ObserveCandidateRegistration(context.Context, CandidateLaunch, string, string, []byte) (BackendRegistrationObservation, error) {
	return BackendRegistrationObservation{}, ErrUnsupported
}
