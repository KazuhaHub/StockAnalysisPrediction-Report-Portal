package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// What an authenticated machine read may rely on (doc 183 section 4.4 step 5), read for EVERY request
// in one indexed statement with no archive and no lock: the installation is enabled and ready, there
// is no restore pause or unfinished operation, the release is the active one and its publisher and
// signing key are still trusted, and machine.read is granted with a valid policy. Revocation of the
// grant, a policy change and a release change are therefore honoured on the very next request.

var (
	// errPlatformMachineRefused means the capability is off or not declared: the token holder is
	// answered as for a token that does not work, since the installation no longer offers the API.
	errPlatformMachineRefused = errors.New("machine read not offered")
	// errPlatformMachineUnavailable means it is on but cannot be served right now: a restore pause,
	// an unfinished operation, a disabled installation, a trust check that failed.
	errPlatformMachineUnavailable = errors.New("machine read unavailable")
	// errPlatformMachineStore marks a database failure of the read; it proves nothing about the
	// identifier asked for.
	errPlatformMachineStore = errors.New("machine read store failure")
)

type platformMachineAuthority struct {
	installation   string
	generation     int64
	policyRevision int64
	release        string
	policyJSON     string
	policy         appplatform.MachinePolicy
	descriptor     platformMachineDescriptor
}

func (a platformMachineAuthority) route(id string) (appplatform.MachineRoute, bool) {
	for _, route := range a.descriptor.machine.Routes {
		if route.ID == id {
			return route, true
		}
	}
	return appplatform.MachineRoute{}, false
}

// readPlatformMachineAuthority is the one statement every authenticated request pays for, plus the
// parsed machine block of the active release from memory (it is read from the database only the first
// time a release is seen).
func (s *Server) readPlatformMachineAuthority(ctx context.Context, installation string) (platformMachineAuthority, error) {
	var zero platformMachineAuthority
	s.platformMachineReads.Add(1)
	if !platformValidUUID(installation) {
		return zero, errPlatformMachineRefused
	}
	e := migExec{s: s.st, ex: s.st.db}
	query := `SELECT i.desired_state,i.phase,i.controller_epoch,i.runtime_id,i.generation,i.policy_revision,
		a.release_sha256,a.generation,a.policy_revision,g.granted,g.policy_json,
		p.enabled,r.verification_key_id,k.revoked_ms,k.not_before_ms,k.not_after_ms,
		(SELECT v FROM meta WHERE k='app_platform_controller_epoch'),
		(SELECT COUNT(*) FROM meta WHERE k='app_platform_restore_paused' AND v='0'),
		(SELECT COUNT(*) FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.installation_id=? AND ` + platformUnresolvedOperationPredicate(e) + `),
		(SELECT COUNT(*) FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.installation_id=? AND f.state IN ('prepared','unknown') AND ` + platformUnsafeEffectPredicate(e) + `)
		FROM app_platform_installations i
		LEFT JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active'
		LEFT JOIN app_platform_publishers p ON p.id=i.publisher_id
		LEFT JOIN app_platform_releases r ON r.sha256=a.release_sha256
		LEFT JOIN app_platform_publisher_keys k ON k.publisher_id=r.publisher_id AND k.key_id=r.verification_key_id
		LEFT JOIN app_platform_capability_grants g ON g.installation_id=i.id AND g.capability='machine.read' AND g.policy_revision=i.policy_revision
		WHERE i.id=?`
	var desired, phase, storedEpoch string
	var runtimeID, release, policyJSON, epoch, keyID sql.NullString
	var generation, policy int64
	var ag, ap, granted, pubEnabled, keyRevoked, keyBefore, keyAfter sql.NullInt64
	var markers, operations, effects int
	if err := s.st.queryRowContext(ctx, query, installation, installation, installation).Scan(&desired, &phase, &storedEpoch, &runtimeID, &generation, &policy, &release, &ag, &ap, &granted, &policyJSON, &pubEnabled, &keyID, &keyRevoked, &keyBefore, &keyAfter, &epoch, &markers, &operations, &effects); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, errPlatformMachineRefused
		}
		return zero, fmt.Errorf("%w: %w: %v", errPlatformMachineUnavailable, errPlatformMachineStore, err)
	}
	// The grant decides first: an installation with the capability off is refused whatever else is
	// going on.
	if !granted.Valid || granted.Int64 != 1 || !policyJSON.Valid || !appplatform.ValidMachinePolicyShape(policyJSON.String) || !release.Valid {
		return zero, errPlatformMachineRefused
	}
	if desired != "enabled" || phase != "ready" || !runtimeID.Valid || !epoch.Valid || epoch.String != storedEpoch || markers != 1 || !ag.Valid || !ap.Valid || ag.Int64 != generation || ap.Int64 != policy || operations != 0 || effects != 0 {
		return zero, errPlatformMachineUnavailable
	}
	// Trust is read with the rest, so it is checked on every request: a disabled publisher, a revoked
	// key or a key outside its validity answers 503 at once.
	nowMS := time.Now().UnixMilli()
	if !pubEnabled.Valid || pubEnabled.Int64 != 1 || !keyID.Valid || keyRevoked.Valid || !keyBefore.Valid || keyBefore.Int64 > nowMS || (keyAfter.Valid && keyAfter.Int64 <= nowMS) {
		return zero, errPlatformMachineUnavailable
	}
	descriptor, err := s.platformMachineDescriptor(ctx, release.String)
	if err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseMachinePolicy(policyJSON.String, descriptor.machine)
	if err != nil {
		return zero, errPlatformMachineRefused
	}
	return platformMachineAuthority{installation: installation, generation: generation, policyRevision: policy, release: release.String, policyJSON: policyJSON.String, policy: parsed, descriptor: descriptor}, nil
}

// platformMachineDescriptor returns the parsed machine block of a stored release. Its identifier is
// the digest of the descriptor bytes, so the parsed block of an identifier never changes and is kept.
// Publisher trust and the release signature are verified by the access transaction of every backend
// call, and publisher and key state is read with every request above.
func (s *Server) platformMachineDescriptor(ctx context.Context, release string) (platformMachineDescriptor, error) {
	if d, ok := s.platformMachine.descriptor(release); ok {
		return d, nil
	}
	var raw []byte
	if err := s.st.queryRowContext(ctx, `SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return platformMachineDescriptor{}, errPlatformMachineRefused
		}
		return platformMachineDescriptor{}, fmt.Errorf("%w: %w: %v", errPlatformMachineUnavailable, errPlatformMachineStore, err)
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil || parsed.SHA256 != release || parsed.Manifest.Machine == nil || parsed.Manifest.Backend == nil {
		return platformMachineDescriptor{}, errPlatformMachineRefused
	}
	d := platformMachineDescriptor{machine: *parsed.Manifest.Machine, appID: parsed.Manifest.ID, version: parsed.Manifest.Version, backendDeclared: true}
	s.platformMachine.keepDescriptor(release, d)
	return d, nil
}
