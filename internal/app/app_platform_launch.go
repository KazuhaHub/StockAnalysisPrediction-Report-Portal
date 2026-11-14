package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errAppPlatformLaunchUnavailable = errors.New("application launch is unavailable")

func platformResolvedRecoveryPredicate(e migExec) string {
	restoreReference := `json_extract(rp.plan_json,'$.checkpoint_operation_id')=o.id`
	planFormat := `json_extract(rp.plan_json,'$.format')`
	if e.s.driver == "postgres" {
		restoreReference = `(rp.plan_json::jsonb->>'checkpoint_operation_id')=o.id`
		planFormat = `(rp.plan_json::jsonb->>'format')`
	}
	// An ordinary restore lands on the generation of the operation it settles. A recovery resume
	// settles the operation its imported checkpoint came from one generation later: it starts from the
	// generation the failed operation had reached, or from a later one when abandoning a replacement
	// runtime advanced the installation's generation in between. The review plan's checkpoint
	// reference below is what binds the resume to the operation.
	generation := `(r.target_generation=o.target_generation OR (` + planFormat + `='` + platformRestoreResumeFormat + `' AND r.source_generation>=o.target_generation))`
	// A failed update a completed retry recovered carries a marker effect, written when the retry activated.
	recovered := `EXISTS (SELECT 1 FROM app_platform_operation_effects rf WHERE rf.operation_id=o.id AND rf.stage='recovered' AND rf.state='applied') OR `
	return `(` + recovered + `EXISTS (
		SELECT 1
		FROM app_platform_operations r
		JOIN app_platform_review_plans rp ON rp.id=r.review_plan_id
		WHERE r.installation_id=o.installation_id
		AND r.action='restore' AND r.managed=1
		AND r.stage='complete' AND r.completed_ms IS NOT NULL
		AND r.completed_ms>=o.completed_ms
		AND ` + generation + `
		AND r.controller_epoch=i.controller_epoch
		AND r.lease_owner IS NULL AND r.lease_until_ms IS NULL
		AND r.error_code IS NULL
		AND rp.action='restore' AND rp.consumed_ms IS NOT NULL
		AND EXISTS (
			SELECT 1 FROM app_platform_operation_effects f
			WHERE f.operation_id=r.id AND f.stage='activate'
			AND f.state='applied'
		)
		AND ` + restoreReference + `
	))`
}

func platformUnresolvedOperationPredicate(e migExec) string {
	return `(o.completed_ms IS NULL OR (
		o.stage='recovery_required' AND NOT ` +
		platformResolvedRecoveryPredicate(e) + `
	))`
}

func platformUnsafeEffectPredicate(e migExec) string {
	return `(o.completed_ms IS NULL OR
		o.stage<>'recovery_required' OR
		NOT ` + platformResolvedRecoveryPredicate(e) + `)`
}

func platformUnresolvedOperationCount(e migExec, installation string) (int, error) {
	var count int
	err := e.queryRow(`SELECT COUNT(*)
		FROM app_platform_operations o
		JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE o.installation_id=? AND `+
		platformUnresolvedOperationPredicate(e),
		installation).Scan(&count)
	return count, err
}

func platformUnresolvedOperationEffectCount(
	e migExec, installation string,
) (int, error) {
	var count int
	err := e.queryRow(`SELECT COUNT(*)
		FROM app_platform_operation_effects f
		JOIN app_platform_operations o ON o.id=f.operation_id
		JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE o.installation_id=?
		AND f.state IN ('prepared','unknown')
		AND `+platformUnsafeEffectPredicate(e),
		installation).Scan(&count)
	return count, err
}

// A point-in-time logical access binding, not a bearer token or readiness proof.
// Future launch/asset handlers must repeat this gate inside their serving boundary;
// caching this value must never bypass current account, grant or trust checks.
// HTTP admission and response policy remain in the central route/handler layer.
type platformLaunchAccess struct {
	installationID, generation, policyRevision, entry, releaseSHA, frontendSHA string
}

func (s *Server) inspectPlatformLaunchAccess(session platformActorSession, installationID string) (platformLaunchAccess, error) {
	return s.withPlatformLaunchRead(session, installationID, nil)
}

// The optional private callback may only do bounded local reads. Its output must
// remain buffered until this transaction's final session/key/epoch check commits.
func (s *Server) withPlatformLaunchRead(session platformActorSession, installationID string, read func(migExec, platformLaunchAccess, appplatform.Manifest) error) (platformLaunchAccess, error) {
	return s.withPlatformLaunchReadContext(context.Background(), session, installationID, read)
}
func (s *Server) withPlatformLaunchReadContext(ctx context.Context, session platformActorSession, installationID string, read func(migExec, platformLaunchAccess, appplatform.Manifest) error) (platformLaunchAccess, error) {
	return s.withPlatformLaunchAccountReadContext(ctx, session, installationID, func(e migExec, binding platformLaunchAccess, m appplatform.Manifest, _ platformActorAccount) error {
		if read != nil {
			return read(e, binding, m)
		}
		return nil
	})
}

func (s *Server) withPlatformLaunchAccountReadContext(ctx context.Context, session platformActorSession, installationID string, read func(migExec, platformLaunchAccess, appplatform.Manifest, platformActorAccount) error) (platformLaunchAccess, error) {
	if !platformValidUUID(installationID) {
		return platformLaunchAccess{}, errAppPlatformLaunchUnavailable
	}
	// Routing hint only. Both concrete transaction paths revalidate the actual
	// runtime and current source under their own complete lock/authority boundary.
	var runtime sql.NullString
	if err := s.st.queryRowContext(ctx, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, installationID).Scan(&runtime); err != nil {
		if ctx.Err() != nil {
			return platformLaunchAccess{}, ctx.Err()
		}
		return platformLaunchAccess{}, errAppPlatformLaunchUnavailable
	}
	if runtime.Valid {
		return s.withPlatformWritableLaunchReadContext(ctx, session, installationID, read)
	}
	return s.withPlatformStaticLaunchReadContext(ctx, session, installationID, read)
}

func (s *Server) withPlatformStaticLaunchReadContext(ctx context.Context, session platformActorSession, installationID string, read func(migExec, platformLaunchAccess, appplatform.Manifest, platformActorAccount) error) (platformLaunchAccess, error) {
	var zero platformLaunchAccess
	fail := func(stage string) (platformLaunchAccess, error) {
		return zero, fmt.Errorf("application launch %s: %w", stage, errAppPlatformLaunchUnavailable)
	}
	if !platformValidUUID(installationID) {
		return fail("identity")
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	// Same ordering as lifecycle writers: principal -> trust -> installation.
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	account, err := platformReadSessionAccount(e, session, now, true, false)
	if err != nil {
		return fail("session")
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return fail("trust")
	}
	var epoch string
	var markers int
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil || !platformValidUUID(epoch) {
		return fail("epoch")
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM meta WHERE k='app_platform_restore_paused' AND v='0'`).Scan(&markers); err != nil || markers != 1 {
		return fail("restore-pause")
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	var appID, publisherID, desired, phase, installationEpoch string
	var runtime sql.NullString
	var generation, policy int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,controller_epoch,runtime_id,generation,policy_revision FROM app_platform_installations WHERE id=?`+lock, installationID).
		Scan(&appID, &publisherID, &desired, &phase, &installationEpoch, &runtime, &generation, &policy); err != nil {
		return fail("installation")
	}
	// This branch remains static-only; runtime-bound reads use their typed history.
	if desired != "enabled" || phase != "ready" || installationEpoch != epoch || runtime.Valid || generation < 1 || policy < 1 {
		return fail("installation-state")
	}
	if err := platformLaunchAudience(e, account, installationID); err != nil {
		return fail("audience")
	}
	var releaseSHA, evidence string
	var activationGeneration, activationPolicy, dataFormat int64
	var namespace sql.NullString
	var observed sql.NullInt64
	if err := e.queryRow(`SELECT release_sha256,generation,policy_revision,data_namespace_id,data_format,observed_ms,readiness_evidence FROM app_platform_activations WHERE installation_id=? AND state='active'`+lock, installationID).
		Scan(&releaseSHA, &activationGeneration, &activationPolicy, &namespace, &dataFormat, &observed, &evidence); err != nil {
		return fail("activation")
	}
	if activationGeneration != generation || activationPolicy != policy || !observed.Valid || observed.Int64 < 0 || !platformValidSHA(releaseSHA) {
		return fail("activation-state")
	}
	if namespace.Valid {
		if !platformValidUUID(namespace.String) {
			return fail("namespace-identity")
		}
		var owner sql.NullString
		var namespaceState string
		var namespaceFormat, writerFence int64
		if err := e.queryRow(`SELECT owner_installation_id,state,data_format,writer_fence FROM app_platform_data_namespaces WHERE id=?`+lock, namespace.String).
			Scan(&owner, &namespaceState, &namespaceFormat, &writerFence); err != nil {
			return fail("namespace")
		}
		if !owner.Valid || owner.String != installationID || namespaceState != "active" || namespaceFormat != dataFormat || writerFence < 1 {
			return fail("namespace-state")
		}
	} else if dataFormat != 0 || generation < 1 || policy < 1 {
		return fail("frontend-format")
	}
	operationUnsafe, err := platformUnresolvedOperationCount(
		e, installationID)
	if err != nil {
		return zero, err
	}
	effectUnsafe, err := platformUnresolvedOperationEffectCount(
		e, installationID)
	if err != nil {
		return zero, err
	}
	var aggregateUnsafe int
	if err := e.queryRow(`SELECT
 (SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision<>?)+
 (SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND granted<>0 AND capability NOT IN ('reports.read','storage.files'))`, installationID, policy, installationID).Scan(&aggregateUnsafe); err != nil {
		return zero, err
	}
	unsafe := operationUnsafe + effectUnsafe + aggregateUnsafe
	if unsafe != 0 {
		return fail("unsafe-effects")
	}
	var trustRevision int64
	for _, publisher := range trust.Publishers {
		if publisher.ID == publisherID {
			trustRevision, _ = strconv.ParseInt(publisher.Revision, 10, 64)
		}
	}
	verified, err := platformVerifyStoredRelease(e, releaseSHA, trustRevision, trust, now)
	if err != nil || verified.appID != appID || verified.publisherID != publisherID {
		return fail("stored-release")
	}
	var raw, signature []byte
	if err := e.queryRow(`SELECT descriptor,signature FROM app_platform_releases WHERE sha256=?`, releaseSHA).Scan(&raw, &signature); err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil || parsed.Manifest.Backend != nil || platformInstallRequirements(parsed.Manifest) != nil {
		return fail("requirements")
	}

	if err := verifyPlatformLaunchCapabilitySnapshot(e, installationID, releaseSHA, epoch, policy, parsed.Manifest); err != nil {
		return zero, err
	}

	if namespace.Valid {
		if int64(parsed.Manifest.Data.Schema) != dataFormat {
			return fail("data-schema")
		}
	} else {
		if err := verifyPlatformFrontendActivation(e, installationID, releaseSHA, epoch, generation, policy, observed.Int64, evidence, parsed.Manifest); err != nil {
			return zero, err
		}
	}

	binding := platformLaunchAccess{installationID: installationID, generation: strconv.FormatInt(generation, 10), policyRevision: strconv.FormatInt(policy, 10), entry: parsed.Manifest.Presentation.Entry, releaseSHA: releaseSHA, frontendSHA: parsed.Manifest.Frontend.SHA256}
	if read != nil {
		if err := read(e, binding, parsed.Manifest, account); err != nil {
			return zero, err
		}
	}
	// All row/trust locks and bounded ZIP inspection precede this clock sample.
	// A reader session or signing key can expire while a lock is being acquired.
	now, err = platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	current, err := platformReadSessionAccount(e, session, now, true, false)
	if err != nil || current != account || observed.Int64 > now {
		return fail("final-session")
	}
	signed, err := appplatform.VerifyPublisherDescriptor(raw, signature, trust, strconv.FormatInt(now, 10))
	if err != nil || signed.Descriptor.SHA256 != releaseSHA {
		return fail("final-release")
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM meta WHERE (k='app_platform_controller_epoch' AND v=?) OR (k='app_platform_restore_paused' AND v='0')`, epoch).Scan(&markers); err != nil || markers != 2 {
		return fail("final-markers")
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return binding, nil
}
