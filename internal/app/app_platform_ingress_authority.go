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

// What an authenticated inbound request may rely on (doc 180 section 3.3 step 3): one indexed read
// with no session, no archive and no write. Every request pays it, so revoking the binding,
// disabling the capability or the app, a restore pause, a release change or a rotation is honoured on
// the very next request.

var (
	// errPlatformIngressNotFound is the uniform refusal: unknown installation, not enabled or not
	// ready, capability off or route undeclared, no active endpoint binding, a binding or credential
	// that no longer matches its review. They are indistinguishable to the caller.
	errPlatformIngressNotFound = errors.New("ingress endpoint not found")
	// errPlatformIngressUnavailable means the endpoint is real but cannot be served right now: a
	// restore pause, an unfinished operation, a trust check or the store.
	errPlatformIngressUnavailable = errors.New("ingress unavailable")
	// errPlatformIngressStore marks a database failure of the read. It proves nothing about the
	// identifier asked for, so nothing is counted against it.
	errPlatformIngressStore = errors.New("ingress store failure")
)

type platformIngressLight struct {
	installation, route, endpoint string
	generation, policyRevision    int64
	release                       string
	// routeKnown is true once the installation has the capability granted and the release declares
	// the route: from then on the route's own bounds apply to the request, found endpoint or not.
	routeKnown bool
	decl       appplatform.IngressRoute

	bindingID                      string
	bindingRevision, valueRevision int64
	credentialID, sealed           string
}

// grantKey is the identity of what this read relied on. The relay reads the same facts inside its
// own transaction and must find exactly this key, so a revocation or a rotation that lands between
// the two refuses the forward.
func (l platformIngressLight) grantKey() string {
	return "ingress|" + l.route + "|" + l.bindingID + "|" + strconv.FormatInt(l.bindingRevision, 10) + "|" + l.credentialID + "|" + strconv.FormatInt(l.valueRevision, 10)
}

// platformIngressDeclared returns the routes a release declares, parsing its descriptor once.
func (s *Server) platformIngressDeclared(ctx context.Context, release string) (platformIngressDeclaration, error) {
	if d, ok := s.platformIngress.declaration(release); ok {
		return d, nil
	}
	var descriptor []byte
	if err := s.st.queryRowContext(ctx, `SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&descriptor); err != nil {
		return platformIngressDeclaration{}, fmt.Errorf("%w: %w: %v", errPlatformIngressUnavailable, errPlatformIngressStore, err)
	}
	var d platformIngressDeclaration
	if parsed, err := appplatform.ParseDescriptor(descriptor); err == nil && parsed.SHA256 == release {
		for _, name := range parsed.Manifest.Capabilities.Optional {
			d.declares = d.declares || name == "ingress.webhook"
		}
		if d.declares && parsed.Manifest.Ingress != nil && parsed.Manifest.Backend != nil {
			d.routes = parsed.Manifest.Ingress.Routes
		} else {
			d.declares = false
		}
	}
	s.platformIngress.storeDeclaration(release, d)
	return d, nil
}

// readPlatformIngressLight is the one statement every inbound request pays for. It returns what it
// has learned even when it refuses (routeKnown), so the caller can apply the route's bounds first.
func (s *Server) readPlatformIngressLight(ctx context.Context, installation, route, endpoint string) (platformIngressLight, error) {
	light := platformIngressLight{installation: installation, route: route, endpoint: endpoint}
	s.platformIngressReads.Add(1)
	if !platformValidUUID(installation) || !appplatform.ValidIngressRouteID(route) || !appplatform.ValidIngressEndpoint(endpoint) {
		return light, errPlatformIngressNotFound
	}
	e := migExec{s: s.st, ex: s.st.db}
	query := `SELECT i.app_id,i.publisher_id,i.revision,i.desired_state,i.phase,i.controller_epoch,i.runtime_id,i.generation,i.policy_revision,
		a.release_sha256,a.generation,a.policy_revision,g.granted,g.policy_json,
		b.id,b.credential_id,b.credential_value_revision,b.operation,b.policy_json,b.policy_revision,b.revision,
		pl.plan_json,pl.plan_sha256,pl.consumed_ms,
		c.label,c.state,c.value_revision,c.revision,c.sealed_value,
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
		LEFT JOIN app_platform_capability_grants g ON g.installation_id=i.id AND g.capability='ingress.webhook' AND g.policy_revision=i.policy_revision
		LEFT JOIN app_platform_secret_bindings b ON b.installation_id=i.id AND b.name=? AND b.state='active'
		LEFT JOIN app_platform_review_plans pl ON pl.id=b.review_plan_id
		LEFT JOIN app_platform_credentials c ON c.id=b.credential_id
		WHERE i.id=?`
	var appID, publisher, desired, phase, storedEpoch string
	var runtimeID, release, grantPolicy, bindingID, credentialID, operation, bindingPolicy, planJSON, planSHA sql.NullString
	var label, credentialState, sealed, epoch, keyID sql.NullString
	var installationRevision, generation, policy int64
	var ag, ap, granted, bindingValueRevision, bindingPolicyRevision, bindingRevision, consumed, credentialValueRevision, credentialRevision sql.NullInt64
	var pubEnabled, pubRevision, keyRevision, keyRevoked, keyBefore, keyAfter sql.NullInt64
	var markers, operations, effects int
	if err := s.st.queryRowContext(ctx, query, installation, installation, endpoint, installation).Scan(&appID, &publisher, &installationRevision, &desired, &phase, &storedEpoch, &runtimeID, &generation, &policy,
		&release, &ag, &ap, &granted, &grantPolicy,
		&bindingID, &credentialID, &bindingValueRevision, &operation, &bindingPolicy, &bindingPolicyRevision, &bindingRevision,
		&planJSON, &planSHA, &consumed,
		&label, &credentialState, &credentialValueRevision, &credentialRevision, &sealed,
		&pubEnabled, &pubRevision, &keyID, &keyRevision, &keyRevoked, &keyBefore, &keyAfter,
		&epoch, &markers, &operations, &effects); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return light, errPlatformIngressNotFound
		}
		return light, fmt.Errorf("%w: %w: %v", errPlatformIngressUnavailable, errPlatformIngressStore, err)
	}
	// The grant decides first: an installation with the capability off answers as an absent one
	// whatever else is going on.
	if desired != "enabled" || phase != "ready" || !runtimeID.Valid || !release.Valid || !granted.Valid || granted.Int64 != 1 || !grantPolicy.Valid || grantPolicy.String != "{}" {
		return light, errPlatformIngressNotFound
	}
	declared, err := s.platformIngressDeclared(ctx, release.String)
	if err != nil {
		return light, err
	}
	if !declared.declares {
		return light, errPlatformIngressNotFound
	}
	for _, r := range declared.routes {
		if r.ID == route {
			light.decl, light.routeKnown = r, true
		}
	}
	if !light.routeKnown {
		return light, errPlatformIngressNotFound
	}
	light.generation, light.policyRevision, light.release = generation, policy, release.String
	// The endpoint binding, judged as every use of a binding is: the policy, the consumed review and
	// the credential must all still say what the review said.
	if !bindingID.Valid || !platformValidUUID(bindingID.String) || !operation.Valid || operation.String != "ingress_hmac_sha256" || !bindingPolicy.Valid || !credentialID.Valid || !platformValidUUID(credentialID.String) ||
		!bindingValueRevision.Valid || !bindingPolicyRevision.Valid || !bindingRevision.Valid || bindingPolicyRevision.Int64 != policy || bindingValueRevision.Int64 < 1 || bindingRevision.Int64 < 1 {
		return light, errPlatformIngressNotFound
	}
	parsedPolicy, err := parsePlatformSecretBindingPolicy([]byte(bindingPolicy.String))
	if err != nil || parsedPolicy.operation != "ingress_hmac_sha256" || parsedPolicy.ingressRoute != route {
		return light, errPlatformIngressNotFound
	}
	if !planJSON.Valid || !planSHA.Valid || !consumed.Valid || platformDigest([]byte(planJSON.String)) != planSHA.String {
		return light, errPlatformIngressNotFound
	}
	plan, err := parsePlatformSecretBindingReview(planJSON.String)
	if err != nil || plan.Operation != "ingress_hmac_sha256" || plan.IngressRoute != route || plan.InstallationID != installation || plan.AppID != appID || plan.PublisherID != publisher || plan.Generation != generation ||
		plan.InstallationRevision != installationRevision || plan.PolicyRevision != policy || !pubRevision.Valid || plan.TrustRevision != pubRevision.Int64 || plan.BindingID != bindingID.String || bindingRevision.Int64 != plan.BindingRevision+1 ||
		plan.Name != endpoint || plan.CredentialID != credentialID.String || plan.CredentialValueRevision != bindingValueRevision.Int64 || plan.ReleaseSHA256 != release.String || !epoch.Valid || plan.ControllerEpoch != epoch.String {
		return light, errPlatformIngressNotFound
	}
	if !label.Valid || label.String != plan.CredentialLabel || !credentialState.Valid || credentialState.String != "active" || !credentialValueRevision.Valid || credentialValueRevision.Int64 != bindingValueRevision.Int64 ||
		!credentialRevision.Valid || credentialRevision.Int64 != plan.CredentialRevision || !sealed.Valid || sealed.String == "" {
		return light, errPlatformIngressNotFound
	}
	light.bindingID, light.bindingRevision, light.valueRevision, light.credentialID, light.sealed = bindingID.String, bindingRevision.Int64, bindingValueRevision.Int64, credentialID.String, sealed.String
	if epoch.String != storedEpoch || markers != 1 || !ag.Valid || !ap.Valid || ag.Int64 != generation || ap.Int64 != policy || operations != 0 || effects != 0 {
		return light, errPlatformIngressUnavailable
	}
	// Trust is read with the rest, so it is checked on every request: a disabled publisher, a revoked
	// key or a key outside its validity answers 503 at once.
	nowMS := time.Now().UnixMilli()
	if !pubEnabled.Valid || pubEnabled.Int64 != 1 || !keyID.Valid || !keyRevision.Valid || keyRevoked.Valid || !keyBefore.Valid || keyBefore.Int64 > nowMS || (keyAfter.Valid && keyAfter.Int64 <= nowMS) {
		return light, errPlatformIngressUnavailable
	}
	return light, nil
}

// platformReadAnonymousGrant is the in-transaction read of what an anonymous backend request relies
// on: the public.projection policy of a public read (doc 179), or the key of the ingress grant and
// endpoint binding of an inbound request (doc 180). Any other state is unavailable.
func platformReadAnonymousGrant(e migExec, session platformActorSession, installation string, policyRevision int64, m appplatform.Manifest, lock string) (string, error) {
	if session.anonymousIngress != "" {
		return platformReadIngressGrant(e, installation, policyRevision, m, session.anonymousIngress, lock)
	}
	return platformReadPublicGrant(e, installation, policyRevision, m, lock)
}

func platformReadIngressGrant(e migExec, installation string, policyRevision int64, m appplatform.Manifest, endpoint, lock string) (string, error) {
	var granted int
	var grantPolicy string
	if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='ingress.webhook' AND policy_revision=?`+lock, installation, policyRevision).Scan(&granted, &grantPolicy); err != nil || granted != 1 || grantPolicy != "{}" {
		return "", errAppPlatformLaunchUnavailable
	}
	var id, credential, operation, policyRaw, state string
	var valueRevision, bindingPolicy, revision int64
	if err := e.queryRow(`SELECT id,credential_id,credential_value_revision,operation,policy_json,policy_revision,revision,state FROM app_platform_secret_bindings WHERE installation_id=? AND name=?`+lock, installation, endpoint).
		Scan(&id, &credential, &valueRevision, &operation, &policyRaw, &bindingPolicy, &revision, &state); err != nil || state != "active" || operation != "ingress_hmac_sha256" || bindingPolicy != policyRevision {
		return "", errAppPlatformLaunchUnavailable
	}
	parsed, err := parsePlatformSecretBindingPolicy([]byte(policyRaw))
	if err != nil || parsed.operation != "ingress_hmac_sha256" {
		return "", errAppPlatformLaunchUnavailable
	}
	if _, ok := platformIngressDeclaredRoute(m, parsed.ingressRoute); !ok {
		return "", errAppPlatformLaunchUnavailable
	}
	var credentialState string
	var credentialValue int64
	if err := e.queryRow(`SELECT state,value_revision FROM app_platform_credentials WHERE id=?`+lock, credential).Scan(&credentialState, &credentialValue); err != nil || credentialState != "active" || credentialValue != valueRevision {
		return "", errAppPlatformLaunchUnavailable
	}
	return "ingress|" + parsed.ingressRoute + "|" + id + "|" + strconv.FormatInt(revision, 10) + "|" + credential + "|" + strconv.FormatInt(valueRevision, 10), nil
}
