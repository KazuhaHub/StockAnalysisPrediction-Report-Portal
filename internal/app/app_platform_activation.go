package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformActivationResult struct {
	activationID, installationID string
	snapshot                     appplatform.OperationSnapshot
}
type platformStaticCandidate struct {
	frontendOnly                                                          bool
	reviewSHA                                                             string
	declaredSchema                                                        int
	namespace, effectID, receiptSHA, releaseSHA, frontendSHA, documentSHA string
	format                                                                int64
	audience                                                              []platformInstallAudience
}

// Reconstruct completed data evidence from the exact durable pair. Neither the
// activation caller nor a browser supplies a receipt or a readiness boolean.
func loadPlatformStaticCandidate(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, now int64) (platformStaticCandidate, error) {
	var zero, result platformStaticCandidate
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var origin, state string
	var owner sql.NullString
	var quota, used, writer, revision, created, updated int64
	var retained, deleted sql.NullInt64
	rows, err := e.query(`SELECT id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=? ORDER BY id LIMIT 2`+lock, lease.installationID, lease.installationID)
	if err != nil {
		return zero, err
	}
	count := 0
	for rows.Next() {
		count++
		err = rows.Scan(&result.namespace, &origin, &owner, &state, &result.format, &quota, &used, &writer, &revision, &retained, &deleted, &created, &updated)
		if err != nil {
			rows.Close()
			return zero, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	if count != 1 || !platformValidUUID(result.namespace) || origin != lease.installationID || !owner.Valid || owner.String != lease.installationID || state != "active" || quota != 256<<20 || writer != 1 || revision != 2 || result.format < 1 || retained.Valid || deleted.Valid || created < 0 || updated < created || updated > now {
		return zero, errAppPlatformStageEvidence
	}
	namespaceUpdated := updated
	var effects, forbidden int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id).Scan(&effects); err != nil {
		return zero, err
	}
	if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=?)+(SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?)`, lease.installationID, lease.installationID).Scan(&forbidden); err != nil {
		return zero, err
	}
	if effects != 1 || forbidden != 0 {
		return zero, errAppPlatformStageEvidence
	}
	var kind, payload, runtimeID, epoch, effectState, raw string
	var admitted, observed int64
	err = e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`+lock, lease.id).Scan(&result.effectID, &kind, &payload, &runtimeID, &epoch, &admitted, &observed, &effectState, &raw, &created, &updated)
	if err != nil {
		return zero, err
	}
	if kind != "prepare-data" || runtimeID != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || observed != admitted || effectState != "applied" || !platformValidUUID(result.effectID) || created < 0 || updated < created || updated != namespaceUpdated {
		return zero, errAppPlatformStageEvidence
	}
	command, err := platformDataCommand(e, lease, runtime, result.namespace, result.effectID)
	if err != nil || command.PayloadSHA256 != payload {
		return zero, errAppPlatformStageEvidence
	}
	receipt, err := journal.ParseDataPreparationReceipt([]byte(raw))
	if err != nil || !receipt.MatchesCommand(command) || strconv.FormatInt(result.format, 10) != receipt.DataFormat || strconv.FormatInt(used, 10) != receipt.UsedBytes {
		return zero, errAppPlatformStageEvidence
	}
	observedAt, _ := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	if observedAt > time.Now().UnixMilli() {
		return zero, errAppPlatformStageEvidence
	}
	result.receiptSHA = platformDigest([]byte(raw))
	result.releaseSHA = command.Payload.ReleaseSHA256
	var descriptor, archive []byte
	var review string
	err = e.queryRow(`SELECT r.descriptor,f.content,p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 JOIN app_platform_frontend_artifacts f ON f.sha256=r.frontend_sha256 WHERE o.id=?`, lease.id).Scan(&descriptor, &archive, &review)
	if err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend != nil || int64(parsed.Manifest.Data.Schema) != result.format {
		return zero, errAppPlatformStageEvidence
	}
	document, err := appplatform.BuildFrontendDocument(parsed.Manifest, archive)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result.frontendSHA, result.documentSHA = document.SourceSHA256, document.DocumentSHA256
	var plan platformInstallProjection
	if json.Unmarshal([]byte(review), &plan) != nil {
		return zero, errAppPlatformStageEvidence
	}
	result.audience = plan.Audience
	if err := validatePlatformInstallAudience(e, result.audience, now); err != nil {
		return zero, err
	}
	return result, nil
}
func insertPlatformInitialAudience(e migExec, installation string, audience []platformInstallAudience, now int64) error {
	for _, p := range audience {
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		var username, accountCreated, generation, groupID, groupCreated any
		if p.Type == "user" {
			username, accountCreated = p.Username, p.AccountCreated
			generation, err = strconv.ParseInt(p.AccountGeneration, 10, 64)
		} else {
			groupID, err = strconv.ParseInt(p.GroupID, 10, 64)
			groupCreated = p.GroupCreated
		}
		if err != nil {
			return errAppPlatformStageEvidence
		}
		if _, err := e.exec(`INSERT INTO app_platform_access_grants(id,installation_id,principal_type,username,account_created,account_generation,group_id,group_created,revision,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,1,?,?)`, id, installation, p.Type, username, accountCreated, generation, groupID, groupCreated, now, now); err != nil {
			return err
		}
	}
	return nil
}

// activatePlatformStaticInstall atomically exposes the verified initial static
// generation and its reviewed audience. It starts no backend or native writer.
func (s *Server) activatePlatformStaticInstall(ctx context.Context, expected platformOperationLease) (platformActivationResult, error) {
	var zero platformActivationResult
	if expected.snapshot.Action != appplatform.ActionInstall || expected.snapshot.Managed || expected.snapshot.Generation != 1 || expected.snapshot.Stage != appplatform.StageActivate || expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	profile, err := s.st.platformInstallProfile(ctx, expected.id)
	if err != nil {
		return zero, err
	}
	var dek []byte
	if profile == platformNativeInstallFormat {
		dek, err = s.dek()
		if err != nil {
			return zero, err
		}
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	var runtime platformRuntimeRegistration
	if profile == platformNativeInstallFormat {
		var seed []byte
		runtime, seed, err = s.platformDataRuntime(e, expected.controllerEpoch, dek)
		clear(seed)
		if err != nil {
			return zero, err
		}
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	if facts.installationRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, facts.now); err != nil {
		return zero, err
	}
	currentProfile, err := platformInstallProfileInTransaction(e, expected.id)
	if err != nil {
		return zero, err
	}
	if currentProfile != profile {
		return zero, errAppPlatformStageEvidence
	}
	var candidate platformStaticCandidate
	if profile == platformFrontendInstallFormat {
		candidate, err = loadPlatformFrontendCandidate(e, expected, facts.now)
	} else {
		candidate, err = loadPlatformStaticCandidate(e, expected, runtime, facts.now)
	}
	if err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	activation, err := platformNewUUID()
	if err != nil {
		return zero, err
	}
	audienceJSON, _ := json.Marshal(candidate.audience)
	evidence, _ := json.Marshal(struct {
		Format      string `json:"format"`
		Epoch       string `json:"controller_epoch"`
		Effect      string `json:"effect_id"`
		Namespace   string `json:"namespace_id"`
		ReceiptSHA  string `json:"data_receipt_sha256"`
		FrontendSHA string `json:"frontend_sha256"`
		DocumentSHA string `json:"document_sha256"`
		AudienceSHA string `json:"audience_sha256"`
	}{"portal.static-readiness/1-dev.1", expected.controllerEpoch, candidate.effectID, candidate.namespace, candidate.receiptSHA, candidate.frontendSHA, candidate.documentSHA, platformDigest(audienceJSON)})
	if candidate.frontendOnly {
		evidence = platformFrontendEvidence(candidate, expected.controllerEpoch, expected.installationID, platformDigest(audienceJSON), 1, 1)
	}
	var namespace any = candidate.namespace
	if candidate.frontendOnly {
		namespace = nil
	}
	if len(evidence) > 8192 {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := e.exec(`INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms) VALUES(?,?,?,1,1,?,?,'active',?,?,?,?)`, activation, expected.installationID, candidate.releaseSHA, namespace, candidate.format, string(evidence), now, now, now); err != nil {
		return zero, err
	}
	if err := insertPlatformInitialAudience(e, expected.installationID, candidate.audience, now); err != nil {
		return zero, err
	}
	if err := activatePlatformInstallCapabilities(e, expected.installationID, expected.id, now); err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_installations SET phase='ready',revision=revision+1,updated_ms=? WHERE id=? AND desired_state='enabled' AND phase='pending' AND generation=1 AND policy_revision=1 AND revision=? AND control_fence=? AND controller_epoch=? AND runtime_id IS NULL`, now, expected.installationID, facts.installationRevision, expected.fence, expected.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	complete, err := model.Advance(expected.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,lease_owner=NULL,lease_until_ms=NULL,completed_ms=?,updated_ms=? WHERE id=? AND installation_id=? AND stage='activate' AND state_revision=? AND row_revision=? AND controller_epoch=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(complete.Snapshot().Revision), now, now, expected.id, expected.installationID, int64(expected.snapshot.Revision), expected.rowRevision, expected.controllerEpoch, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Activation  string `json:"activation_id"`
		AudienceSHA string `json:"audience_sha256"`
	}{activation, platformDigest(audienceJSON)})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.install_activated','app_platform_installation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.installationID, string(detail)); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAudience(e, candidate.audience, now); err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformActivationResult{activationID: activation, installationID: expected.installationID, snapshot: complete.Snapshot()}, nil
}
