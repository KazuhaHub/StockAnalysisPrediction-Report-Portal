//go:build linux

package driver

import (
	"context"
	"errors"
	"testing"
)

func TestWritableHandleCannotEnterLegacyStart(t *testing.T) {
	r := &CandidateRuntime{}
	binding := writableBindingFixture()
	state := &candidateLaunchState{owner: r, plan: containerPlan{writable: &binding}}
	// This synthetic state tests type/profile refusal before any native IO only.
	if _, err := r.StartCandidate(context.Background(), CandidateLaunch{state: state}); !errors.Is(err, ErrContainerUncertain) {
		t.Fatal("legacy entry accepted writable handle", err)
	}
	old := WritableCandidateLaunch{launch: CandidateLaunch{state: &candidateLaunchState{owner: r}}}
	if _, err := old.Snapshot(); err == nil {
		t.Fatal("readonly handle adopted as writable")
	}
}
