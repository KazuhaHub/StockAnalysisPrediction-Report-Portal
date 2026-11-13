package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// The load-image effect row. Kind load_image, stage "stage", number 1, the id derived from the operation and
// release. The kind column has no enum check, so this needs no schema change. The row is written before the
// first command leaves Portal, and ends applied (receipt) or reverted (a definite rejection). A load the gate
// gives up on is left unknown: the operation ends recovery_required, an unsettled effect keeps it there (see
// settlePlatformRecoveryOperation), and settlePlatformLoadImages reads it back and sends it again under the same
// id until it settles.
const platformLoadImageKind = "load_image"

type platformImageRows struct {
	s         *Server
	lease     platformOperationLease
	runtimeID string
	// guard confirms, in the writing transaction, that the caller may still act for the operation, and returns
	// the database time.
	guard func(migExec) (int64, error)
}

func (r platformImageRows) with(ctx context.Context, fn func(e migExec, now int64) error) error {
	tx, err := r.s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: r.s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	now, err := r.guard(e)
	if err != nil {
		return err
	}
	if err := fn(e, now); err != nil {
		return err
	}
	if _, err := r.guard(e); err != nil {
		return err
	}
	return tx.Commit()
}

// prepare writes the row before the first command is sent. It reports applied when an earlier attempt already
// settled the load, and refuses a load an earlier attempt saw rejected.
func (r platformImageRows) prepare(ctx context.Context, c control.LoadImageCommand) (applied bool, err error) {
	err = r.with(ctx, func(e migExec, now int64) error {
		var id, kind, digest, state string
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		qerr := e.queryRow(`SELECT effect_id,kind,payload_sha256,state FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=1`+lock, r.lease.id, control.LoadImageStage).
			Scan(&id, &kind, &digest, &state)
		if errors.Is(qerr, sql.ErrNoRows) {
			_, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
				VALUES(?,?,1,?,?,?,?,?,?,0,'prepared','{}',?,?)`, r.lease.id, control.LoadImageStage, c.EffectID, platformLoadImageKind, c.PayloadSHA256, r.runtimeID, r.lease.controllerEpoch, r.lease.fence, now, now)
			return err
		}
		if qerr != nil {
			return qerr
		}
		if kind != platformLoadImageKind || id != c.EffectID || digest != c.PayloadSHA256 {
			return errAppPlatformStageEvidence
		}
		switch state {
		case "applied":
			applied = true
		case "reverted":
			return fmt.Errorf("%w: an earlier attempt saw the runner reject the image", errPlatformImageRejected)
		case "prepared", "unknown":
		default:
			return errAppPlatformStageEvidence
		}
		return nil
	})
	return applied, err
}

func (r platformImageRows) settle(ctx context.Context, c control.LoadImageCommand, state, receipt string) error {
	if len(receipt) > 16384 {
		return errAppPlatformStageEvidence
	}
	return r.with(ctx, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state=?,observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=1 AND effect_id=? AND kind=? AND state IN ('prepared','unknown')`,
			state, r.lease.fence, receipt, now, r.lease.id, control.LoadImageStage, c.EffectID, platformLoadImageKind)
		return platformLeaseCAS(changed, err)
	})
}

// unknown marks a load that was sent and has no verdict. It is best effort: a failure leaves the row prepared,
// which the settlement driver treats the same way.
func (r platformImageRows) unknown(ctx context.Context, c control.LoadImageCommand) error {
	return r.with(ctx, func(e migExec, now int64) error {
		_, err := e.exec(`UPDATE app_platform_operation_effects SET state='unknown',updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=1 AND effect_id=? AND kind=? AND state='prepared'`,
			now, r.lease.id, control.LoadImageStage, c.EffectID, platformLoadImageKind)
		return err
	})
}

// platformLoadSettlementLease reads a finished operation as the unleased identity a settlement command is bound
// to.
func platformLoadSettlementLease(e migExec, operation string) (platformOperationLease, error) {
	var id, installation, epoch, stage, action string
	var fence, generation, stateRevision, rowRevision int64
	var completed sql.NullInt64
	var owner sql.NullString
	if err := e.queryRow(`SELECT id,installation_id,controller_epoch,fence,target_generation,state_revision,row_revision,stage,action,completed_ms,lease_owner
		FROM app_platform_operations WHERE id=? AND managed=1`, operation).
		Scan(&id, &installation, &epoch, &fence, &generation, &stateRevision, &rowRevision, &stage, &action, &completed, &owner); err != nil ||
		!completed.Valid || owner.Valid || generation < 1 {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	return platformOperationLease{
		id: id, installationID: installation, controllerEpoch: epoch, fence: fence, rowRevision: rowRevision,
		leaseUntilMS: time.Now().Add(5 * time.Minute).UnixMilli(),
		snapshot: appplatform.OperationSnapshot{Action: appplatform.Action(action), Managed: true, Generation: uint64(generation),
			Revision: uint64(stateRevision), Stage: appplatform.Stage(stage)},
	}, nil
}

// platformLoadSettlementGuard checks under the mutation lock that the finished operation is what the settlement
// was derived from, and that nothing has moved the installation's fence past it.
func platformLoadSettlementGuard(e migExec, lease platformOperationLease) (int64, error) {
	current, err := platformLoadSettlementLease(e, lease.id)
	if err != nil || current.fence != lease.fence || current.controllerEpoch != lease.controllerEpoch || current.installationID != lease.installationID {
		return 0, errors.Join(errAppPlatformLeaseConflict, err)
	}
	var epoch string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),'')`).Scan(&epoch); err != nil || epoch != lease.controllerEpoch {
		return 0, errAppPlatformControlPaused
	}
	var fence int64
	var installationEpoch string
	if err := e.queryRow(`SELECT control_fence,controller_epoch FROM app_platform_installations WHERE id=?`, lease.installationID).Scan(&fence, &installationEpoch); err != nil ||
		fence != lease.fence || installationEpoch != lease.controllerEpoch {
		return 0, errAppPlatformLeaseConflict
	}
	return platformDatabaseNow(e)
}

// platformLoadSettlementCandidates lists the finished operations that hold a load with no verdict and that
// this Portal can still settle: the installation's fence and epoch are the ones the operation was admitted under.
// A row it cannot settle (a fence that moved, an epoch that was rotated) is left out of the window, so it cannot
// starve the others, and is returned in skipped so the caller can say so.
func (s *Server) platformLoadSettlementCandidates(ctx context.Context) (ids, skipped []string, err error) {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT o.id,
		CASE WHEN i.control_fence=o.fence AND i.controller_epoch=o.controller_epoch
		AND o.controller_epoch=COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),'') THEN 1 ELSE 0 END
		FROM app_platform_operations o
		JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='stage' AND f.effect_number=1 AND f.kind='load_image'
		JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE o.managed=1 AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND f.state IN ('prepared','unknown') ORDER BY o.completed_ms,o.id LIMIT 256`))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ok int
		if err := rows.Scan(&id, &ok); err != nil {
			return nil, nil, err
		}
		if ok == 1 {
			ids = append(ids, id)
		} else {
			skipped = append(skipped, id)
		}
	}
	return ids, skipped, rows.Err()
}

// settleLoadsAsAbandoned settles the unsettled loads of the given operations as reverted with the outcome
// abandoned_outcome_unknown: Portal has no runner verdict for them.
func (s *Server) settleLoadsAsAbandoned(ctx context.Context, operations []string) error {
	return s.st.withOUMutation(func(e migExec) error {
		now, err := platformDatabaseNow(e)
		if err != nil {
			return err
		}
		for _, id := range operations {
			if _, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json='{"outcome":"abandoned_outcome_unknown"}',updated_ms=?
				WHERE operation_id=? AND kind='load_image' AND state IN ('prepared','unknown')`, now, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// platformLoadSettlementWindow is how many loads one tick settles.
const platformLoadSettlementWindow = 8

// revertLoadsOfRetiredRuntimes settles, as reverted with the outcome abandoned_outcome_unknown (not a runner verdict,
// which Portal does not have), the loads issued to a runtime binding that was retired
// (abandoned). A load to a runtime that no longer exists cannot happen, and nothing could read it back. The
// abandon itself leaves these rows to this driver (it reverts every other unsettled effect of the runtime).
func (s *Server) revertLoadsOfRetiredRuntimes(ctx context.Context) error {
	return s.st.withOUMutation(func(e migExec) error {
		now, err := platformDatabaseNow(e)
		if err != nil {
			return err
		}
		_, err = e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json='{"outcome":"abandoned_outcome_unknown"}',updated_ms=?
			WHERE kind='load_image' AND state IN ('prepared','unknown')
			AND runtime_id IN (SELECT id FROM app_platform_runtime_bindings WHERE enabled=0)
			AND operation_id IN (SELECT id FROM app_platform_operations WHERE completed_ms IS NOT NULL)`, now)
		return err
	})
}

// settlePlatformLoadImages takes one settlement step for up to platformLoadSettlementWindow finished operations
// that hold an unknown load, rotating through them so one that cannot settle does not starve the rest. Each
// load is read back first and, while the runner reports nothing running it, sent again under the same id, with
// its blobs uploaded when the runner needs them. It returns how many loads settled.
func (s *Server) settlePlatformLoadImages(ctx context.Context, endpoint platformRuntimeEndpoint) (int, error) {
	if err := s.revertLoadsOfRetiredRuntimes(ctx); err != nil {
		return 0, err
	}
	if endpoint.path == "" {
		return 0, nil
	}
	ids, skipped, err := s.platformLoadSettlementCandidates(ctx)
	if err != nil {
		return 0, err
	}
	// A load whose operation's fence or epoch has moved can never settle under its lease. Its outcome is unknown, and
	// leaving it open would block the installation for ever, so it is settled as such.
	if len(skipped) > 0 {
		for _, id := range skipped {
			log.Printf("app-platform load-image settlement %s: the installation's fence or the controller epoch has moved; settling the load as abandoned_outcome_unknown", id)
		}
		if err := s.settleLoadsAsAbandoned(ctx, skipped); err != nil {
			return 0, err
		}
	}
	if len(ids) > platformLoadSettlementWindow {
		start := s.platformLoadCursor % len(ids)
		ids = append(append([]string{}, ids[start:]...), ids[:start]...)[:platformLoadSettlementWindow]
		s.platformLoadCursor += platformLoadSettlementWindow
	}
	settled := 0
	for _, id := range ids {
		lease, err := platformLoadSettlementLease(s.st.poolExec(), id)
		if err == nil {
			err = s.settlePlatformLoadImage(ctx, endpoint, lease)
		}
		switch {
		case err == nil:
			settled++
		case ctx.Err() != nil:
			return settled, ctx.Err()
		case errors.Is(err, errPlatformImageBusy):
			// The runner still holds it: the same effect is read again on the next tick.
		default:
			log.Printf("app-platform load-image settlement %s: %v", id, err)
		}
	}
	return settled, nil
}

// settlePlatformLoadImage reads the load back first. That needs no blob, so it does not hash any: a receipt
// settles the row as it is. Only when the runner reports that the bytes are needed (blobs_missing, orphaned,
// blob_refused, or no row at all) are the blobs hashed again and the same effect sent under its own id.
func (s *Server) settlePlatformLoadImage(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease) error {
	target, err := s.loadPlatformImageDescriptorTarget(ctx, lease)
	if err != nil {
		return err
	}
	if target == nil {
		return errAppPlatformStageEvidence
	}
	guard := func(e migExec) (int64, error) { return platformLoadSettlementGuard(e, lease) }
	signer, err := s.newPlatformImageSignerGuarded(ctx, lease, func(e migExec) error { _, err := guard(e); return err })
	if err != nil {
		return err
	}
	defer signer.close()
	rows := platformImageRows{s: s, lease: lease, runtimeID: signer.runtime.id, guard: guard}
	call, err := signer.loadCall(target)
	if err != nil {
		return err
	}
	reply, readErr := s.platformImageRunnerSeam().ReadLoadImage(ctx, endpoint, call)
	switch {
	case readErr == nil:
		receipt, _, verdict := s.interpretPlatformImageReply(reply, call, endpoint)
		if receipt != nil {
			// A rejection is a settled verdict too: the load is over, whichever way it ended.
			_ = s.recordPlatformImageReceipt(ctx, signer, rows, call, receipt, verdict)
			return nil
		}
		if verdict != nil {
			return verdict
		}
	case errors.Is(readErr, errPlatformMigrationAbsent):
	default:
		// The runner refused the load for lack of room in its image store: the effect never ran, so it is settled
		// as reverted with the typed receipt, which is the definite end the operation already ended with.
		if refusal, ok := platformCapacityRefusalOf(readErr); ok && refusal.Matches(journal.CapacityIdentityOfHeader(call.command.CommandHeader)) {
			raw, _ := json.Marshal(refusal)
			return rows.settle(ctx, call.command, "reverted", string(raw))
		}
		return readErr
	}
	// The runner needs the bytes. Hash them again now; if they are gone the load cannot settle until the package is
	// uploaded again, which the settings status says.
	if err := s.verifyPlatformImageBlobs(target); err != nil {
		log.Printf("app-platform load-image settlement %s: package re-upload required for release %s: %v", lease.id, target.releaseSHA, err)
		return err
	}
	return s.runPlatformImageLoad(ctx, endpoint, signer, target, rows)
}

// platformImageDeliveryProjection reports the first release this installation needs whose image blobs cannot be
// used: the current release, or the target of an operation that ended recovery_required or holds a load with no
// verdict. It checks presence and the stored manifest, not hashes. The same fact makes the sweep stop (fail
// closed), so it is surfaced here for the operator: upload the package of that release again.
func (s *Server) platformImageDeliveryProjection(e migExec, installation, current string) (*platformSettingsImageDelivery, error) {
	releases := []string{}
	if platformValidSHA(current) {
		releases = append(releases, current)
	}
	rows, err := e.query(`SELECT DISTINCT p.target_release_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.managed=1 AND p.target_release_sha256 IS NOT NULL
		AND (o.stage='recovery_required' OR EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.kind='load_image' AND f.state IN ('prepared','unknown')))`, installation)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			rows.Close()
			return nil, err
		}
		releases = append(releases, sha)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	store, err := s.platformImages()
	if err != nil {
		return nil, nil
	}
	for _, sha := range releases {
		var descriptor []byte
		if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, sha).Scan(&descriptor); err != nil {
			continue
		}
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.Manifest.Backend == nil {
			continue
		}
		for _, entry := range parsed.Manifest.Backend.Images {
			plan, err := store.peekPlan(entry)
			if err != nil {
				return &platformSettingsImageDelivery{State: "reupload_required", ReleaseSHA256: sha, Reason: "manifest_unreadable"}, nil
			}
			if len(store.missingBlobs(plan.Blobs())) != 0 {
				return &platformSettingsImageDelivery{State: "reupload_required", ReleaseSHA256: sha, Reason: "blobs_missing"}, nil
			}
		}
	}
	return nil, nil
}

// platformImageStorageStatus lists every pinned release whose stored image cannot be used: the manifest or config
// cannot be read (the sweep stops on these, fail-closed) or a blob is missing. It is global, not per installation,
// and it reads without writing anything. The operator's fix is the same for each: upload the package again.
type platformImageStorageStatus struct {
	Unreadable []platformImageUnreadable `json:"unreadable"`
}

type platformImageUnreadable struct {
	ReleaseSHA256 string `json:"release_sha256"`
	AppID         string `json:"app_id"`
	Version       string `json:"version"`
	// Reason is "manifest_unreadable" or "blobs_missing".
	Reason string `json:"reason"`
}

func (s *Server) platformImageStorageStatus(ctx context.Context) (platformImageStorageStatus, error) {
	result := platformImageStorageStatus{Unreadable: []platformImageUnreadable{}}
	store, err := s.platformImages()
	if err != nil {
		return result, nil
	}
	pool := migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}}
	pins, err := loadPlatformReleasePins(pool, time.Now().UnixMilli())
	if err != nil {
		return result, err
	}
	shas := make([]string, 0, len(pins))
	for sha := range pins {
		shas = append(shas, sha)
	}
	sort.Strings(shas)
	for _, sha := range shas {
		var descriptor []byte
		if err := pool.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, sha).Scan(&descriptor); err != nil {
			continue
		}
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.Manifest.Backend == nil {
			continue
		}
		reason := ""
		for _, entry := range parsed.Manifest.Backend.Images {
			plan, err := store.peekPlan(entry)
			if err != nil {
				reason = "manifest_unreadable"
				break
			}
			if len(store.missingBlobs(plan.Blobs())) != 0 {
				reason = "blobs_missing"
			}
		}
		if reason != "" {
			result.Unreadable = append(result.Unreadable, platformImageUnreadable{ReleaseSHA256: sha, AppID: parsed.Manifest.ID, Version: parsed.Manifest.Version, Reason: reason})
		}
	}
	return result, nil
}

func (s *Server) apiPlatformImageStorageStatus(w http.ResponseWriter, r *http.Request, user string) {
	if _, err := s.platformHTTPReader(r, user); err != nil {
		platformManagementError(w, err)
		return
	}
	status, err := s.platformImageStorageStatus(r.Context())
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, status)
}
