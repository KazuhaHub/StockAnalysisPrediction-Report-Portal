package app

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errAppPlatformLaunchUnavailable = errors.New("application launch is unavailable")

// A point-in-time logical access binding, not a bearer token or readiness proof.
// Future launch/asset handlers must repeat this gate inside their serving boundary;
// caching this value must never bypass current account, grant or trust checks.
// No HTTP route is registered by this repository.
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
	var zero platformLaunchAccess
	if !platformValidUUID(installationID) {
		return zero, errAppPlatformLaunchUnavailable
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
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	var epoch string
	var markers int
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil || !platformValidUUID(epoch) {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM meta WHERE k='app_platform_restore_paused' AND v='0'`).Scan(&markers); err != nil || markers != 1 {
		return zero, errAppPlatformLaunchUnavailable
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
		return zero, errAppPlatformLaunchUnavailable
	}
	// Managed launch stays denied until execution/readiness and broker adapters exist.
	if desired != "enabled" || phase != "ready" || installationEpoch != epoch || runtime.Valid || generation < 1 || policy < 1 {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := platformLaunchAudience(e, account, installationID); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	var releaseSHA string
	var activationGeneration, activationPolicy, dataFormat int64
	var namespace sql.NullString
	var observed sql.NullInt64
	if err := e.queryRow(`SELECT release_sha256,generation,policy_revision,data_namespace_id,data_format,observed_ms FROM app_platform_activations WHERE installation_id=? AND state='active'`+lock, installationID).
		Scan(&releaseSHA, &activationGeneration, &activationPolicy, &namespace, &dataFormat, &observed); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if activationGeneration != generation || activationPolicy != policy || !namespace.Valid || !platformValidUUID(namespace.String) || !observed.Valid || observed.Int64 < 0 || !platformValidSHA(releaseSHA) {
		return zero, errAppPlatformLaunchUnavailable
	}
	var owner sql.NullString
	var namespaceState string
	var namespaceFormat, writerFence int64
	if err := e.queryRow(`SELECT owner_installation_id,state,data_format,writer_fence FROM app_platform_data_namespaces WHERE id=?`+lock, namespace.String).
		Scan(&owner, &namespaceState, &namespaceFormat, &writerFence); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if !owner.Valid || owner.String != installationID || namespaceState != "active" || namespaceFormat != dataFormat || writerFence < 1 {
		return zero, errAppPlatformLaunchUnavailable
	}
	var unsafe int
	if err := e.queryRow(`SELECT
 (SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND (completed_ms IS NULL OR stage='recovery_required'))+
 (SELECT COUNT(*) FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id WHERE o.installation_id=? AND f.state IN ('prepared','unknown'))+
 (SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND (granted<>0 OR policy_revision<>?))`, installationID, installationID, installationID, policy).Scan(&unsafe); err != nil {
		return zero, err
	}
	if unsafe != 0 {
		return zero, errAppPlatformLaunchUnavailable
	}
	var trustRevision int64
	for _, publisher := range trust.Publishers {
		if publisher.ID == publisherID {
			trustRevision, _ = strconv.ParseInt(publisher.Revision, 10, 64)
		}
	}
	verified, err := platformVerifyStoredRelease(e, releaseSHA, trustRevision, trust, now)
	if err != nil || verified.appID != appID || verified.publisherID != publisherID {
		return zero, errAppPlatformLaunchUnavailable
	}
	var raw, signature []byte
	if err := e.queryRow(`SELECT descriptor,signature FROM app_platform_releases WHERE sha256=?`, releaseSHA).Scan(&raw, &signature); err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil || parsed.Manifest.Backend != nil || platformInstallRequirements(parsed.Manifest) != nil || int64(parsed.Manifest.Data.Schema) != dataFormat {
		return zero, errAppPlatformLaunchUnavailable
	}

	binding := platformLaunchAccess{installationID: installationID, generation: strconv.FormatInt(generation, 10), policyRevision: strconv.FormatInt(policy, 10), entry: parsed.Manifest.Presentation.Entry, releaseSHA: releaseSHA, frontendSHA: parsed.Manifest.Frontend.SHA256}
	if read != nil {
		if err := read(e, binding, parsed.Manifest); err != nil {
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
		return zero, errAppPlatformLaunchUnavailable
	}
	signed, err := appplatform.VerifyPublisherDescriptor(raw, signature, trust, strconv.FormatInt(now, 10))
	if err != nil || signed.Descriptor.SHA256 != releaseSHA {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM meta WHERE (k='app_platform_controller_epoch' AND v=?) OR (k='app_platform_restore_paused' AND v='0')`, epoch).Scan(&markers); err != nil || markers != 2 {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return binding, nil
}

func platformLaunchAudience(e migExec, account platformActorAccount, installationID string) error {
	// Identity/topology must remain stable until the transaction completes, even
	// for older OU helpers that update a row without the lifecycle advisory lock.
	if e.s.driver == "postgres" {
		rows, err := e.query(`SELECT id FROM user_groups ORDER BY id FOR SHARE`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	snapshot, err := e.s.loadOUPolicySnapshotFrom(e.ex)
	if err != nil {
		return err
	}
	group := account.groupID
	if group == 0 {
		group = snapshot.graph.defaultID
	}
	chain, err := snapshot.graph.actualChain(group)
	if err != nil {
		return err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	var grant string
	err = e.queryRow(`SELECT id FROM app_platform_access_grants WHERE installation_id=? AND principal_type='user' AND username=? AND account_created=? AND account_generation=? LIMIT 1`+lock, installationID, account.username, account.created, account.generation).Scan(&grant)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Bounded by actualChain's 64-node limit. Default policy fallback is not an
	// audience ancestor of a historical independent root. Lock the matching grant
	// so revocation cannot commit between its check and the final trust check.
	for _, id := range chain {
		if snapshot.graph.nodes[id].created == "" {
			continue // An absent OU creation identity cannot pin an audience grant.
		}
		err = e.queryRow(`SELECT id FROM app_platform_access_grants WHERE installation_id=? AND principal_type='ou' AND group_id=? AND group_created=? LIMIT 1`+lock, installationID, id, snapshot.graph.nodes[id].created).Scan(&grant)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return errAppPlatformLaunchUnavailable
}
