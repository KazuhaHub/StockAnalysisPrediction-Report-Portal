package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// A private point-in-time admission snapshot, never a bearer grant or proof of
// current native readiness. Consumers must check the native boundary and repeat
// admission after IO before releasing any buffered response. Close on all paths.
type platformBackendAccessIntent interface {
	closePlatformBackendIdentity()
	verifyPlatformBackendIdentity(int64) error
}
type platformBackendAccess[T platformBackendAccessIntent] struct {
	intent        T
	viewer        protocol.Viewer
	account       platformActorAccount
	policy, fence int64
	activationID  string
}
type platformManagedBackendAccess = platformBackendAccess[platformBackendDeliveryIntent]
type platformWritableBackendAccess = platformBackendAccess[platformWritableRegistrationIntent]

func (a platformBackendAccess[T]) Close()           { a.intent.closePlatformBackendIdentity() }
func (platformBackendAccess[T]) String() string     { return "managed backend access (redacted)" }
func (a platformBackendAccess[T]) GoString() string { return a.String() }
func (platformBackendAccess[T]) MarshalJSON() ([]byte, error) {
	return nil, errAppPlatformLaunchUnavailable
}
func (a *platformBackendAccess[T]) UnmarshalJSON([]byte) error {
	a.Close()
	*a = platformBackendAccess[T]{}
	return errAppPlatformLaunchUnavailable
}
func (i platformBackendDeliveryIntent) closePlatformBackendIdentity() { i.registration.Close() }
func (i platformBackendDeliveryIntent) verifyPlatformBackendIdentity(now int64) error {
	_, err := control.VerifyBackendRegistration(i.raw, i.signature, i.candidate.authority, now)
	return err
}
func (i platformWritableRegistrationIntent) closePlatformBackendIdentity() { i.registration.Close() }
func (i platformWritableRegistrationIntent) verifyPlatformBackendIdentity(now int64) error {
	_, err := control.VerifyWritableBackendRegistration(i.raw, i.signature, i.start.writer.authority, now)
	return err
}

type platformBackendHistoryBinding struct {
	installation, activation, release, namespace, epoch, readiness string
	format, fence, observed                                        int64
}

// No worker lease is manufactured or renewed here. Completed source operations
// select immutable execution history; current session/audience/policy authorize
// this read. Frontend serving and live backend execution remain separate gates.
func (s *Server) preparePlatformManagedBackendAccess(ctx context.Context, session platformActorSession, installationID string) (platformManagedBackendAccess, error) {
	return s.withPlatformManagedBackendAccess(ctx, session, installationID, nil)
}

// The private optional callback may only perform bounded local cryptography or
// reads. Its buffered output must not escape before the final checks/commit.
func (s *Server) withPlatformManagedBackendAccess(ctx context.Context, session platformActorSession, installationID string, read func(platformManagedBackendAccess, []byte) error) (platformManagedBackendAccess, error) {
	return withPlatformBackendAccess(s, ctx, session, installationID, func(e migExec, b platformBackendHistoryBinding, r platformRuntimeRegistration, seed, dek []byte, now int64) (platformBackendDeliveryIntent, error) {
		return s.loadPlatformManagedBackendHistory(e, b.installation, b.activation, b.release, b.namespace, b.epoch, b.format, b.fence, b.observed, b.readiness, r, seed, dek, now)
	}, read)
}
func (s *Server) preparePlatformWritableBackendAccess(ctx context.Context, session platformActorSession, installationID string) (platformWritableBackendAccess, error) {
	return s.withPlatformWritableBackendAccess(ctx, session, installationID, nil)
}
func (s *Server) withPlatformWritableBackendAccess(ctx context.Context, session platformActorSession, installationID string, read func(platformWritableBackendAccess, []byte) error) (platformWritableBackendAccess, error) {
	return withPlatformBackendAccess(s, ctx, session, installationID, s.loadPlatformWritableBackendHistory, read)
}

// Both families share the current account/OU/audience/trust transaction. Their
// independently checked execution histories and signed domains remain distinct.
func withPlatformBackendAccess[T platformBackendAccessIntent](s *Server, ctx context.Context, session platformActorSession, installationID string, load func(migExec, platformBackendHistoryBinding, platformRuntimeRegistration, []byte, []byte, int64) (T, error), read func(platformBackendAccess[T], []byte) error) (platformBackendAccess[T], error) {
	return withPlatformBackendRead(s, ctx, session, installationID, load, func(_ migExec, a platformBackendAccess[T], seed []byte) error {
		if read != nil {
			return read(a, seed)
		}
		return nil
	})
}

// The optional transaction callback performs only bounded local reads/crypto.
// Source/authority checks and buffered-result release remain shared by all users.
func withPlatformBackendRead[T platformBackendAccessIntent](s *Server, ctx context.Context, session platformActorSession, installationID string, load func(migExec, platformBackendHistoryBinding, platformRuntimeRegistration, []byte, []byte, int64) (T, error), read func(migExec, platformBackendAccess[T], []byte) error) (platformBackendAccess[T], error) {
	var zero platformBackendAccess[T]
	if !platformValidUUID(installationID) {
		return zero, errAppPlatformLaunchUnavailable
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
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	account, err := platformReadSessionAccount(e, session, now, true, false)
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	var epoch string
	var markers int
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil || !platformValidUUID(epoch) {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM meta WHERE k='app_platform_restore_paused' AND v='0'`).Scan(&markers); err != nil || markers != 1 {
		return zero, errAppPlatformLaunchUnavailable
	}
	dek, err := s.readPlatformDEK(e)
	defer clear(dek)
	if err != nil {
		return zero, err
	}
	runtime, seed, err := s.platformDataRuntime(e, epoch, dek)
	defer clear(seed)
	if err != nil {
		return zero, err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	var appID, publisher, desired, phase, storedEpoch string
	var runtimeID sql.NullString
	var generation, policy, fence int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,controller_epoch,runtime_id,generation,policy_revision,control_fence FROM app_platform_installations WHERE id=?`+lock, installationID).Scan(&appID, &publisher, &desired, &phase, &storedEpoch, &runtimeID, &generation, &policy, &fence); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if desired != "enabled" || phase != "ready" || storedEpoch != epoch || !runtimeID.Valid || runtimeID.String != runtime.id || generation != 1 || policy < 1 || fence < 1 {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := platformLaunchAudience(e, account, installationID); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	var activation, release, namespace, readiness string
	var ag, ap, format, observed int64
	if err := e.queryRow(`SELECT id,release_sha256,data_namespace_id,generation,policy_revision,data_format,observed_ms,readiness_evidence FROM app_platform_activations WHERE installation_id=? AND state='active'`+lock, installationID).Scan(&activation, &release, &namespace, &ag, &ap, &format, &observed, &readiness); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if !platformValidUUID(activation) || !platformValidUUID(namespace) || !platformValidSHA(release) || ag != generation || ap != policy || format < 1 || observed < 0 {
		return zero, errAppPlatformLaunchUnavailable
	}
	var unsafe int
	if err := e.queryRow(`SELECT
 (SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND (completed_ms IS NULL OR stage='recovery_required'))+
 (SELECT COUNT(*) FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id WHERE o.installation_id=? AND f.state IN ('prepared','unknown'))+
 (SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND (granted<>0 OR policy_revision<>?))+
 (SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND id<>?)`, installationID, installationID, installationID, policy, installationID, activation).Scan(&unsafe); err != nil || unsafe != 0 {
		return zero, errAppPlatformLaunchUnavailable
	}
	var trustRevision int64
	for _, p := range trust.Publishers {
		if p.ID == publisher {
			trustRevision, _ = strconv.ParseInt(p.Revision, 10, 64)
		}
	}
	verified, err := platformVerifyStoredRelease(e, release, trustRevision, trust, now)
	if err != nil || verified.appID != appID || verified.publisherID != publisher {
		return zero, errAppPlatformLaunchUnavailable
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&descriptor); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != release {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := verifyPlatformLaunchCapabilitySnapshot(e, installationID, release, epoch, policy, parsed.Manifest); err != nil {
		return zero, err
	}
	intent, err := load(e, platformBackendHistoryBinding{installation: installationID, activation: activation, release: release, namespace: namespace, epoch: epoch, format: format, fence: fence, observed: observed, readiness: readiness}, runtime, seed, dek, now)
	if err != nil {
		return zero, err
	}
	failed := true
	defer func() {
		if failed {
			intent.closePlatformBackendIdentity()
		}
	}()
	viewer, err := s.platformViewerIdentity(e, account, installationID, appID)
	if err != nil {
		return zero, err
	}
	result := platformBackendAccess[T]{intent: intent, viewer: viewer, account: account, policy: policy, fence: fence, activationID: activation}
	if read != nil {
		if err := read(e, result, seed); err != nil {
			return zero, err
		}
	}
	now, err = platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	current, err := platformReadSessionAccount(e, session, now, true, false)
	if err != nil || current != account {
		return zero, errAppPlatformLaunchUnavailable
	}
	if _, err := platformVerifyStoredRelease(e, release, trustRevision, trust, now); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM meta WHERE (k='app_platform_controller_epoch' AND v=?) OR (k='app_platform_restore_paused' AND v='0')`, epoch).Scan(&markers); err != nil || markers != 2 {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := intent.verifyPlatformBackendIdentity(time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	failed = false
	return result, nil
}

// Rebuild initial-install history without a live operation lease. This does not
// treat old health/receipts as live readiness. The enclosing read owns all current
// session, audience, trust, runtime and installation checks and transaction locks.
func (s *Server) loadPlatformManagedBackendHistory(e migExec, installation, activation, release, namespace, epoch string, format, fence, observed int64, readiness string, runtime platformRuntimeRegistration, seed, dek []byte, now int64) (platformBackendDeliveryIntent, error) {
	var zero platformBackendDeliveryIntent
	if len(seed) != ed25519.SeedSize || observed > now {
		return zero, errAppPlatformLaunchUnavailable
	}
	source := platformManagedSource{installationID: installation, epoch: epoch, generation: 1, fence: fence}
	var planRaw, planSHA string
	var descriptor []byte
	var count int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=?`, installation).Scan(&count); err != nil || count != 1 {
		return zero, errAppPlatformLaunchUnavailable
	}
	var completed, created, updated int64
	if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,r.descriptor,o.completed_ms,o.created_ms,o.updated_ms FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 WHERE o.installation_id=? AND o.action='install' AND o.managed=1 AND o.source_generation=0 AND o.target_generation=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND o.error_code IS NULL AND o.controller_epoch=? AND o.fence=? AND p.action='install' AND p.app_id=r.app_id AND p.publisher_id=r.publisher_id AND p.target_release_sha256=? AND p.controller_epoch=? AND p.consumed_ms IS NOT NULL`, installation, epoch, fence, release, epoch).Scan(&source.operationID, &planRaw, &planSHA, &descriptor, &completed, &created, &updated); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if !platformValidUUID(source.operationID) || created < 0 || completed < created || observed > completed || updated < completed || updated > now || platformDigest([]byte(planRaw)) != planSHA {
		return zero, errAppPlatformLaunchUnavailable
	}
	var plan platformInstallProjection
	if json.Unmarshal([]byte(planRaw), &plan) != nil || plan.Format != platformManagedInstallFormat || plan.ControllerEpoch != epoch || plan.ReleaseSHA256 != release || plan.Runtime == nil || plan.DataQuotaBytes != 256<<20 {
		return zero, errAppPlatformLaunchUnavailable
	}
	canonical, _ := json.Marshal(plan)
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || !bytes.Equal(canonical, []byte(planRaw)) || parsed.SHA256 != release || parsed.Manifest.ID != plan.AppID || parsed.Manifest.Publisher != plan.PublisherID || int64(parsed.Manifest.Data.Schema) != format || installRequirementsForTarget(parsed.Manifest, plan.Format, plan.Runtime) != nil || validateManagedInstallTarget(e, plan.Runtime, epoch) != nil || plan.Runtime.RuntimeID != runtime.id {
		return zero, errAppPlatformLaunchUnavailable
	}
	var owner sql.NullString
	var origin, state string
	var storedFormat, quota, used, writer, revision int64
	var retained, deleted sql.NullInt64
	if err := e.queryRow(`SELECT origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms FROM app_platform_data_namespaces WHERE id=?`, namespace).Scan(&origin, &owner, &state, &storedFormat, &quota, &used, &writer, &revision, &retained, &deleted, &created, &updated); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if origin != installation || !owner.Valid || owner.String != installation || state != "active" || storedFormat != format || quota != 256<<20 || used < 0 || used > quota || writer != 1 || revision != 2 || retained.Valid || deleted.Valid || created < 0 || updated < created || updated > completed {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=?`, installation, installation).Scan(&count); err != nil || count != 1 {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, source.operationID).Scan(&count); err != nil || count != 3 {
		return zero, errAppPlatformLaunchUnavailable
	}
	dataEffect, err := loadPlatformManagedHistoryEffect(e, source, runtime.id, "prepare_data", "prepare-data", completed)
	if err != nil {
		return zero, err
	}
	dataCommand, err := platformDataCommandForSource(e, source, runtime, namespace, dataEffect.id)
	data, parseErr := journal.ParseDataPreparationReceipt([]byte(dataEffect.receipt))
	if err != nil || parseErr != nil || dataCommand.PayloadSHA256 != dataEffect.digest || !data.MatchesCommand(dataCommand) || data.UsedBytes != strconv.FormatInt(used, 10) || data.Claim.RootFilesystemUUID != plan.Runtime.Profile.RootFilesystemUUID || data.Claim.RootInode != plan.Runtime.Profile.RootInode || !platformManagedHistoryTime(data.ObservedMS, completed) {
		return zero, errAppPlatformLaunchUnavailable
	}
	startEffect, err := loadPlatformManagedHistoryEffect(e, source, runtime.id, "start", "start-candidate", completed)
	if err != nil {
		return zero, err
	}
	c := control.CandidateCommand{Format: control.CandidateFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: epoch, ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: installation, OperationID: source.operationID, TargetGeneration: "1", Stage: "start", EffectNumber: "1", EffectID: startEffect.id, Kind: "start-candidate", InstallationFence: strconv.FormatInt(fence, 10), Payload: control.CandidatePayload{ResourceID: activation, DataNamespaceID: namespace, ReleaseSHA256: release, Platform: plan.Runtime.Profile.Platform, DataFormat: strconv.FormatInt(format, 10), WriterFence: "1", SandboxProfile: plan.Runtime.Profile.SandboxProfile}}
	c.PayloadSHA256, err = control.CandidateLogicalDigest(c)
	running, parseErr := journal.ParseCandidateRunningReceipt([]byte(startEffect.receipt))
	if err != nil || parseErr != nil || c.PayloadSHA256 != startEffect.digest || !running.MatchesCommand(c) || running.Claim.DataReceiptSHA256 != platformDigest([]byte(dataEffect.receipt)) || !platformManagedHistoryTime(running.SettledMS, completed) {
		return zero, errAppPlatformLaunchUnavailable
	}
	health, err := journal.ParseCandidateReadiness([]byte(readiness))
	if err != nil || health.PayloadSHA256 != c.PayloadSHA256 || health.NativeID != running.NativeID || health.RunningReceiptSHA256 != platformDigest([]byte(startEffect.receipt)) || health.DataFormat != c.Payload.DataFormat || health.ObservedMS != strconv.FormatInt(observed, 10) {
		return zero, errAppPlatformLaunchUnavailable
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLaunchUnavailable
	}
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(issued+30000, 10)
	authority := control.Authority{DeploymentID: runtime.deploymentID, ControllerEpoch: epoch, ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	raw, signature, err := control.SignCandidate(c, key, authority, issued)
	if err != nil {
		return zero, err
	}
	candidate := platformCandidateIntent{resourceID: activation, effectID: startEffect.id, runtime: runtime, authority: authority, command: c, raw: raw, signature: signature, descriptor: descriptor, data: data, dataSHA: platformDigest([]byte(dataEffect.receipt)), running: running, readiness: health}
	registration, err := platformBackendRegistrationForSource(e, source, runtime, candidate, dek)
	if err != nil {
		return zero, err
	}
	failed := true
	defer func() {
		if failed {
			registration.Close()
		}
	}()
	document, err := registration.Document()
	defer clear(document)
	if err != nil {
		return zero, err
	}
	deliveryEffect, err := loadPlatformManagedHistoryEffect(e, source, runtime.id, "activate", "activate", completed)
	if err != nil {
		return zero, err
	}
	command := control.BackendRegistrationCommand{Format: control.BackendRegistrationFormat, EffectID: deliveryEffect.id, CandidateRaw: raw, CandidateSignature: signature, DocumentSHA256: platformDigest(document)}
	command.PayloadSHA256, err = control.BackendRegistrationLogicalDigest(command)
	receipt, parseErr := journal.ParseBackendRegistrationReceipt([]byte(deliveryEffect.receipt))
	if err != nil || parseErr != nil || command.PayloadSHA256 != deliveryEffect.digest || !receipt.MatchesCommand(command) || receipt.Claim.SourceSHA256 != platformDigest([]byte(startEffect.receipt)) || receipt.Claim.NativeID != running.NativeID || !platformManagedHistoryTime(receipt.SettledMS, completed) {
		return zero, errAppPlatformLaunchUnavailable
	}
	signed, sig, err := control.SignBackendRegistration(command, key, authority, issued)
	if err != nil {
		return zero, err
	}
	failed = false
	return platformBackendDeliveryIntent{candidate: candidate, command: command, raw: signed, signature: sig, registration: registration, applied: true, receipt: receipt}, nil
}

func platformManagedHistoryTime(value string, completed int64) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n >= 0 && n <= completed
}

type platformManagedHistoryEffect struct{ id, digest, receipt string }

func loadPlatformManagedHistoryEffect(e migExec, source platformManagedSource, runtime, stage, kind string, now int64) (platformManagedHistoryEffect, error) {
	var r platformManagedHistoryEffect
	var created, updated int64
	err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=1 AND kind=? AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND observed_fence=? AND state='applied'`, source.operationID, stage, kind, runtime, source.epoch, source.fence, source.fence).Scan(&r.id, &r.digest, &r.receipt, &created, &updated)
	if err != nil || !platformValidUUID(r.id) || !platformValidSHA(r.digest) || created < 0 || updated < created || updated > now {
		return platformManagedHistoryEffect{}, errAppPlatformLaunchUnavailable
	}
	return r, nil
}
