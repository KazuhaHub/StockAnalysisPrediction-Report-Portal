package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Abandoning the replacement runtime (doc 168, owner decision).
//
// An import or resume whose effect stays unknown wedges recovery: the grant stays issued, the
// installation cannot be imported or resumed again, and recovery cannot end. When the owner decides the
// replacement host is to be discarded, the operator attests it, and this one reviewed action settles
// everything that rested on it without asking it anything:
//
//   - the replacement runtime binding is retired (enabled=0, no key), which is the state a restore
//     leaves old bindings in, so a new replacement with a fresh journal and deployment can be enrolled;
//   - every grant still issued on it is revoked;
//   - every unsettled import and resume effect on it is marked reverted, with the reason recorded and
//     no native action: the host is gone, and a late receipt from it is refused because the runtime is
//     retired, the grant revoked and the effect settled;
//   - the operations still in flight on it are ended;
//   - every installation placed on it returns to awaiting review (one the administrator already disabled
//     in recovery is left as it is, and listed apart): disabled, with the recovery set and
//     the data namespace awaiting review again and no activation, ready for a new import.
//
// It is admitted only while the Portal is paused. The decision belongs to the deployment, not to an
// installation, so it is not a review plan: the review-plan table is installation-shaped (it demands an
// application and a publisher) and a runtime-level row there would need borrowed values. Instead the
// action is a prepare and a confirm, like the replacement runtime's. Prepare lists exactly what will
// change and the digest of that listing; confirm names the digest and is refused if any of the listed
// work changed since. The confirmation is idempotent: its outcome is recorded under a meta key naming the
// runtime and the controller epoch, and the same confirmation again answers the recorded outcome without acting.

const (
	platformAbandonRuntimeFormat = "portal.recovery-abandon-runtime-review/1-dev.1"
	platformAbandonReason        = "runtime abandoned"
	// platformAbandonedRuntimeKeyPrefix names the meta key that records the outcome of an abandon; the
	// key carries the runtime and the controller epoch it was decided in.
	platformAbandonedRuntimeKeyPrefix = "app_platform_abandoned_runtime:"
)

var errAppPlatformAbandonUnavailable = errors.New("application recovery has no replacement runtime to abandon")

type platformAbandonRuntimePlan struct {
	Format          string   `json:"format"`
	RuntimeID       string   `json:"runtime_id"`
	DeploymentID    string   `json:"deployment_id"`
	RuntimeRevision int64    `json:"runtime_revision"`
	ControllerEpoch string   `json:"controller_epoch"`
	Installations   []string `json:"installations"`
	// DataRoots names the data roots ("filesystem uuid/root inode") that imports on this runtime were
	// applied to. They are recorded with the abandon: a replacement landing on one of them is refused.
	DataRoots []string `json:"data_roots"`
	// Disabled lists installations the administrator already disabled in recovery. They are left as
	// they are: abandoning the runtime does not revisit that decision.
	Disabled   []string `json:"disabled"`
	Grants     []string `json:"grants"`
	Effects    []string `json:"effects"`
	Operations []string `json:"operations"`
	// Finished lists the operations that already ended recovery_required on this runtime and rested on
	// an effect this abandon settles: they are settled to failed with it.
	Finished []string `json:"finished"`
	Reason   string   `json:"reason"`
}

// platformAbandonedRootsKey names the meta record of the data roots of abandoned runtimes.
const platformAbandonedRootsKey = "app_platform_abandoned_data_roots"

const platformAbandonedRootsLimit = 64

type platformAbandonedRoot struct {
	RuntimeID  string `json:"runtime_id"`
	Filesystem string `json:"filesystem_uuid"`
	RootInode  string `json:"root_inode"`
}

func loadPlatformAbandonedRoots(e migExec) ([]platformAbandonedRoot, error) {
	var raw string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k=?),'')`, platformAbandonedRootsKey).Scan(&raw); err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var roots []platformAbandonedRoot
	if json.Unmarshal([]byte(raw), &roots) != nil || len(roots) > platformAbandonedRootsLimit {
		return nil, errAppPlatformStageEvidence
	}
	return roots, nil
}

// refusePlatformAbandonedRoot refuses an import receipt whose data root is one an abandoned runtime's
// import landed on. One journal binds one data root, and the abandoned host was never asked what it had
// done there, so a new journal must not take that root over. It fails closed: an unreadable record
// refuses too.
func refusePlatformAbandonedRoot(e migExec, receipt journal.ImportCheckpointReceipt) error {
	roots, err := loadPlatformAbandonedRoots(e)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	for _, r := range roots {
		if r.Filesystem == receipt.Claim.RootFilesystemUUID && r.RootInode == receipt.Claim.RootInode {
			return errAppPlatformStageEvidence
		}
	}
	return nil
}

// platformReplacementRootKey names the meta record of the data root the replacement runtime reported
// when it was confirmed. It holds one record: only one runtime is ever the replacement in force, and the
// root of a retired one moves to the abandoned roots when it is abandoned.
const platformReplacementRootKey = "app_platform_replacement_root"

func loadPlatformReplacementRoot(e migExec) (platformAbandonedRoot, bool, error) {
	var raw string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k=?),'')`, platformReplacementRootKey).Scan(&raw); err != nil {
		return platformAbandonedRoot{}, false, err
	}
	if raw == "" {
		return platformAbandonedRoot{}, false, nil
	}
	var root platformAbandonedRoot
	if json.Unmarshal([]byte(raw), &root) != nil || !platformValidUUID(root.RuntimeID) || !platformValidUUID(root.Filesystem) || root.RootInode == "" {
		return platformAbandonedRoot{}, false, errAppPlatformStageEvidence
	}
	return root, true, nil
}

// platformRootAbandoned reports whether a data root belongs to an abandoned runtime.
func platformRootAbandoned(e migExec, filesystem, inode string) (bool, error) {
	roots, err := loadPlatformAbandonedRoots(e)
	if err != nil {
		return false, errAppPlatformStageEvidence
	}
	for _, r := range roots {
		if r.Filesystem == filesystem && r.RootInode == inode {
			return true, nil
		}
	}
	return false, nil
}

// recordPlatformReplacementRoot records the data root the runner reported for the replacement runtime. The
// root is recorded even when it is an abandoned one, so that the import review can keep refusing it; the
// caller learns of the refusal from the error. The runtime must be the binding in force.
func (s *Server) recordPlatformReplacementRoot(session platformActorSession, runtimeID, filesystem, inode string) error {
	if !platformValidUUID(runtimeID) || !platformValidUUID(filesystem) || inode == "" {
		return errAppPlatformRuntimeConflict
	}
	refused := false
	err := s.withPlatformRecoveryTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		active, found, _, err := platformActiveRuntime(e, true)
		if err != nil {
			return err
		}
		if !found || active.id != runtimeID || active.enabled != 1 {
			return errAppPlatformRuntimeConflict
		}
		if refused, err = platformRootAbandoned(e, filesystem, inode); err != nil {
			return err
		}
		raw, err := json.Marshal(platformAbandonedRoot{RuntimeID: runtimeID, Filesystem: filesystem, RootInode: inode})
		if err != nil {
			return errAppPlatformStageEvidence
		}
		if err := e.setSetting(platformReplacementRootKey, string(raw)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return err
	}
	if refused {
		return errAppPlatformRuntimeConflict
	}
	return nil
}

// refusePlatformReplacementOnAbandonedRoot refuses to work with a runtime whose recorded data root is an
// abandoned one. A runtime with no recorded root is not refused here: its import receipt is still checked
// against the abandoned roots when it lands.
func refusePlatformReplacementOnAbandonedRoot(e migExec, runtimeID string) error {
	root, found, err := loadPlatformReplacementRoot(e)
	if err != nil {
		return err
	}
	if !found || root.RuntimeID != runtimeID {
		return nil
	}
	abandoned, err := platformRootAbandoned(e, root.Filesystem, root.RootInode)
	if err != nil {
		return err
	}
	if abandoned {
		return errAppPlatformRuntimeConflict
	}
	return nil
}

// loadPlatformAbandonDataRoots reads the roots from the applied import receipts of the runtime, and the
// root the runtime reported when it was confirmed: an import that is lost or still in flight may have
// landed on it although no receipt says so.
func loadPlatformAbandonDataRoots(e migExec, runtimeID string) ([]string, error) {
	rows, err := e.query(`SELECT receipt_json FROM app_platform_operation_effects WHERE runtime_id=? AND kind='import-checkpoint' AND state='applied' ORDER BY effect_id`, runtimeID)
	if err != nil {
		return nil, err
	}
	var raws []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		raws = append(raws, raw)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []string{}
	seen := map[string]bool{}
	if recorded, found, err := loadPlatformReplacementRoot(e); err != nil {
		return nil, err
	} else if found && recorded.RuntimeID == runtimeID {
		root := recorded.Filesystem + "/" + recorded.RootInode
		seen[root] = true
		result = append(result, root)
	}
	for _, raw := range raws {
		receipt, err := journal.ParseImportCheckpointReceipt([]byte(raw))
		if err != nil || !platformValidUUID(receipt.Claim.RootFilesystemUUID) || receipt.Claim.RootInode == "" {
			return nil, errAppPlatformStageEvidence
		}
		root := receipt.Claim.RootFilesystemUUID + "/" + receipt.Claim.RootInode
		if !seen[root] {
			seen[root] = true
			result = append(result, root)
		}
	}
	return result, nil
}

func platformIDList(e migExec, query string, args ...any) ([]string, error) {
	rows, err := e.query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func loadPlatformAbandonRuntimePlan(e migExec, runtimeID string, control platformActorControl) (platformAbandonRuntimePlan, error) {
	var p platformAbandonRuntimePlan
	runtime, found, _, err := platformActiveRuntime(e, true)
	if err != nil {
		return p, err
	}
	if !found || runtime.enabled != 1 || !runtime.sealed.Valid || !runtime.pin.Valid || runtime.id != runtimeID || runtime.controllerEpoch != control.epoch {
		return p, errAppPlatformAbandonUnavailable
	}
	p.Format, p.RuntimeID, p.DeploymentID, p.RuntimeRevision, p.ControllerEpoch, p.Reason =
		platformAbandonRuntimeFormat, runtime.id, runtime.deploymentID, runtime.revision, runtime.controllerEpoch, platformAbandonReason
	if p.Installations, err = platformIDList(e, `SELECT id FROM app_platform_installations WHERE runtime_id=? AND desired_state<>'removed' AND phase<>'disabled' ORDER BY id`, runtime.id); err != nil {
		return p, err
	}
	if p.Disabled, err = platformIDList(e, `SELECT id FROM app_platform_installations WHERE runtime_id=? AND desired_state<>'removed' AND phase='disabled' ORDER BY id`, runtime.id); err != nil {
		return p, err
	}
	if p.Grants, err = platformIDList(e, `SELECT id FROM app_platform_import_grants WHERE runtime_id=? AND state='issued' ORDER BY id`, runtime.id); err != nil {
		return p, err
	}
	if p.Effects, err = platformIDList(e, `SELECT f.effect_id FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id
		WHERE f.runtime_id=? AND o.action IN ('restore_import','restore') AND f.state IN ('prepared','unknown') ORDER BY f.effect_id`, runtime.id); err != nil {
		return p, err
	}
	if p.Operations, err = platformIDList(e, `SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE i.runtime_id=? AND o.action IN ('restore_import','restore') AND o.completed_ms IS NULL ORDER BY o.id`, runtime.id); err != nil {
		return p, err
	}
	if p.DataRoots, err = loadPlatformAbandonDataRoots(e, runtime.id); err != nil {
		return p, err
	}
	if p.Finished, err = platformIDList(e, `SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE i.runtime_id=? AND o.action IN ('restore_import','restore') AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL
		AND EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.runtime_id=? AND f.state IN ('prepared','unknown'))
		AND NOT EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.runtime_id<>? AND f.state IN ('prepared','unknown'))
		ORDER BY o.id`, runtime.id, runtime.id, runtime.id); err != nil {
		return p, err
	}
	return p, nil
}

// previewPlatformAbandonRuntime lists what abandoning the runtime would settle, with the digest a
// confirmation must name. It changes nothing.
func (s *Server) previewPlatformAbandonRuntime(ctx context.Context, session platformActorSession, runtimeID string) (platformAbandonRuntimePlan, string, error) {
	var plan platformAbandonRuntimePlan
	var digest string
	if !platformValidUUID(runtimeID) {
		return plan, "", errAppPlatformAbandonUnavailable
	}
	err := s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var err error
		if plan, digest, err = loadPlatformAbandonRuntimeListing(e, runtimeID, control); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformAbandonRuntimePlan{}, "", err
	}
	return plan, digest, nil
}

func loadPlatformAbandonRuntimeListing(e migExec, runtimeID string, control platformActorControl) (platformAbandonRuntimePlan, string, error) {
	plan, err := loadPlatformAbandonRuntimePlan(e, runtimeID, control)
	if err != nil {
		return plan, "", err
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 65536 {
		return plan, "", errAppPlatformPlanChanged
	}
	return plan, platformDigest(raw), nil
}

type platformAbandonResult struct {
	RuntimeID                                  string
	Installations, Grants, Effects, Operations int
}

// platformAbandonOutcome is what is recorded of an abandon: the digest of the listing it acted on and
// what it settled.
type platformAbandonOutcome struct {
	PlanSHA256      string `json:"plan_sha256"`
	RuntimeID       string `json:"runtime_id"`
	ControllerEpoch string `json:"controller_epoch"`
	Installations   int    `json:"installations"`
	Grants          int    `json:"grants"`
	Effects         int    `json:"effects"`
	Operations      int    `json:"operations"`
}

// confirmPlatformAbandonRuntime abandons the runtime the listing named. It reports replayed when the
// same listing was already confirmed: the recorded outcome is answered and nothing is done again.
func (s *Server) confirmPlatformAbandonRuntime(ctx context.Context, session platformActorSession, runtimeID, planSHA string) (platformAbandonResult, bool, error) {
	var result platformAbandonResult
	var replayed bool
	if !platformValidUUID(runtimeID) || !platformValidSHA(planSHA) {
		return result, false, errAppPlatformPlanChanged
	}
	err := s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		result, replayed = platformAbandonResult{}, false
		key := platformAbandonedRuntimeKeyPrefix + runtimeID + ":" + control.epoch
		var recorded string
		switch err := e.queryRow(`SELECT v FROM meta WHERE k=?`, key).Scan(&recorded); {
		case err == nil:
			var outcome platformAbandonOutcome
			if json.Unmarshal([]byte(recorded), &outcome) != nil || outcome.RuntimeID != runtimeID || outcome.PlanSHA256 != planSHA || outcome.ControllerEpoch != control.epoch {
				return errAppPlatformPlanChanged
			}
			result = platformAbandonResult{RuntimeID: runtimeID, Installations: outcome.Installations, Grants: outcome.Grants, Effects: outcome.Effects, Operations: outcome.Operations}
			replayed = true
			_, err = platformRefreshActor(e, session, actor, control)
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		plan, digest, err := loadPlatformAbandonRuntimeListing(e, runtimeID, control)
		if err != nil {
			return err
		}
		if digest != planSHA {
			return errAppPlatformPlanChanged
		}
		now := control.nowMS
		if err := recordPlatformAbandonedRoots(e, plan); err != nil {
			return err
		}
		if err := applyPlatformAbandonRuntime(e, plan, now); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			PlanSHA256    string   `json:"plan_sha256"`
			RuntimeID     string   `json:"runtime_id"`
			DeploymentID  string   `json:"deployment_id"`
			Installations []string `json:"installations"`
			Disabled      []string `json:"disabled"`
			DataRoots     []string `json:"data_roots"`
			Grants        []string `json:"grants"`
			Effects       []string `json:"effects"`
			Operations    []string `json:"operations"`
			Finished      []string `json:"finished"`
			Reason        string   `json:"reason"`
		}{digest, plan.RuntimeID, plan.DeploymentID, plan.Installations, plan.Disabled, plan.DataRoots, plan.Grants, plan.Effects, plan.Operations, plan.Finished, plan.Reason})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.runtime_abandoned','app_platform_runtime',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339), actor.username, actor.ouID, plan.RuntimeID, string(detail)); err != nil {
			return err
		}
		result = platformAbandonResult{RuntimeID: plan.RuntimeID, Installations: len(plan.Installations), Grants: len(plan.Grants), Effects: len(plan.Effects), Operations: len(plan.Operations) + len(plan.Finished)}
		outcome, _ := json.Marshal(platformAbandonOutcome{PlanSHA256: digest, RuntimeID: plan.RuntimeID, ControllerEpoch: control.epoch, Installations: result.Installations,
			Grants: result.Grants, Effects: result.Effects, Operations: result.Operations})
		if err := e.setSetting(key, string(outcome)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformAbandonResult{}, false, err
	}
	return result, replayed, nil
}

func recordPlatformAbandonedRoots(e migExec, plan platformAbandonRuntimePlan) error {
	if len(plan.DataRoots) == 0 {
		return nil
	}
	roots, err := loadPlatformAbandonedRoots(e)
	if err != nil {
		return err
	}
	for _, root := range plan.DataRoots {
		filesystem, inode, ok := strings.Cut(root, "/")
		if !ok {
			return errAppPlatformStageEvidence
		}
		roots = append(roots, platformAbandonedRoot{RuntimeID: plan.RuntimeID, Filesystem: filesystem, RootInode: inode})
	}
	if len(roots) > platformAbandonedRootsLimit {
		return errAppPlatformStageEvidence
	}
	raw, err := json.Marshal(roots)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	return e.setSetting(platformAbandonedRootsKey, string(raw))
}

func applyPlatformAbandonRuntime(e migExec, plan platformAbandonRuntimePlan, now int64) error {
	// Retire the binding first: every settlement below is then final, because nothing can settle on it.
	changed, err := e.exec(`UPDATE app_platform_runtime_bindings SET enabled=0,sealed_control_key=NULL,verification_key_fingerprint=NULL,
		control_revision=control_revision+1,revision=revision+1,updated_ms=? WHERE id=? AND enabled=1 AND revision=? AND controller_epoch=?`,
		now, plan.RuntimeID, plan.RuntimeRevision, plan.ControllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_import_grants SET state='revoked',updated_ms=? WHERE runtime_id=? AND state='issued'`, now, plan.RuntimeID); err != nil {
		return err
	}
	reason, _ := json.Marshal(struct {
		Reason string `json:"reason"`
	}{platformAbandonReason})
	for _, effect := range plan.Effects {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND runtime_id=? AND state IN ('prepared','unknown')`,
			string(reason), now, effect, plan.RuntimeID)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
	}
	for _, id := range plan.Operations {
		var action, stage string
		var managed int
		var generation, revision int64
		if err := e.queryRow(`SELECT action,managed,target_generation,state_revision,stage FROM app_platform_operations WHERE id=? AND completed_ms IS NULL`, id).
			Scan(&action, &managed, &generation, &revision, &stage); err != nil {
			return errAppPlatformPlanChanged
		}
		model, err := appplatform.OperationFromSnapshot(appplatform.OperationSnapshot{Action: appplatform.Action(action), Managed: managed == 1,
			Generation: uint64(generation), Revision: uint64(revision), Stage: appplatform.Stage(stage)})
		if err != nil {
			return errAppPlatformStageEvidence
		}
		ended, err := model.Fail(model.Snapshot().Revision, appplatform.EffectsNone)
		if err != nil {
			return errAppPlatformStageEvidence
		}
		changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code='runtime_abandoned',completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
			WHERE id=? AND completed_ms IS NULL`, string(ended.Snapshot().Stage), int64(ended.Snapshot().Revision), now, now, id)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
	}
	for _, id := range plan.Finished {
		// Every effect of these rested on the retired runtime and is reverted above.
		if err := settlePlatformRecoveryOperation(e, id, "runtime_abandoned", now); err != nil {
			return err
		}
	}
	for _, installation := range plan.Installations {
		if _, err := e.exec(`UPDATE app_platform_installations SET desired_state='disabled',phase='review_required',generation=generation+1,revision=revision+1,
			policy_revision=policy_revision+1,control_fence=control_fence+1,updated_ms=? WHERE id=? AND runtime_id=? AND desired_state<>'removed'`,
			now, installation, plan.RuntimeID); err != nil {
			return err
		}
		for _, q := range []string{
			`UPDATE app_platform_activations SET state='retired',updated_ms=? WHERE installation_id=? AND state='active'`,
			`UPDATE app_platform_data_namespaces SET state='review_required',owner_installation_id=NULL,writer_fence=writer_fence+1,revision=revision+1,updated_ms=?
				WHERE origin_installation_id=? AND state NOT IN ('deleted','deleting')`,
			`UPDATE app_platform_recovery_sets SET state='review_required',updated_ms=? WHERE installation_id=? AND state NOT IN ('deleted','review_required')`,
		} {
			if _, err := e.exec(q, now, installation); err != nil {
				return err
			}
		}
		if _, err := e.exec(`DELETE FROM app_platform_activations WHERE installation_id=? AND state='candidate'`, installation); err != nil {
			return err
		}
	}
	return nil
}
