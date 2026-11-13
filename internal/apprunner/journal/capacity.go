package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
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
	// transferFrom names the stopped source backend an enable or repair start replaces. The native start removes
	// that container before it creates the target, so its reservation is transferred to the target.
	transferFrom string
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
	// Definite evidence that a reserved start never reached the native layer, so its reservation is dead: the
	// resource is still the pending row the reserve wrote, and the start command either ended reverted or was
	// superseded by a newer fence before any claim (a claim of a superseded fence is refused). A start that was
	// claimed (unknown) or applied is never released here: only the proof of its absence releases it.
	const startOf = `s.installation_id=res.installation_id AND s.target_generation=res.generation AND s.admitted_fence=res.control_fence AND s.stage='start' AND s.kind IN ('start-candidate','update-start')`
	if _, err := tx.ExecContext(ctx, `UPDATE runner_reservations SET state='released',updated_ms=?
		WHERE state='held' AND kind='backend' AND resource_id IN (
			SELECT res.id FROM runner_resources res
			WHERE res.kind='container' AND res.desired_state='present' AND res.observed_state='unknown' AND res.revision=1 AND res.observed_ms IS NULL
			AND EXISTS (SELECT 1 FROM runner_commands s WHERE `+startOf+` AND s.state IN ('prepared','reverted'))
			AND NOT EXISTS (SELECT 1 FROM runner_commands s WHERE `+startOf+` AND s.state IN ('unknown','applied'))
			AND (EXISTS (SELECT 1 FROM runner_commands s WHERE `+startOf+` AND s.state='reverted')
			  OR EXISTS (SELECT 1 FROM runner_commands n WHERE n.installation_id=res.installation_id AND n.kind<>'load-image' AND n.admitted_fence>res.control_fence)))`, now); err != nil {
		return nil, err
	}
	if plan.transferFrom != "" {
		// Transfer: the source of the same installation is stopped or absent, and the same effect removes it before
		// it creates the target, so the target takes its place in the totals. A running source is not transferred.
		if _, err := tx.ExecContext(ctx, `UPDATE runner_reservations SET state='released',updated_ms=?
			WHERE state='held' AND resource_id=? AND installation_id=? AND kind='backend'
			AND resource_id IN (SELECT id FROM runner_resources WHERE kind='container' AND installation_id=? AND observed_state IN ('stopped','absent'))`,
			now, plan.transferFrom, plan.installationID, plan.installationID); err != nil {
			return nil, err
		}
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

type backendResourcesKey struct{}

// withBackendResources carries the declared resources of the release being started to the reservation that
// admits it. Dispatch entry points attach them after parsing the descriptor, so no signature of the claim
// functions changes and no caller can reserve an amount the descriptor did not declare.
func withBackendResources(ctx context.Context, r appplatform.Resources) context.Context {
	return context.WithValue(ctx, backendResourcesKey{}, r)
}

// backendPlan builds the reservation of one backend or migration job. It reports false when admission does not
// apply because no budgets are configured, and refuses when they are but no resources were declared.
func (s *Store) backendPlan(ctx context.Context, kind, resourceID, installationID string) (capacityPlan, bool, error) {
	if _, ok := s.CapacityBudgets(); !ok {
		return capacityPlan{}, false, nil
	}
	r, ok := ctx.Value(backendResourcesKey{}).(appplatform.Resources)
	if !ok || r.MemoryMiB <= 0 || r.CPUMillis <= 0 {
		return capacityPlan{}, false, ErrCapacityPlanMissing
	}
	return capacityPlan{kind: kind, resourceID: resourceID, installationID: installationID, memoryMiB: int64(r.MemoryMiB), cpuMillis: int64(r.CPUMillis)}, true, nil
}

// namespacePlan builds the reservation of one managed data namespace.
func (s *Store) namespacePlan(resourceID, installationID string) (capacityPlan, bool) {
	if _, ok := s.CapacityBudgets(); !ok {
		return capacityPlan{}, false
	}
	return capacityPlan{kind: reservationNamespace, resourceID: resourceID, installationID: installationID, diskBytes: NamespaceReservationBytes}, true
}

// lookupCapacityRefusal finds the refusal a capacity refusal stored for this command, or nil.
func (s *Store) lookupCapacityRefusal(ctx context.Context, id CapacityIdentity) *CapacityRefusalError {
	var state, raw string
	if err := s.db.QueryRowContext(ctx, `SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, id.EffectID).Scan(&state, &raw); err != nil || state != "reverted" {
		return nil
	}
	if r, ok := storedCapacityRefusal(raw, id); ok {
		return &CapacityRefusalError{Receipt: r}
	}
	return nil
}

// capacityRefusalFor answers a replay of a command a capacity refusal reverted.
func (s *Store) capacityRefusalFor(ctx context.Context, id CapacityIdentity) error {
	if refusal := s.lookupCapacityRefusal(ctx, id); refusal != nil {
		return refusal
	}
	return ErrFenceReconciliation
}

// requireHeldReservation is the claim's last check before a native call: with budgets configured, the resource
// must hold a reservation. It catches a resource that reached its claim without being admitted.
func (s *Store) requireHeldReservation(ctx context.Context, tx *sql.Tx, resourceID string) error {
	if _, ok := s.CapacityBudgets(); !ok {
		return nil
	}
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id=? AND state='held'`, resourceID).Scan(&held); err != nil {
		return err
	}
	if held != 1 {
		return fmt.Errorf("%w: no held reservation", ErrFenceReconciliation)
	}
	return nil
}

// answerCapacityRefusal is the read side of a refusal. A read of a command whose effect is reverted by a capacity
// refusal returns the stored receipt, but only after verify accepts the signature under the read's own purpose and
// the stored receipt binds that exact command. It returns nil when there is no such refusal, leaving the
// caller's own error in force.
func (s *Store) answerCapacityRefusal(ctx context.Context, effectID string, verify func(a control.Authority, now int64) (CapacityIdentity, error)) error {
	if s.closing.Load() {
		return nil
	}
	a, err := s.writableDispatchAuthority(ctx)
	if err != nil {
		return nil
	}
	id, err := verify(a, time.Now().UTC().UnixMilli())
	if err != nil || id.EffectID != effectID {
		return nil
	}
	if refusal := s.lookupCapacityRefusal(ctx, id); refusal != nil {
		return refusal
	}
	return nil
}

// CapacityIdentityOfUpdateStart is the identity a refusal of an update, restore or enable start carries. All three
// are stored, and answered, as one update-start.
func CapacityIdentityOfUpdateStart(c control.UpdateStartCommand) CapacityIdentity {
	return CapacityIdentityOfCommand(updateStartIndex(c))
}

// admitUpdateStart reserves an update, restore or enable start in its claim transaction. On a refusal it reverts
// the never claimed start with the typed receipt and takes its never started target resource out of the journal,
// in the same transaction, so nothing of the refused start remains. It returns the refusal to commit, or nil when
// the start fits.
func (s *Store) admitUpdateStart(ctx context.Context, tx *sql.Tx, f updateStartFacts, resource *dataResource) (*CapacityRefusalError, error) {
	c := f.command
	plan, admit, err := s.backendPlan(ctx, reservationBackend, c.Payload.ResourceID, c.InstallationID)
	if err != nil || !admit {
		return nil, err
	}
	if id := enableStartSourceResource(f); c.Kind == "enable-start" && id != "" && id != c.Payload.ResourceID {
		plan.transferFrom = id
	}
	refusal, err := s.admitCapacity(ctx, tx, plan, f.now)
	if err != nil || refusal == nil {
		return nil, err
	}
	receipt, err := refuseCapacityPrepared(ctx, tx, CapacityIdentityOfUpdateStart(c), *refusal, f.now)
	if err != nil {
		return nil, err
	}
	// The target reused the row of a removed one only when a removal receipt of this very resource exists. A
	// remove-code effect alone says nothing: every update carries one for its source.
	var removals int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND kind='remove-code' AND state='applied' AND json_extract(receipt_json,'$.resource_id')=?`,
		c.InstallationID, resource.id).Scan(&removals); err != nil {
		return nil, err
	}
	// A target that reused the row of a removed one goes back to absent; a fresh one never existed and is deleted.
	var changed sql.Result
	if removals == 0 {
		changed, err = tx.ExecContext(ctx, `DELETE FROM runner_resources WHERE id=? AND kind='container' AND native_id=? AND revision=1 AND desired_state='present' AND observed_state='unknown' AND observed_ms IS NULL`,
			resource.id, resource.native)
	} else {
		changed, err = tx.ExecContext(ctx, `UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=4,observed_ms=?,updated_ms=? WHERE id=? AND kind='container' AND native_id=? AND revision=1 AND desired_state='present' AND observed_state='unknown' AND observed_ms IS NULL`,
			f.now, f.now, resource.id, resource.native)
	}
	if err != nil {
		return nil, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return nil, fmt.Errorf("%w: refused update start resource rows=%d err=%v", ErrFenceReconciliation, n, err)
	}
	return &CapacityRefusalError{Receipt: receipt}, nil
}

// CapacityIdentityOfCandidate is the identity a refusal of a candidate or writable candidate start carries.
func CapacityIdentityOfCandidate(c control.CandidateCommand) CapacityIdentity {
	return CapacityIdentityOfCommand(candidateIndex(c))
}

// CapacityIdentityOfImport is the identity a refusal of an imported namespace carries.
func CapacityIdentityOfImport(c control.ImportCheckpointCommand) CapacityIdentity {
	return CapacityIdentity{c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.EffectID, c.Kind, c.PayloadSHA256}
}

// enableStartSourceResource is the stopped backend an enable or repair start replaces: the resource of whichever
// start produced the checkpoint it enables from.
func enableStartSourceResource(f updateStartFacts) string {
	source := f.source.source
	switch {
	case source.update != nil:
		return source.update.Claim.ResourceID
	case source.writable != nil:
		return source.writable.Claim.ResourceID
	}
	return source.source.Claim.ResourceID
}
