package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// What an anonymous public read may rely on (doc 179 section 3.3 step 3), in two parts so that the
// work an anonymous request costs stays small and bounded:
//
//   - platformPublicLight is read for EVERY request, cache hits included, in one indexed statement
//     with no archive and no lock: the installation is enabled and ready, the grant is on, and the
//     tuple (generation, policy revision, release, policy) is current. Withdrawal, a policy change
//     and a release change are therefore honoured on the very next request.
//   - platformPublicVerified is the expensive part: publisher trust, the stored release signature,
//     the frontend archive and the public/ file index. It is built once per tuple, kept in memory
//     under the cache bounds, and rebuilt when the tuple changes, the entry is purged or its
//     bounded lifetime ends (so a trust change is noticed within platformPublicVerifiedTTL).
//
// There is no session: authority is the installation, its active release and the grant.

var (
	// errPlatformPublicNotFound is the uniform refusal: unknown installation, not enabled or not
	// ready, projection off or not declared. They are indistinguishable to the caller, and they
	// cost the same single read.
	errPlatformPublicNotFound = errors.New("public projection not found")
	// errPlatformPublicUnavailable means the projection is on but cannot be served right now:
	// a restore pause, an unfinished operation, a trust or release check that failed.
	errPlatformPublicUnavailable = errors.New("public projection unavailable")
	// errPlatformPublicStore marks a database failure of the cheap read. It is still answered as
	// unavailable, but it proves nothing about the identifier asked for, so nothing is counted
	// against it.
	errPlatformPublicStore = errors.New("public projection store failure")
)

// platformPublicVerifiedTTL bounds how long a verified release is trusted without looking again.
const platformPublicVerifiedTTL = 5 * time.Minute

type platformPublicLight struct {
	installation   string
	generation     int64
	policyRevision int64
	release        string
	policyJSON     string
	// What publisher trust looked like when this read was made: the publisher's revision and the
	// key (and its revision) the release was signed with. They are part of the tuple, so disabling
	// a publisher or revoking its key changes the tuple and the very next request starts over.
	publisherRevision int64
	keyID             string
	keyRevision       int64
}

// tuple is the identity of this authority's content. Cache entries live and die by it.
func (l platformPublicLight) tuple() string {
	return strconv.FormatInt(l.generation, 10) + "|" + strconv.FormatInt(l.policyRevision, 10) + "|" + l.release + "|" + platformDigest([]byte(l.policyJSON)) +
		"|" + strconv.FormatInt(l.publisherRevision, 10) + "|" + l.keyID + "|" + strconv.FormatInt(l.keyRevision, 10)
}

// platformPublicVerified is a release whose signature, trust and archive were checked, reduced to
// what the public routes need.
type platformPublicVerified struct {
	manifest    appplatform.Manifest
	declared    appplatform.Public
	policy      appplatform.PublicPolicy
	frontendSHA string
	files       map[string]appplatform.FrontendFile
	filesBytes  int64
}

// platformPublicAuthority is the light read joined with the verified release of the same tuple.
type platformPublicAuthority struct {
	platformPublicLight
	*platformPublicVerified
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

// readPlatformPublicLight is the one statement every anonymous request pays for.
func (s *Server) readPlatformPublicLight(ctx context.Context, installation string) (platformPublicLight, error) {
	var zero platformPublicLight
	if !platformValidUUID(installation) {
		return zero, errPlatformPublicNotFound
	}
	e := migExec{s: s.st, ex: s.st.db}
	query := `SELECT i.desired_state,i.phase,i.controller_epoch,i.runtime_id,i.generation,i.policy_revision,
		a.release_sha256,a.generation,a.policy_revision,g.granted,g.policy_json,
		p.enabled,p.revision,r.verification_key_id,k.revision,k.revoked_ms,k.not_before_ms,k.not_after_ms,
		(SELECT v FROM meta WHERE k='app_platform_controller_epoch'),
		(SELECT COUNT(*) FROM meta WHERE k='app_platform_restore_paused' AND v='0'),
		(SELECT COUNT(*) FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.installation_id=? AND ` + platformUnresolvedOperationPredicate(e) + `),
		(SELECT COUNT(*) FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.installation_id=? AND f.state IN ('prepared','unknown') AND ` + platformUnsafeEffectPredicate(e) + `)
		FROM app_platform_installations i
		LEFT JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active'
		LEFT JOIN app_platform_publishers p ON p.id=i.publisher_id
		LEFT JOIN app_platform_releases r ON r.sha256=a.release_sha256
		LEFT JOIN app_platform_publisher_keys k ON k.publisher_id=r.publisher_id AND k.key_id=r.verification_key_id
		LEFT JOIN app_platform_capability_grants g ON g.installation_id=i.id AND g.capability='public.projection' AND g.policy_revision=i.policy_revision
		WHERE i.id=?`
	var desired, phase, storedEpoch string
	var runtimeID, release, policyJSON, epoch sql.NullString
	var generation, policy int64
	var ag, ap, granted, pubEnabled, pubRevision, keyRevision, keyRevoked, keyBefore, keyAfter sql.NullInt64
	var keyID sql.NullString
	var markers, operations, effects int
	if err := s.st.queryRowContext(ctx, query, installation, installation, installation).Scan(&desired, &phase, &storedEpoch, &runtimeID, &generation, &policy, &release, &ag, &ap, &granted, &policyJSON, &pubEnabled, &pubRevision, &keyID, &keyRevision, &keyRevoked, &keyBefore, &keyAfter, &epoch, &markers, &operations, &effects); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, errPlatformPublicNotFound
		}
		return zero, fmt.Errorf("%w: %w: %v", errPlatformPublicUnavailable, errPlatformPublicStore, err)
	}
	// The grant decides first: an installation with the projection off answers 404 whatever else
	// is going on, so a busy installation cannot be told apart from an absent one.
	if desired != "enabled" || phase != "ready" || !runtimeID.Valid || !release.Valid || !granted.Valid || granted.Int64 != 1 || !policyJSON.Valid || !appplatform.ValidPublicPolicyShape(policyJSON.String) {
		return zero, errPlatformPublicNotFound
	}
	if !epoch.Valid || epoch.String != storedEpoch || markers != 1 || !ag.Valid || !ap.Valid || ag.Int64 != generation || ap.Int64 != policy || operations != 0 || effects != 0 {
		return zero, errPlatformPublicUnavailable
	}
	// Trust is read with the rest, so it is checked on every request, cache hits included: a disabled
	// publisher, a revoked key or a key outside its validity answers 503 at once.
	nowMS := time.Now().UnixMilli()
	if !pubEnabled.Valid || pubEnabled.Int64 != 1 || !pubRevision.Valid || !keyID.Valid || !keyRevision.Valid || keyRevoked.Valid || !keyBefore.Valid || keyBefore.Int64 > nowMS || (keyAfter.Valid && keyAfter.Int64 <= nowMS) {
		return zero, errPlatformPublicUnavailable
	}
	return platformPublicLight{installation: installation, generation: generation, policyRevision: policy, release: release.String, policyJSON: policyJSON.String,
		publisherRevision: pubRevision.Int64, keyID: keyID.String, keyRevision: keyRevision.Int64}, nil
}

// verifyPlatformPublicRelease is the heavy check, run once per tuple (see platformPublicVerifiedTTL).
// It reads the stored release and its archive, so every call is counted.
func (s *Server) verifyPlatformPublicRelease(ctx context.Context, l platformPublicLight) (*platformPublicVerified, error) {
	s.platformPublicArchiveReads.Add(1)
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPlatformPublicUnavailable, err)
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return nil, errPlatformPublicUnavailable
	}
	var appID, publisher string
	if err := e.queryRow(`SELECT app_id,publisher_id FROM app_platform_installations WHERE id=?`, l.installation).Scan(&appID, &publisher); err != nil {
		return nil, errPlatformPublicUnavailable
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return nil, errPlatformPublicUnavailable
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return nil, errPlatformPublicUnavailable
	}
	var trustRevision int64
	for _, p := range trust.Publishers {
		if p.ID == publisher {
			trustRevision, _ = strconv.ParseInt(p.Revision, 10, 64)
		}
	}
	rows, err := platformReadStoredRelease(e, l.release)
	if err != nil {
		return nil, errPlatformPublicUnavailable
	}
	if err := tx.Commit(); err != nil {
		return nil, errPlatformPublicUnavailable
	}
	// What remains reads nothing: it verifies the rows read above against current publisher trust.
	verified, err := verifyPlatformStoredReleaseRows(rows, l.release, trustRevision, trust, now)
	if err != nil || verified.appID != appID || verified.publisherID != publisher {
		return nil, errPlatformPublicUnavailable
	}
	parsed, err := appplatform.ParseDescriptor(rows.raw)
	if err != nil || parsed.SHA256 != l.release || parsed.Manifest.Public == nil || parsed.Manifest.Backend == nil {
		return nil, errPlatformPublicNotFound
	}
	declared := *parsed.Manifest.Public
	policy, err := appplatform.ParsePublicPolicy(l.policyJSON, declared)
	if err != nil {
		return nil, errPlatformPublicNotFound
	}
	result := &platformPublicVerified{manifest: parsed.Manifest, declared: declared, policy: policy, frontendSHA: parsed.Manifest.Frontend.SHA256}
	if policy.Document && declared.Document != "" {
		files, err := appplatform.ReadFrontendDirectory(parsed.Manifest, rows.archive, "public/", platformPublicMaxDocumentBytes)
		if err != nil {
			return nil, errPlatformPublicUnavailable
		}
		result.files = files
		for name, file := range files {
			result.filesBytes += int64(len(file.Content)) + int64(len(name))
		}
	}
	return result, nil
}
