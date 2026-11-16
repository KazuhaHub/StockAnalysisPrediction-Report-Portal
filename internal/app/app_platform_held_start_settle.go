package app

import (
	"context"
	"database/sql"
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

// platformHeldStartsToSettle is the window of open starts this tick takes a step for: the finished enables, repairs
// and roll backs whose start effect is still open, walked in key order with a cursor that wraps (see
// platformKeysetCursor), so starts that keep failing cannot starve the ones after them.
func (s *Server) platformHeldStartsToSettle(ctx context.Context) ([]string, error) {
	// An enable or repair holds an enable-start. A managed restore that is not a recovery resume (a roll back of a
	// failed update) holds a restore-start; a resume is reconciled by the recovery path, which runs while the Portal is
	// paused and has its own evidence, so it is left out here.
	return platformKeysetWindow(ctx, s.st, &s.platformHeldStartCursor,
		`SELECT o.id FROM app_platform_operations o
		JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='start' AND f.effect_number=1
		LEFT JOIN app_platform_review_plans p ON p.id=o.review_plan_id`,
		`o.managed=1 AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND f.state IN ('prepared','unknown')
		AND ((o.action IN ('enable','repair') AND f.kind='enable-start')
		  OR (o.action='restore' AND f.kind='restore-start' AND (p.plan_json IS NULL OR p.plan_json NOT LIKE ?)))`,
		"o.id", []any{platformResumePlanPrefix()}, platformHeldStartWindow,
		func(rows *sql.Rows) (string, string, bool, error) {
			var id string
			err := rows.Scan(&id)
			return id, id, true, err
		})
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
			if platformRunnerDown(ctx, err) {
				return progressed, err
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
