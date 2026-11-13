package app

import (
	"context"
	"log"
)

// Settling the start an enable or repair left open.
//
// An enable or repair that ended for review with its start effect still open (prepared or unknown) may have left
// the runner holding that start claimed with nothing executing it. The runner keeps its target reservation, and an
// enable start's replaced source, until the start is settled. The settlement of a failed update already reads such a
// start back, asks the runner to settle an orphan, and closes the effect on what it answers; this drives the same
// steps (settleSettlementStart) for the operations that hold an enable start, from the same idle tick.
//
// Only the read-back and the orphan settlement are taken here. A start the runner holds as reserved (never claimed)
// holds no capacity and is left alone: its removal is built from an update's checkpoint, which these operations do
// not have. A start the runner reports running is closed as applied and nothing more is done; the installation stays
// in recovery for an administrator.

// platformHeldStartCandidates lists the finished enables and repairs whose start effect is still open.
func (s *Server) platformHeldStartCandidates(ctx context.Context) ([]string, error) {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT o.id FROM app_platform_operations o
		JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='start' AND f.effect_number=1
		WHERE o.action IN ('enable','repair') AND o.managed=1 AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND f.kind='enable-start' AND f.state IN ('prepared','unknown')
		ORDER BY o.completed_ms,o.id LIMIT 8`))
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

// settlePlatformHeldStarts takes one settlement step for each finished enable or repair whose start is open, and
// returns how many made progress.
func (s *Server) settlePlatformHeldStarts(ctx context.Context, endpoint platformRuntimeEndpoint) (int, error) {
	if endpoint.path == "" {
		return 0, nil
	}
	ids, err := s.platformHeldStartCandidates(ctx)
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
	if err != nil || !platformSameReleaseLifecycle(lease.snapshot.Action) {
		return false, err
	}
	start, err := loadPlatformSettleEffect(s.st.poolExec(), operation, "start", 1)
	if err != nil || !start.exists || (start.state != "prepared" && start.state != "unknown") {
		return false, err
	}
	return s.settleSettlementStart(ctx, endpoint, lease, start)
}
