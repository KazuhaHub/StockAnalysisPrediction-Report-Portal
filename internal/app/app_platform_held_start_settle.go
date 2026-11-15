package app

import (
	"context"
	"log"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Settling the start an enable, repair or roll back left open.
//
// An enable, repair or roll back (a managed restore that is not a recovery resume) that ended for review with its
// start effect still open (prepared or unknown) may have left the runner holding that start claimed with nothing
// executing it. The runner keeps its target reservation, and an enable start's replaced source, until the start is
// settled. The settlement of a failed update already reads such a start back, asks the runner to settle an orphan,
// and closes the effect on what it answers; this drives the same steps (settleSettlementStart) for these
// operations, from the same idle tick.
//
// A start the runner holds as reserved (never claimed) holds no capacity but keeps the pending target resource the
// reserve wrote; its target is removed bound to the reservation, which needs only the start effect, and the start is
// then closed as never run. A start the runner reports running is closed as applied and nothing more is done; the
// installation stays in recovery for an administrator.

// platformHeldStartWindow is how many open starts one tick takes a step for.
const platformHeldStartWindow = 8

// platformHeldStartCandidates lists the finished enables, repairs and roll backs whose start effect is still open.
func (s *Server) platformHeldStartCandidates(ctx context.Context) ([]string, error) {
	// An enable or repair holds an enable-start. A managed restore that is not a recovery resume (a roll back of a
	// failed update) holds a restore-start; a resume is reconciled by the recovery path, which runs while the Portal is
	// paused and has its own evidence, so it is left out here.
	rows, err := s.st.queryContext(ctx, `SELECT o.id FROM app_platform_operations o
		JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='start' AND f.effect_number=1
		LEFT JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.managed=1 AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND f.state IN ('prepared','unknown')
		AND ((o.action IN ('enable','repair') AND f.kind='enable-start')
		  OR (o.action='restore' AND f.kind='restore-start' AND (p.plan_json IS NULL OR p.plan_json NOT LIKE ?)))
		ORDER BY o.completed_ms,o.id LIMIT 256`, platformResumePlanPrefix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// platformHeldStartsToSettle is the window of open starts this tick takes a step for. The candidates are read in a
// fixed order, so a window that always began at the oldest would be taken up for good by starts that keep failing
// (a runner that stays unreachable, a start whose evidence no longer verifies): the ones after them would never be
// reached. The window therefore rotates through the candidates with a cursor, as the load settlement does, and every
// open start is reached within a bounded number of ticks.
func (s *Server) platformHeldStartsToSettle(ctx context.Context) ([]string, error) {
	ids, err := s.platformHeldStartCandidates(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) > platformHeldStartWindow {
		start := s.platformHeldStartCursor % len(ids)
		ids = append(append([]string{}, ids[start:]...), ids[:start]...)[:platformHeldStartWindow]
		s.platformHeldStartCursor += platformHeldStartWindow
	}
	return ids, nil
}

// settlePlatformHeldStarts takes one settlement step for each finished enable or repair whose start is open, and
// returns how many made progress.
func (s *Server) settlePlatformHeldStarts(ctx context.Context, endpoint platformRuntimeEndpoint) (int, error) {
	if endpoint.path == "" {
		return 0, nil
	}
	ids, err := s.platformHeldStartsToSettle(ctx)
	if err != nil {
		return 0, err
	}
	progressed := 0
	for _, id := range ids {
		done, err := s.settlePlatformHeldStart(ctx, endpoint, id)
		if err != nil {
			if ctx.Err() != nil {
				return progressed, ctx.Err()
			}
			log.Printf("app-platform held start settlement %s: %v", id, err)
			continue
		}
		if done {
			progressed++
		}
	}
	return progressed, nil
}

func (s *Server) settlePlatformHeldStart(ctx context.Context, endpoint platformRuntimeEndpoint, operation string) (bool, error) {
	lease, err := platformSettlementLease(s.st.poolExec(), operation)
	if err != nil || (!platformSameReleaseLifecycle(lease.snapshot.Action) && lease.snapshot.Action != appplatform.ActionRestore) {
		return false, err
	}
	start, err := loadPlatformSettleEffect(s.st.poolExec(), operation, "start", 1)
	if err != nil || !start.exists || (start.state != "prepared" && start.state != "unknown") {
		return false, err
	}
	return s.settleSettlementStart(ctx, endpoint, lease, start)
}
