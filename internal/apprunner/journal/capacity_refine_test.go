//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type fakeLimits struct {
	byID  map[string]driver.ContainerLimits
	err   error
	calls []string
}

func (f *fakeLimits) ContainerLimits(_ context.Context, id string) (driver.ContainerLimits, error) {
	f.calls = append(f.calls, id)
	return f.byID[id], f.err
}

// An upgraded journal over-counts at the maxima until the runner reads the live containers' real limits.
func upgradedJournalWithEstimates(t *testing.T) (*Store, string, string) {
	t.Helper()
	directory, db := newV2Journal(t)
	if err := insertDeployment(db); err != nil {
		t.Fatal(err)
	}
	live, gone := strings.Repeat("a", 64), strings.Repeat("b", 64)
	insertV2Resource(t, db, 10, "container", live, "present", "running")
	insertV2Resource(t, db, 11, "container", gone, "present", "running")
	insertV2Resource(t, db, 12, "container", "candidate-pending/v1/never", "present", "unknown")
	db.Close()
	s, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, live, gone
}

func TestEstimatedReservationsAreCountedAndRefinedFromTheLiveContainer(t *testing.T) {
	s, live, _ := upgradedJournalWithEstimates(t)
	if n, err := s.EstimatedReservations(context.Background()); err != nil || n != 3 {
		t.Fatalf("estimated = %d %v", n, err)
	}
	obs := &fakeLimits{byID: map[string]driver.ContainerLimits{live: {MemoryBytes: 256 << 20, NanoCPUs: 500_000_000, Found: true}}}
	refined, err := s.RefineEstimatedReservations(context.Background(), obs)
	if err != nil || refined != 1 {
		t.Fatalf("refined = %d %v", refined, err)
	}
	var mem, cpu int64
	if err := s.db.QueryRow(`SELECT memory_mib,cpu_millis FROM runner_reservations WHERE resource_id=?`, fixtureID(10)).Scan(&mem, &cpu); err != nil || mem != 256 || cpu != 500 {
		t.Fatalf("live backend = %d %d %v", mem, cpu, err)
	}
	// A container the daemon no longer has, and a pending resource that never had one, stay at the maxima:
	// the amount cannot be observed, so it stays fail-closed, and it still shows as estimated.
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations WHERE memory_mib=4096 AND cpu_millis=4000`) != 2 {
		t.Fatal("an unobservable row was lowered")
	}
	if n, _ := s.EstimatedReservations(context.Background()); n != 2 {
		t.Fatalf("estimated after refinement = %d", n)
	}
	if len(obs.calls) != 2 {
		t.Fatalf("daemon reads = %v (a pending resource has no container to read)", obs.calls)
	}
}

func TestRefinementFailsClosedWhenTheDaemonCannotAnswer(t *testing.T) {
	s, _, _ := upgradedJournalWithEstimates(t)
	obs := &fakeLimits{err: errors.New("daemon unavailable")}
	if _, err := s.RefineEstimatedReservations(context.Background(), obs); err == nil {
		t.Fatal("a daemon failure was swallowed")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations WHERE memory_mib=4096`) != 3 {
		t.Fatal("a failed refinement changed amounts")
	}
}

func TestRefinementIgnoresAnUnlimitedContainer(t *testing.T) {
	s, live, _ := upgradedJournalWithEstimates(t)
	obs := &fakeLimits{byID: map[string]driver.ContainerLimits{live: {Found: true}}}
	if refined, err := s.RefineEstimatedReservations(context.Background(), obs); err != nil || refined != 0 {
		t.Fatalf("a container with no limits was refined: %d %v", refined, err)
	}
}
