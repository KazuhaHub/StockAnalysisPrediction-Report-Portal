package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// What an anonymous public read may rely on, read fresh for every request (doc 179 section 3.3
// step 3). There is no session: authority is the installation, its active release and the
// public.projection grant. The grant is the whole consent; a request is never trusted for more.

var (
	// errPlatformPublicNotFound is the uniform refusal: unknown installation, not enabled or not
	// ready, projection off or not declared. They are indistinguishable to the caller.
	errPlatformPublicNotFound = errors.New("public projection not found")
	// errPlatformPublicUnavailable means the projection is on but cannot be served right now:
	// a restore pause, an unfinished operation, a trust or release check that failed.
	errPlatformPublicUnavailable = errors.New("public projection unavailable")
)

type platformPublicAuthority struct {
	installation   string
	generation     int64
	policyRevision int64
	release        string
	frontendSHA    string
	manifest       appplatform.Manifest
	declared       appplatform.Public
	policy         appplatform.PublicPolicy
	policyJSON     string
}

// tuple is the identity of this authority's content. Cache entries live and die by it.
func (a platformPublicAuthority) tuple() string {
	return strconv.FormatInt(a.generation, 10) + "|" + strconv.FormatInt(a.policyRevision, 10) + "|" + a.release + "|" + platformDigest([]byte(a.policyJSON))
}

func (a platformPublicAuthority) route(id string) (appplatform.PublicRoute, bool) {
	for _, granted := range a.policy.Routes {
		if granted != id {
			continue
		}
		for _, route := range a.declared.Routes {
			if route.ID == id {
				return route, true
			}
		}
	}
	return appplatform.PublicRoute{}, false
}

func (s *Server) readPlatformPublicAuthority(ctx context.Context, installation string) (platformPublicAuthority, error) {
	var zero platformPublicAuthority
	if !platformValidUUID(installation) {
		return zero, errPlatformPublicNotFound
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("%w: %v", errPlatformPublicUnavailable, err)
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	var desired, phase, storedEpoch string
	var runtimeID sql.NullString
	var generation, policy int64
	if err := e.queryRow(`SELECT desired_state,phase,controller_epoch,runtime_id,generation,policy_revision FROM app_platform_installations WHERE id=?`+lock, installation).Scan(&desired, &phase, &storedEpoch, &runtimeID, &generation, &policy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, errPlatformPublicNotFound
		}
		return zero, fmt.Errorf("%w: %v", errPlatformPublicUnavailable, err)
	}
	if desired != "enabled" || phase != "ready" || !runtimeID.Valid {
		return zero, errPlatformPublicNotFound
	}
	var release string
	var ag, ap int64
	var descriptor []byte
	if err := e.queryRow(`SELECT a.release_sha256,a.generation,a.policy_revision,r.descriptor FROM app_platform_activations a JOIN app_platform_releases r ON r.sha256=a.release_sha256 WHERE a.installation_id=? AND a.state='active'`+lock, installation).Scan(&release, &ag, &ap, &descriptor); err != nil {
		return zero, errPlatformPublicNotFound
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != release || parsed.Manifest.Public == nil || parsed.Manifest.Backend == nil {
		return zero, errPlatformPublicNotFound
	}
	// The grant is read before any state that could reveal that the installation exists but is
	// busy: an installation with the projection off answers 404 whatever else is going on.
	var granted int
	var policyJSON string
	if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection' AND policy_revision=?`+lock, installation, policy).Scan(&granted, &policyJSON); err != nil || granted != 1 {
		return zero, errPlatformPublicNotFound
	}
	declared := *parsed.Manifest.Public
	chosen, err := appplatform.ParsePublicPolicy(policyJSON, declared)
	if err != nil {
		return zero, errPlatformPublicNotFound
	}
	var epoch string
	var markers int
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil || epoch != storedEpoch {
		return zero, errPlatformPublicUnavailable
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM meta WHERE k='app_platform_restore_paused' AND v='0'`).Scan(&markers); err != nil || markers != 1 {
		return zero, errPlatformPublicUnavailable
	}
	if ag != generation || ap != policy {
		return zero, errPlatformPublicUnavailable
	}
	operations, err := platformUnresolvedOperationCount(e, installation)
	if err != nil || operations != 0 {
		return zero, errPlatformPublicUnavailable
	}
	effects, err := platformUnresolvedOperationEffectCount(e, installation)
	if err != nil || effects != 0 {
		return zero, errPlatformPublicUnavailable
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, errPlatformPublicUnavailable
	}
	var appID, publisher string
	if err := e.queryRow(`SELECT app_id,publisher_id FROM app_platform_installations WHERE id=?`, installation).Scan(&appID, &publisher); err != nil {
		return zero, errPlatformPublicUnavailable
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, errPlatformPublicUnavailable
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, errPlatformPublicUnavailable
	}
	var trustRevision int64
	for _, p := range trust.Publishers {
		if p.ID == publisher {
			trustRevision, _ = strconv.ParseInt(p.Revision, 10, 64)
		}
	}
	rows, err := platformReadStoredRelease(e, release)
	if err != nil {
		return zero, errPlatformPublicUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, errPlatformPublicUnavailable
	}
	// What remains reads nothing: it verifies the rows read above against current publisher trust.
	verified, err := verifyPlatformStoredReleaseRows(rows, release, trustRevision, trust, now)
	if err != nil || verified.appID != appID || verified.publisherID != publisher {
		return zero, errPlatformPublicUnavailable
	}
	return platformPublicAuthority{installation: installation, generation: generation, policyRevision: policy, release: release, frontendSHA: parsed.Manifest.Frontend.SHA256, manifest: parsed.Manifest, declared: declared, policy: chosen, policyJSON: policyJSON}, nil
}
