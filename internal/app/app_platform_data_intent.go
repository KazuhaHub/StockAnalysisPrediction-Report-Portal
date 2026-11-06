package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

type platformDataIntent struct {
	namespaceID, effectID string
	runtime               platformRuntimeRegistration
	authority             control.Authority
	command               control.Command
	raw, signature        []byte
}

// The data service is shared infrastructure. A static installation retains NULL
// runtime_id and managed=false; only this typed data effect references the runner.
func (s *Server) platformDataRuntime(e migExec, epoch string, dek []byte) (platformRuntimeRegistration, []byte, error) {
	var zero, current platformRuntimeRegistration
	lock := ""
	if s.st.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	rows, err := e.query(`SELECT id,deployment_id,controller_epoch,control_revision,revision,enabled,verification_key_fingerprint,sealed_control_key,created_ms,updated_ms FROM app_platform_runtime_bindings ORDER BY id LIMIT 2` + lock)
	if err != nil {
		return zero, nil, err
	}
	var enabled, count int
	var pin, sealed sql.NullString
	var created, updated int64
	for rows.Next() {
		count++
		err = rows.Scan(&current.id, &current.deploymentID, &current.controllerEpoch, &current.controlRevision, &current.revision, &enabled, &pin, &sealed, &created, &updated)
		if err != nil {
			rows.Close()
			return zero, nil, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, nil, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, nil, err
	}
	if count != 1 || enabled != 1 || current.revision != 2 || current.controlRevision < 1 || current.controllerEpoch != epoch || !platformValidUUID(current.id) || !platformValidUUID(current.deploymentID) || !pin.Valid || !platformValidSHA(pin.String) || !sealed.Valid || !strings.HasPrefix(sealed.String, sealPrefix) || created < 0 || updated < created || updated > now {
		return zero, nil, errAppPlatformRuntimeConflict
	}
	seed, err := aesOpen(dek, strings.TrimPrefix(sealed.String, sealPrefix), aad("app-platform", "runtime-control:"+current.id))
	if err != nil || len(seed) != ed25519.SeedSize {
		clear(seed)
		return zero, nil, errAppPlatformRuntimeConflict
	}
	key := ed25519.NewKeyFromSeed(seed)
	current.publicKey = bytes.Clone(key[ed25519.SeedSize:])
	clear(key)
	digest := sha256.Sum256(current.publicKey)
	current.fingerprint = hex.EncodeToString(digest[:])
	if current.fingerprint != pin.String {
		clear(seed)
		return zero, nil, errAppPlatformRuntimeConflict
	}
	return current, seed, nil
}

func platformDataCommand(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, namespace, effect string) (control.Command, error) {
	return platformDataCommandForSource(e, platformManagedSource{operationID: lease.id, installationID: lease.installationID, epoch: lease.controllerEpoch, generation: lease.snapshot.Generation, fence: lease.fence}, runtime, namespace, effect)
}

// A source tuple selects historical bytes. It is not a worker lease or grant.
type platformManagedSource struct {
	operationID, installationID, epoch string
	generation                         uint64
	fence                              int64
}

func platformDataCommandForSource(e migExec, source platformManagedSource, runtime platformRuntimeRegistration, namespace, effect string) (control.Command, error) {
	profile, profileErr := platformInstallProfileInTransaction(e, source.operationID)
	if profileErr != nil {
		return control.Command{}, profileErr
	}
	if profile != platformNativeInstallFormat && !platformManagedFormat(profile) {
		return control.Command{}, errAppPlatformStageEvidence
	}
	var target string
	var raw []byte
	err := e.queryRow(`SELECT p.target_release_sha256,r.descriptor FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 WHERE o.id=?`, source.operationID).Scan(&target, &raw)
	if err != nil {
		return control.Command{}, err
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		return control.Command{}, errAppPlatformStageEvidence
	}
	c := control.Command{Format: control.Format, DeploymentID: runtime.deploymentID, ControllerEpoch: source.epoch, ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: source.installationID, OperationID: source.operationID, TargetGeneration: strconv.FormatUint(source.generation, 10), Stage: "prepare_data", EffectNumber: "1", EffectID: effect, Kind: "prepare-data", InstallationFence: strconv.FormatInt(source.fence, 10), Payload: control.PrepareDataPayload{DataNamespaceID: namespace, ReleaseSHA256: target, DataFormat: strconv.Itoa(parsed.Manifest.Data.Schema), QuotaBytes: "268435456", QuotaInodes: "65536", WriterFence: "1"}}
	c.PayloadSHA256, err = control.LogicalDigest(c)
	return c, err
}

// loadPlatformDataIntent accepts either an entirely fresh pair or the exact
// original unobserved pair. It never repairs a partial row, restores data or
// replaces a historical effect after lease/fence takeover.
func loadPlatformDataIntent(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, now int64, create bool) (platformDataIntent, bool, error) {
	var zero, result platformDataIntent
	var forbidden, effects int
	if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?)+(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=?)`, lease.installationID, lease.installationID).Scan(&forbidden); err != nil {
		return zero, false, err
	}
	if forbidden != 0 {
		return zero, false, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id).Scan(&effects); err != nil {
		return zero, false, err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	rows, err := e.query(`SELECT id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=? ORDER BY id LIMIT 2`+lock, lease.installationID, lease.installationID)
	if err != nil {
		return zero, false, err
	}
	var count int
	var origin, state string
	var owner sql.NullString
	var format, quota, observed, writer, revision, created, updated int64
	var retained, deleted sql.NullInt64
	for rows.Next() {
		count++
		err = rows.Scan(&result.namespaceID, &origin, &owner, &state, &format, &quota, &observed, &writer, &revision, &retained, &deleted, &created, &updated)
		if err != nil {
			rows.Close()
			return zero, false, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, false, err
	}
	fresh := count == 0 && effects == 0
	if fresh {
		if !create {
			return zero, false, errAppPlatformStageEvidence
		}
		result.namespaceID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		result.effectID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
	} else {
		if count != 1 || effects != 1 || !platformValidUUID(result.namespaceID) || origin != lease.installationID || !owner.Valid || owner.String != lease.installationID || state != "review_required" || format != 0 || quota != 256<<20 || observed != 0 || writer != 0 || revision != 1 || retained.Valid || deleted.Valid || created < 0 || created > now || created != updated {
			return zero, false, errAppPlatformStageEvidence
		}
		var kind, digest, runtimeID, epoch, effectState, receipt string
		var admitted, floor int64
		err := e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`+lock, lease.id).Scan(&result.effectID, &kind, &digest, &runtimeID, &epoch, &admitted, &floor, &effectState, &receipt, &created, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			return zero, false, errAppPlatformStageEvidence
		}
		if err != nil {
			return zero, false, err
		}
		if !platformValidUUID(result.effectID) || kind != "prepare-data" || runtimeID != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || floor != 0 || effectState != "prepared" || receipt != "{}" || created < 0 || created > now || created != updated {
			return zero, false, errAppPlatformStageEvidence
		}
		result.command, err = platformDataCommand(e, lease, runtime, result.namespaceID, result.effectID)
		if err != nil || result.command.PayloadSHA256 != digest {
			return zero, false, errAppPlatformStageEvidence
		}
	}
	if fresh {
		result.command, err = platformDataCommand(e, lease, runtime, result.namespaceID, result.effectID)
		if err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_data_namespaces(id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms) VALUES(?,?,?,'review_required',0,268435456,0,0,1,NULL,NULL,?,?)`, result.namespaceID, lease.installationID, lease.installationID, now, now); err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) VALUES(?,'prepare_data',1,?,'prepare-data',?,?,?, ?,0,'prepared','{}',?,?)`, lease.id, result.effectID, result.command.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, now, now); err != nil {
			return zero, false, err
		}
	}
	result.runtime = runtime
	result.authority = control.Authority{DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch, ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint}
	return result, fresh, nil
}

// preparePlatformInstallData creates signed durable intent only. It never opens
// an endpoint, dispatches native work, marks data active or changes an app backend.
func (s *Server) preparePlatformInstallData(ctx context.Context, expected platformOperationLease) (platformDataIntent, error) {
	var zero platformDataIntent
	if expected.snapshot.Action != appplatform.ActionInstall || expected.snapshot.Generation != 1 || expected.snapshot.Stage != appplatform.StagePrepareData {
		return zero, errAppPlatformStageEvidence
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	profile, err := s.st.platformInstallProfile(ctx, expected.id)
	if err != nil {
		return zero, err
	}
	if profile != platformNativeInstallFormat && !platformManagedFormat(profile) {
		return zero, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return zero, err
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
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, facts.now); err != nil {
		return zero, err
	}
	if expected.snapshot.Managed {
		target, err := platformManagedTargetForOperation(e, expected.id)
		if err != nil || target.RuntimeID != runtime.id {
			return zero, errAppPlatformRuntimeConflict
		}
	}
	result, fresh, err := loadPlatformDataIntent(e, expected, runtime, facts.now, true)
	if err != nil {
		return zero, err
	}
	if fresh {
		detail, _ := json.Marshal(struct {
			NamespaceID string `json:"namespace_id"`
			EffectID    string `json:"effect_id"`
		}{result.namespaceID, result.effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.data_intent','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	if _, _, err := loadPlatformDataIntent(e, expected, runtime, now, false); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	issued := time.Now().UnixMilli()
	// DB lease and local signer clocks must agree within one second. Subtract that
	// full bound from the lease expiry instead of extending authority across skew.
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	expires := min(issued+30000, expected.leaseUntilMS-1000)
	if expires <= issued {
		return zero, errAppPlatformLeaseConflict
	}
	result.command.IssuedMS, result.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	result.raw, result.signature, err = control.Sign(result.command, key, result.authority, issued)
	if err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if _, err := control.Verify(result.raw, result.signature, result.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	return result, nil
}
