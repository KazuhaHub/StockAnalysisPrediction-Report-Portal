package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// CapacityBudgets are the absolute ceilings the runner admits reservations
// against (doc 171). The service derives them from host facts at startup.
type CapacityBudgets struct {
	MemoryMiB       int64
	CPUMillis       int64
	DiskBytes       int64
	ImageStoreBytes int64
	RunningBackends int64
}

func (b CapacityBudgets) valid() bool {
	return b.MemoryMiB > 0 && b.CPUMillis > 0 && b.DiskBytes > 0 && b.ImageStoreBytes > 0 && b.RunningBackends > 0
}

// The dimensions a capacity refusal names.
const (
	CapacityMemory          = "memory"
	CapacityCPU             = "cpu"
	CapacityDisk            = "disk"
	CapacityImageStore      = "image_store"
	CapacityRunningBackends = "running_backends"
)

// CapacityExhaustedOutcome is the typed outcome of a refusal.
const CapacityExhaustedOutcome = "capacity_exhausted"

var (
	// ErrCapacityBudgets refuses budgets that are not all positive.
	ErrCapacityBudgets = errors.New("runner capacity budgets must all be positive")
	// ErrCapacityPlanMissing is an internal refusal: budgets are configured but the caller supplied no declared
	// resources to reserve. It is never a reservation and never a typed capacity refusal.
	ErrCapacityPlanMissing = errors.New("runner capacity admission has no declared resources")
)

// SetCapacityBudgets turns reservation and admission on. Until it is called a
// store reserves nothing and refuses nothing, which only tests rely on: the
// runner service calls it before it listens and reports not ready without it.
func (s *Store) SetCapacityBudgets(b CapacityBudgets) error {
	if !b.valid() {
		return ErrCapacityBudgets
	}
	s.capacity.Store(&b)
	return nil
}

// CapacityBudgets returns the configured budgets.
func (s *Store) CapacityBudgets() (CapacityBudgets, bool) {
	b := s.capacity.Load()
	if b == nil {
		return CapacityBudgets{}, false
	}
	return *b, true
}

// capacityPlan is one reservation a start-bearing claim asks for.
type capacityPlan struct {
	kind, resourceID, installationID            string
	memoryMiB, cpuMillis, diskBytes, imageBytes int64
}

// capacityRefusal is the dimension a plan did not fit, with what it asked for and what remained.
type capacityRefusal struct {
	dimension            string
	requested, remaining int64
}

// admitCapacity reserves the plan inside the caller's claim transaction, or reports the first dimension it does
// not fit. A refusal writes nothing. A held row of the same resource is replaced by the plan, never counted
// twice, so a replay and the replacement of a backfilled row both pass.
//
// Before it counts, it nets: a held backend or migration reservation of the same installation whose resource is
// proven absent in this transaction is released. A resource that is only stopped is not netted.
func (s *Store) admitCapacity(ctx context.Context, tx *sql.Tx, plan capacityPlan, now int64) (*capacityRefusal, error) {
	budgets, ok := s.CapacityBudgets()
	if !ok {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runner_reservations SET state='released',updated_ms=?
		WHERE state='held' AND installation_id=? AND resource_id<>? AND kind IN ('backend','migration')
		AND resource_id IN (SELECT id FROM runner_resources WHERE kind='container' AND desired_state='absent' AND observed_state='absent')`,
		now, plan.installationID, plan.resourceID); err != nil {
		return nil, err
	}
	var held struct{ state string }
	var mem, cpu, disk, image int64
	err := tx.QueryRowContext(ctx, `SELECT state,memory_mib,cpu_millis,disk_bytes,image_bytes FROM runner_reservations WHERE resource_id=?`, plan.resourceID).Scan(&held.state, &mem, &cpu, &disk, &image)
	existing := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if existing && held.state == reservationHeld && mem == plan.memoryMiB && cpu == plan.cpuMillis && disk == plan.diskBytes && image == plan.imageBytes {
		// Already counted at exactly this size: a replay, or an image another installation loaded first.
		return nil, nil
	}
	var usedMem, usedCPU, usedDisk, usedImage, usedBackends int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(memory_mib),0),COALESCE(SUM(cpu_millis),0),COALESCE(SUM(disk_bytes),0),COALESCE(SUM(image_bytes),0),COALESCE(SUM(CASE WHEN kind='backend' THEN 1 ELSE 0 END),0)
		FROM runner_reservations WHERE state='held' AND resource_id<>?`, plan.resourceID).Scan(&usedMem, &usedCPU, &usedDisk, &usedImage, &usedBackends); err != nil {
		return nil, err
	}
	backends := int64(0)
	if plan.kind == reservationBackend {
		backends = 1
	}
	for _, d := range []struct {
		dimension               string
		requested, used, budget int64
	}{
		{CapacityMemory, plan.memoryMiB, usedMem, budgets.MemoryMiB},
		{CapacityCPU, plan.cpuMillis, usedCPU, budgets.CPUMillis},
		{CapacityDisk, plan.diskBytes, usedDisk, budgets.DiskBytes},
		{CapacityImageStore, plan.imageBytes, usedImage, budgets.ImageStoreBytes},
		{CapacityRunningBackends, backends, usedBackends, budgets.RunningBackends},
	} {
		if d.requested > 0 && d.used+d.requested > d.budget {
			return &capacityRefusal{dimension: d.dimension, requested: d.requested, remaining: max(d.budget-d.used, 0)}, nil
		}
	}
	id, err := dataClaimID()
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,'held',?,?)
		ON CONFLICT(resource_id) DO UPDATE SET installation_id=excluded.installation_id,kind=excluded.kind,memory_mib=excluded.memory_mib,cpu_millis=excluded.cpu_millis,
		disk_bytes=excluded.disk_bytes,image_bytes=excluded.image_bytes,state='held',updated_ms=excluded.updated_ms`,
		id, plan.resourceID, plan.installationID, plan.kind, plan.memoryMiB, plan.cpuMillis, plan.diskBytes, plan.imageBytes, now, now)
	return nil, err
}

// releaseReservation marks a held reservation released. Callers invoke it only in the transaction that records
// the resource's proven absence; a stop request is never a release. An unknown or already released resource is
// not an error.
func releaseReservation(ctx context.Context, tx *sql.Tx, resourceID string, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE runner_reservations SET state='released',updated_ms=? WHERE resource_id=? AND state='held'`, now, resourceID)
	return err
}

func (r capacityRefusal) String() string {
	return fmt.Sprintf("%s requested=%s remaining=%s", r.dimension, strconv.FormatInt(r.requested, 10), strconv.FormatInt(r.remaining, 10))
}
