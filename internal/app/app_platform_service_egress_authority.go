package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Background broker egress, Portal side (doc 174). A backend that no user is
// driving may reach the reviewed destinations only through an egress session the
// runner holds open for it. This file is the authority half: what Portal checks,
// from SQL, before it opens a session, before it signs one call and before it
// answers each authority challenge of that call. It decides nothing from the
// session or the request: every fact is read again, and a session is only ever
// as strong as the assertion, grants, release and runtime that are current now.

const (
	platformServiceEgressNetwork = "network.http"
	platformServiceEgressNotice  = "notifications.publish"
	// platformServiceEgressProxy is the reviewed forward proxy (doc 184). It rides the session like the
	// other resources and carries the reviewed policy in the signed command.
	platformServiceEgressProxy = appplatform.ProxyCapability
	// platformServiceEgressSecrets lets a call of the session name one reviewed
	// credential binding (doc 176). It is planned only beside network.http.
	platformServiceEgressSecrets = "secrets.use"
	// An admit is signed only while the session still has this long to run, so a
	// command (30 s, the foreground lifetime) never outlives its assertion.
	platformServiceEgressMinAdmitLeft = 31 * time.Second
	// platformServiceEgressCheckBudget is the budget of one authority challenge.
	platformServiceEgressCheckBudget = time.Second
	platformServiceEgressRequestMS   = int64(30000)
)

var (
	errPlatformServiceEgressUnavailable = errors.New("service egress authority unavailable")
	// errPlatformServiceEgressDenied is an admit the reviewed policy does not allow.
	errPlatformServiceEgressDenied = errors.New("service egress request denied")
	// errPlatformServiceEgressExpiring is an admit that arrives too close to the
	// end of its session.
	errPlatformServiceEgressExpiring = errors.New("service egress session is about to expire")
)

// platformServiceEgressPin is everything a session is bound to. A check or an
// admit that finds any of it different refuses, and the coordinator closes the
// session as drift. It holds no secret and compares with ==.
type platformServiceEgressPin struct {
	installation, appID, generation, policyRevision, release, epoch string
	activation, runtimeID, deploymentID, fingerprint                string
	controlRevision, fence                                          int64
	resources                                                       string
	review, policySHA                                               string
	concurrency                                                     int
	// proxy is the digest of the reviewed proxy policy, or "". A change of it is drift.
	proxy string
}

// platformServiceEgressAuthority is one current reading. It is private, never
// serialized, and Close clears its key material.
type platformServiceEgressAuthority struct {
	installation string
	facts        platformServiceFacts
	tuple        platformServiceActiveTuple
	resources    []string
	runtime      platformRuntimeRegistration
	authority    control.Authority
	controlSeed  []byte
	grant        platformNetworkGrant
	policy       appplatform.ParsedHTTPPolicyAny
	concurrency  int
	identity     appplatform.ServiceIdentityGrant
	// proxy is the reviewed proxy policy the session carries, when network.proxy is a resource.
	proxy   *appplatform.ParsedProxyPolicy
	history *platformServiceBackendHistory
	// binding is the service binding a credential call resolved to, and
	// credentialName its name. credentialHeader and credentialValue are set only
	// when the load was asked to open the value (an admit); the value is cleared
	// by Close.
	binding          platformSecretBindingUse
	credentialName   string
	credentialHeader string
	credentialValue  []byte
	// signedHeaders are the ACS3 headers Portal computed at an admit that asked for a signed call
	// (doc 177), in place of a credential header and value. They are wiped by Close.
	signedHeaders []appplatform.SignedHeader
}

func (a *platformServiceEgressAuthority) Close() {
	if a == nil {
		return
	}
	clear(a.credentialValue)
	for _, h := range a.signedHeaders {
		clear(h.Value)
	}
	clear(a.controlSeed)
	if a.history != nil {
		a.history.Close()
	}
	*a = platformServiceEgressAuthority{}
}

func (a platformServiceEgressAuthority) pin() platformServiceEgressPin {
	proxy := ""
	if a.proxy != nil {
		proxy = a.proxy.SHA256
	}
	return platformServiceEgressPin{proxy: proxy,
		installation: a.installation, appID: a.facts.appID,
		generation: a.facts.generation, policyRevision: a.facts.policyRevision,
		release: a.facts.release, epoch: a.facts.epoch,
		activation: a.tuple.activation, runtimeID: a.runtime.id,
		deploymentID: a.runtime.deploymentID, fingerprint: a.runtime.fingerprint,
		controlRevision: a.runtime.controlRevision, fence: a.tuple.fence,
		resources: strings.Join(a.resources, ","),
		review:    a.grant.review, policySHA: a.policy.SHA256,
		concurrency: a.concurrency,
	}
}

func (a platformServiceEgressAuthority) hasNetwork() bool {
	return slices.Contains(a.resources, platformServiceEgressNetwork)
}

// platformServiceEgressLoad says what to read. resources nil means "every egress
// resource that is granted now" (planning a session); a generation-1 managed
// install, whose network.http is ungranted, is then planned notification-only. identity nil skips the assertion
// (planning, before one is issued). history asks for the registration proof the
// session command carries; it is signed fresh on each read and is needed at open
// only.
type platformServiceEgressLoad struct {
	identity  []byte
	resources []string
	history   bool
	// credential names the binding the call uses, or is empty. It needs
	// secrets.use in the session. The binding must be reviewed for the service
	// and for exactly the current network policy, else the load is denied before
	// any value is opened; decrypt then opens it (an admit; a check does not).
	credential string
	decrypt    bool
	// admit, when set with a credential, is judged against the reviewed policy
	// before the binding is resolved or opened.
	admit *transport.EgressAdmit
	// destination is the method and URL of a call that is being checked rather
	// than admitted, so that a target binding is judged for it too. An admit
	// supplies its own. Neither means the binding is only checked to still be a
	// target of the policy.
	destination platformBindingDestination
	// beforeCommit is a test seam: it runs with the transaction still open.
	beforeCommit func()
}

// loadPlatformServiceEgressCurrent reads the complete current authority of one
// installation for background egress: the service facts (enabled, ready, no
// restore pause, current epoch, trusted release, background.run and every
// resource granted, no missing setup), the active tuple, the current runtime,
// no unresolved operation or effect, and for network.http the exact reviewed
// grant of a writable backend. It is the single reading the open, every admit
// and every challenge rely on.
func (s *Server) loadPlatformServiceEgressCurrent(ctx context.Context, installation string, o platformServiceEgressLoad) (platformServiceEgressAuthority, error) {
	var zero platformServiceEgressAuthority
	if s == nil || s.st == nil || !platformValidUUID(installation) {
		return zero, errPlatformServiceEgressUnavailable
	}
	discover := o.resources == nil
	resources := o.resources
	if !discover {
		var err error
		resources, err = normalizePlatformServiceResources(resources)
		if err != nil {
			return zero, errPlatformServiceEgressUnavailable
		}
	}
	for _, resource := range resources {
		if resource != platformServiceEgressNetwork && resource != platformServiceEgressNotice && resource != platformServiceEgressSecrets && resource != platformServiceEgressProxy {
			return zero, errPlatformServiceEgressUnavailable
		}
	}
	if slices.Contains(resources, platformServiceEgressSecrets) && !slices.Contains(resources, platformServiceEgressNetwork) {
		return zero, errPlatformServiceEgressUnavailable
	}
	if o.credential != "" && (!slices.Contains(resources, platformServiceEgressSecrets) || !platformSecretBindingNamePattern.MatchString(o.credential) || discover) {
		return zero, errPlatformServiceEgressDenied
	}
	var identity appplatform.ServiceIdentityGrant
	if len(o.identity) > 0 {
		if discover {
			return zero, errPlatformServiceEgressUnavailable
		}
		grant, err := s.verifyPlatformServiceIdentity(ctx, o.identity, resources[0])
		if err != nil || grant.InstallationID != installation || !slices.Equal(grant.Resources, resources) {
			return zero, errPlatformServiceEgressUnavailable
		}
		identity = grant
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, errPlatformServiceEgressUnavailable
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	// Read-only: the shared locks keep every writer out exactly as the
	// exclusive ones do, without making concurrent egress loads queue.
	if err := lockPlatformPublisherTrustShared(e); err != nil {
		return zero, errPlatformServiceEgressUnavailable
	}
	if discover {
		rows, err := e.query(`SELECT capability FROM app_platform_capability_grants
			WHERE installation_id=? AND granted=1
			AND capability IN ('network.http','network.proxy','notifications.publish','secrets.use')
			ORDER BY capability`, installation)
		if err != nil {
			return zero, errPlatformServiceEgressUnavailable
		}
		for rows.Next() {
			var capability string
			if rows.Scan(&capability) != nil {
				rows.Close()
				return zero, errPlatformServiceEgressUnavailable
			}
			resources = append(resources, capability)
		}
		err = rows.Err()
		rows.Close()
		// secrets.use is only ever planned beside network.http.
		if !slices.Contains(resources, platformServiceEgressNetwork) {
			resources = slices.DeleteFunc(resources, func(r string) bool { return r == platformServiceEgressSecrets })
		}
		if err != nil || len(resources) == 0 {
			return zero, errPlatformServiceEgressUnavailable
		}
	}
	facts, err := loadPlatformServiceFactsLocked(e, installation, resources, " FOR SHARE")
	if err != nil {
		return zero, errPlatformServiceEgressUnavailable
	}
	tuple, err := loadPlatformServiceActiveTuple(e, installation, facts)
	if err != nil {
		return zero, errPlatformServiceEgressUnavailable
	}
	dek, err := s.readPlatformDEK(e)
	if err != nil {
		return zero, errPlatformServiceEgressUnavailable
	}
	defer clear(dek)
	runtime, seed, err := s.platformDataRuntimeLocked(e, facts.epoch, dek, " FOR SHARE")
	failed := true
	defer func() {
		if failed {
			clear(seed)
		}
	}()
	if err != nil || runtime.id != tuple.runtimeID {
		return zero, errPlatformServiceEgressUnavailable
	}
	// An operation or an effect in flight is drift: the tuple may be about to
	// change under a session that was opened against the old one.
	operations, err := platformUnresolvedOperationCount(e, installation)
	if err != nil || operations != 0 {
		return zero, errPlatformServiceEgressUnavailable
	}
	effects, err := platformUnresolvedOperationEffectCount(e, installation)
	if err != nil || effects != 0 {
		return zero, errPlatformServiceEgressUnavailable
	}
	var aggregate int
	if err := e.queryRow(`SELECT
 (SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision<>?)+
 (SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND id<>? AND (state<>'retired' OR generation>=?))`,
		installation, tuple.policy, installation, tuple.activation, tuple.generation).Scan(&aggregate); err != nil || aggregate != 0 {
		return zero, errPlatformServiceEgressUnavailable
	}
	result := platformServiceEgressAuthority{
		installation: installation, facts: facts, tuple: tuple, resources: resources,
		runtime: runtime, authority: platformRuntimeAuthority(runtime), controlSeed: seed,
		concurrency: 1, identity: identity,
	}
	defer func() {
		if failed {
			clear(result.credentialValue)
		}
	}()
	if slices.Contains(resources, platformServiceEgressProxy) {
		// The proxy policy is the administrator's review, proved against the latest consumed proxy plan. In
		// discovery a grant no review backs is simply not a resource; a session that asked for it by name
		// fails, which the coordinator treats as drift.
		proxy, err := loadPlatformProxyGrant(e, installation, tuple.policy, facts.release, facts.epoch)
		switch {
		case err == nil:
			result.proxy = &proxy
		case discover:
			resources = slices.DeleteFunc(resources, func(r string) bool { return r == platformServiceEgressProxy })
			result.resources = resources
			if len(resources) == 0 {
				return zero, errPlatformServiceEgressUnavailable
			}
		default:
			return zero, errPlatformServiceEgressUnavailable
		}
	}
	if slices.Contains(resources, platformServiceEgressNetwork) {
		grant, err := loadPlatformNetworkGrantForIntent(e, installation, platformNetworkGrantTarget{
			release: facts.release, generation: tuple.generation, policy: tuple.policy, runtime: runtime,
		}, platformServiceEgressNetwork)
		var parsed appplatform.ParsedHTTPPolicyAny
		if err == nil {
			parsed, err = appplatform.ParseHTTPPolicyAny([]byte(grant.policy))
		}
		if err == nil && (parsed.Policy.Deadline <= 0 || parsed.Policy.Deadline > platformNetworkRequestLifetime ||
			parsed.Policy.Concurrency < 1 || parsed.Policy.Concurrency > 4) {
			err = errPlatformServiceEgressUnavailable
		}
		// A network grant that does not resolve fails the session, whatever the
		// kind of install. The generation-1 managed install of doc 174 F13 has no
		// network target and its network.http is ungranted, so discovery never
		// lists the resource for it and no network grant is read at all: that case
		// needs no fallback, and a fallback would hide real grant failures.
		if err != nil {
			return zero, errPlatformServiceEgressUnavailable
		}
		result.grant, result.policy, result.concurrency = grant, parsed, parsed.Policy.Concurrency
	}
	if o.credential != "" {
		if err := s.loadPlatformServiceEgressCredential(e, installation, o, &result); err != nil {
			return zero, err
		}
	}
	if o.history {
		now, err := platformDatabaseNow(e)
		if err != nil {
			return zero, errPlatformServiceEgressUnavailable
		}
		history, err := s.loadPlatformServiceBackendHistory(e, platformBackendHistoryBinding{
			installation: installation, activation: tuple.activation, release: tuple.release,
			namespace: tuple.namespace, epoch: facts.epoch, readiness: tuple.readiness,
			format: tuple.format, fence: tuple.fence, observed: tuple.observed, generation: tuple.generation,
		}, runtime, seed, dek, now)
		if err != nil {
			return zero, errPlatformServiceEgressUnavailable
		}
		if platformDigest(history.descriptor) != facts.release || history.runtime.id != runtime.id {
			history.Close()
			return zero, errPlatformServiceEgressUnavailable
		}
		result.history = &history
	}
	if o.beforeCommit != nil {
		o.beforeCommit()
	}
	if err := tx.Commit(); err != nil {
		result.Close()
		return zero, errPlatformServiceEgressUnavailable
	}
	failed = false
	return result, nil
}

// loadPlatformServiceEgressCredential resolves the binding of one credential call
// inside the authority transaction, with the same reading the foreground uses
// (loadPlatformSecretBindingFacts and the binding's consumed review), for the
// service principal. A binding that is unknown, revoked, awaiting review,
// reviewed for the viewer only, or reviewed for another network policy is a
// denial before anything is decrypted; a value that cannot be opened is
// unavailable.
func (s *Server) loadPlatformServiceEgressCredential(e migExec, installation string, o platformServiceEgressLoad, result *platformServiceEgressAuthority) error {
	if !slices.Contains(result.resources, platformServiceEgressSecrets) || !result.hasNetwork() {
		return errPlatformServiceEgressDenied
	}
	// The destination comes first (doc 176 section 3.3): a call the reviewed
	// policy does not allow is denied without the binding being looked at, and
	// so never reaches the custody.
	if o.admit != nil {
		selected, err := result.policy.Policy.Select(o.admit.Method, o.admit.URL)
		if err != nil || int64(o.admit.BodyBytes) > selected.RequestBytes || selected.Scheme != "https" {
			return errPlatformServiceEgressDenied
		}
	}
	facts, err := loadPlatformSecretBindingFacts(e, installation, false)
	if err != nil {
		return errPlatformServiceEgressUnavailable
	}
	if facts.networkPolicySHA != result.policy.SHA256 {
		return errPlatformServiceEgressDenied
	}
	dest := o.destination
	if o.admit != nil {
		dest = platformBindingDestination{method: o.admit.Method, url: o.admit.URL}
	}
	// A call that opens the value names where it goes; there is no default.
	if o.decrypt && dest.url == "" {
		return errPlatformServiceEgressDenied
	}
	if o.decrypt && o.admit != nil && o.admit.SigningScheme != "" {
		// A signed call (doc 177): the binding must be an ACS3 one whose reviewed policy lists this
		// action and version, all judged before the credential is opened; then Portal signs the exact
		// request the admit described and keeps only the headers.
		use, headers, err := s.signPlatformACS3Facts(e, installation, platformPrincipalService, platformACS3Call{
			name: o.credential, action: o.admit.SigningAction, version: o.admit.SigningVersion,
			method: o.admit.Method, target: o.admit.URL, contentType: o.admit.ContentType, bodySHA256: o.admit.BodySHA256,
		}, facts)
		if err != nil {
			return platformServiceEgressCredentialError(err)
		}
		result.binding, result.credentialName, result.signedHeaders = use, o.credential, headers
		return nil
	}
	use, sealed, err := loadPlatformSecretBindingUseFor(e, installation, o.credential, facts, platformPrincipalService, dest)
	if err != nil {
		return platformServiceEgressCredentialError(err)
	}
	result.binding, result.credentialName = use, o.credential
	if !o.decrypt {
		return nil
	}
	if use.header == "" {
		// A signing binding named without a signing request: nothing to place, and nothing opened.
		return errPlatformServiceEgressDenied
	}
	plain, err := s.openPlatformCredentialValue(use.credentialID, use.credentialValueRevision, sealed)
	if err != nil {
		return errPlatformServiceEgressUnavailable
	}
	header, value, ok := platformSecretHeader(use.operation, plain)
	clear(plain)
	if !ok || header != use.header {
		clear(value)
		return errPlatformServiceEgressUnavailable
	}
	result.credentialHeader, result.credentialValue = header, value
	return nil
}

// platformServiceEgressCredentialError says what a failed binding read means for
// a call: an unknown, revoked, unreviewed, wrong-principal or wrong-target binding
// is a denial; a failure of the store is unavailable and writes no denied row.
func platformServiceEgressCredentialError(err error) error {
	if errors.Is(err, errPlatformSecretBindingStore) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errPlatformServiceEgressUnavailable
	}
	return errPlatformServiceEgressDenied
}

// signServiceEgressSession signs the command that opens a session. The assertion
// is the one issued for exactly these resources, whose lease id is the session
// id; the session ends no later than it does.
func (a platformServiceEgressAuthority) signServiceEgressSession(sessionID string, identity []byte, nowMS int64) (raw, signature []byte, expiresMS int64, err error) {
	if a.history == nil || len(a.controlSeed) != ed25519.SeedSize || !platformValidUUID(sessionID) || a.identity.LeaseID != sessionID || len(identity) == 0 {
		return nil, nil, 0, errPlatformServiceEgressUnavailable
	}
	assertion, err := strconv.ParseInt(a.identity.ExpiresMS, 10, 64)
	if err != nil {
		return nil, nil, 0, errPlatformServiceEgressUnavailable
	}
	expiresMS = min(assertion, nowMS+control.ServiceEgressMaxLifetimeMS)
	if expiresMS-nowMS < control.ServiceEgressMinLifetimeMS {
		return nil, nil, 0, errPlatformServiceEgressExpiring
	}
	r := a.runtime
	format := control.ServiceEgressSessionFormat
	var proxy *control.ServiceEgressProxy
	if slices.Contains(a.resources, platformServiceEgressProxy) {
		if a.proxy == nil {
			return nil, nil, 0, errPlatformServiceEgressUnavailable
		}
		block := control.NewServiceEgressProxy(a.proxy.Raw)
		format, proxy = control.ServiceEgressSessionProxyFormat, &block
	}
	command := control.ServiceEgressSessionCommand{
		Format: format, Proxy: proxy, DeploymentID: r.deploymentID,
		ControllerEpoch: r.controllerEpoch, ControlRevision: strconv.FormatInt(r.controlRevision, 10),
		SessionID: sessionID, InstallationID: a.installation,
		Generation: a.facts.generation, PolicyRevision: a.facts.policyRevision,
		ReleaseSHA256: a.facts.release, ServiceIdentitySHA256: platformDigest(identity),
		Resources: slices.Clone(a.resources), Concurrency: strconv.Itoa(a.concurrency),
		RatePerMinute: control.ServiceEgressRatePerMinute, Burst: control.ServiceEgressBurst,
		RegistrationRaw:       bytes.Clone(a.history.raw),
		RegistrationSignature: bytes.Clone(a.history.signature),
		IssuedMS:              strconv.FormatInt(nowMS, 10), ExpiresMS: strconv.FormatInt(expiresMS, 10),
	}
	key := ed25519.NewKeyFromSeed(a.controlSeed)
	defer clear(key)
	raw, signature, err = control.SignServiceEgressSession(command, key, a.history.authority, nowMS)
	if err != nil {
		return nil, nil, 0, errPlatformServiceEgressUnavailable
	}
	return raw, signature, expiresMS, nil
}

// platformServiceEgressActorScope is the actor scope of every call of a session:
// the service, never a viewer, so a background call can never be mistaken for
// (or replayed as) a foreground one.
func platformServiceEgressActorScope(sessionID string) string {
	return platformDigest([]byte("portal-network-service/v1:" + sessionID))
}

// signServiceEgressCommand signs one admitted call. The reviewed policy decides
// (target, method, size) exactly as the runner will; the command carries a fresh
// Portal-minted request id, the credential this authority opened for the admit's
// binding (none when the admit names none), and expires with the session at the
// latest. sessionExpiresMS is the session's own end.
func (a platformServiceEgressAuthority) signServiceEgressCommand(sessionID string, admit transport.EgressAdmit, nowMS, sessionExpiresMS int64) (control.NetworkHTTPRequestCommand, []byte, []byte, error) {
	var zero control.NetworkHTTPRequestCommand
	if !a.hasNetwork() || len(a.controlSeed) != ed25519.SeedSize || !platformValidUUID(sessionID) || a.grant.policy == "" {
		return zero, nil, nil, errPlatformServiceEgressUnavailable
	}
	if limit := min(sessionExpiresMS, platformServiceEgressIdentityExpiry(a.identity, sessionExpiresMS)); limit-nowMS < platformServiceEgressMinAdmitLeft.Milliseconds() {
		return zero, nil, nil, errPlatformServiceEgressExpiring
	}
	selected, err := a.policy.Policy.Select(admit.Method, admit.URL)
	if err != nil || int64(admit.BodyBytes) > selected.RequestBytes || (admit.Credential != "" && selected.Scheme != "https") {
		return zero, nil, nil, errPlatformServiceEgressDenied
	}
	privateSHA := ""
	if a.policy.Policy.HasPrivate() {
		if !platformValidSHA(a.grant.privateNetworksSHA) {
			return zero, nil, nil, errPlatformServiceEgressUnavailable
		}
		privateSHA = a.grant.privateNetworksSHA
	}
	// A call carries a credential only when its admit named exactly the binding
	// this authority resolved and opened; a signed call carries Portal's headers instead.
	signed := admit.SigningScheme != ""
	if (admit.Credential != "") != (a.credentialName != "") || admit.Credential != a.credentialName ||
		(admit.Credential != "" && !signed && (len(a.credentialValue) == 0 || a.credentialHeader == "")) ||
		(signed && (len(a.signedHeaders) == 0 || len(a.credentialValue) != 0 || a.credentialHeader != "")) ||
		(!signed && len(a.signedHeaders) != 0) {
		return zero, nil, nil, errPlatformServiceEgressUnavailable
	}
	if signed {
		if action, version := appplatform.ACS3ActionOf(a.signedHeaders); action != admit.SigningAction || version != admit.SigningVersion || admit.SigningScheme != appplatform.ACS3Scheme {
			return zero, nil, nil, errPlatformServiceEgressUnavailable
		}
	}
	id, err := platformNewUUID()
	if err != nil {
		return zero, nil, nil, errPlatformServiceEgressUnavailable
	}
	r := a.runtime
	c := control.NetworkHTTPRequestCommand{
		Format: control.NetworkHTTPRequestFormat, DeploymentID: r.deploymentID,
		ControllerEpoch: r.controllerEpoch, ControlRevision: strconv.FormatInt(r.controlRevision, 10),
		RequestID: id, InstallationID: a.installation, ReviewPlanID: a.grant.review,
		Generation: a.facts.generation, PolicyRevision: a.facts.policyRevision,
		ReleaseSHA256: a.facts.release, ActorScope: platformServiceEgressActorScope(sessionID),
		PolicyRaw: []byte(a.grant.policy), PolicySHA256: a.policy.SHA256, PrivateNetworksSHA256: privateSHA,
		Method: admit.Method, URL: admit.URL, Accept: admit.Accept, ContentType: admit.ContentType,
		BodyBytes: strconv.Itoa(admit.BodyBytes), BodySHA256: admit.BodySHA256,
		CredentialName: a.credentialName, CredentialHeader: a.credentialHeader, CredentialValue: bytes.Clone(a.credentialValue),
		IssuedMS: strconv.FormatInt(nowMS, 10), ExpiresMS: strconv.FormatInt(nowMS+platformServiceEgressRequestMS, 10),
	}
	if signed {
		c.SigningScheme = admit.SigningScheme
		c.SetSignedHeaders(cloneSignedHeaders(a.signedHeaders))
	}
	key := ed25519.NewKeyFromSeed(a.controlSeed)
	defer clear(key)
	raw, signature, err := control.SignNetworkHTTPRequest(c, key, a.authority, nowMS)
	if err != nil {
		clear(c.CredentialValue)
		c.ClearSignedHeaders()
		return zero, nil, nil, errPlatformServiceEgressDenied
	}
	return c, raw, signature, nil
}

func platformServiceEgressIdentityExpiry(g appplatform.ServiceIdentityGrant, fallback int64) int64 {
	if expires, err := strconv.ParseInt(g.ExpiresMS, 10, 64); err == nil {
		return expires
	}
	return fallback
}

// platformServiceEgressSessionState is what an open session holds for the calls
// it serves: the pin, its assertion and when it ends.
type platformServiceEgressSessionState struct {
	sessionID string
	identity  []byte
	resources []string
	pin       platformServiceEgressPin
	expiresMS int64
}

// loadServiceEgressForSession reads current authority for an open session and
// refuses unless it is still the one the session was opened against.
func (s *Server) loadServiceEgressForSession(ctx context.Context, state platformServiceEgressSessionState) (platformServiceEgressAuthority, error) {
	return s.loadServiceEgressForSessionCredential(ctx, state, "", false, platformBindingDestination{})
}

// loadServiceEgressForSessionCredential is that reading for a call that names a
// credential binding (credential non-empty): see platformServiceEgressLoad.
func (s *Server) loadServiceEgressForSessionCredential(ctx context.Context, state platformServiceEgressSessionState, credential string, decrypt bool, dest platformBindingDestination) (platformServiceEgressAuthority, error) {
	return s.loadServiceEgressForSessionLoad(ctx, state, platformServiceEgressLoad{credential: credential, decrypt: decrypt, destination: dest})
}

// loadServiceEgressForSessionAdmit is the admit's reading: a credential call has
// its target judged first and its value opened last.
func (s *Server) loadServiceEgressForSessionAdmit(ctx context.Context, state platformServiceEgressSessionState, admit transport.EgressAdmit) (platformServiceEgressAuthority, error) {
	o := platformServiceEgressLoad{credential: admit.Credential, decrypt: admit.Credential != ""}
	if admit.Credential != "" {
		o.admit = &admit
	}
	return s.loadServiceEgressForSessionLoad(ctx, state, o)
}

func (s *Server) loadServiceEgressForSessionLoad(ctx context.Context, state platformServiceEgressSessionState, o platformServiceEgressLoad) (platformServiceEgressAuthority, error) {
	o.identity, o.resources = state.identity, state.resources
	a, err := s.loadPlatformServiceEgressCurrent(ctx, state.pin.installation, o)
	if err != nil {
		return a, err
	}
	if a.identity.LeaseID != state.sessionID || a.pin() != state.pin {
		a.Close()
		return platformServiceEgressAuthority{}, errPlatformServiceEgressUnavailable
	}
	return a, nil
}

// approveServiceEgressCheck answers one authority challenge of one call. It runs
// the whole authority load again (nothing is carried over from the admit) inside
// the one-second budget of a check, and signs the proof only when the call's
// command is still exactly what this authority would sign now. A refusal is the
// absence of a proof: the caller withholds it. binding is the binding the admit
// resolved for a call that carries a credential (the zero value for one that does
// not); the check resolves the binding again, without opening the value, and
// refuses unless it is exactly that one: a revoked binding, a rotated or revoked
// credential, a withdrawn service principal or a changed policy all differ.
func (s *Server) approveServiceEgressCheck(ctx context.Context, state platformServiceEgressSessionState, call platformServiceEgressCall, challenge string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, platformServiceEgressCheckBudget)
	defer cancel()
	command, binding := call.command, call.binding
	if !call.consistent || (command.CredentialName != "") != (binding.id != "") || call.signed != (binding.operation == "acs3_hmac_sha256") {
		return nil, nil, errPlatformServiceEgressDenied
	}
	a, err := s.loadServiceEgressForSessionLoad(ctx, state, platformServiceEgressLoad{
		credential: command.CredentialName, destination: platformBindingDestination{method: command.Method, url: command.URL},
	})
	if err != nil {
		return nil, nil, err
	}
	defer a.Close()
	now := time.Now().UnixMilli()
	// The call no longer holds the credential value. The fields are validated as
	// signed, with a stand-in of the right shape for the value, which the
	// metadata validation only requires to be present.
	checked := command
	if call.credentialHeader != "" {
		checked.CredentialValue = []byte("Bearer x")
		if call.credentialHeader != "Authorization" {
			checked.CredentialValue = []byte("x")
		}
	}
	if !a.hasNetwork() ||
		command.InstallationID != a.installation ||
		command.ActorScope != platformServiceEgressActorScope(state.sessionID) ||
		command.ReviewPlanID != a.grant.review || command.PolicySHA256 != a.policy.SHA256 ||
		a.binding != binding || command.CredentialHeader != binding.header || (binding.id == "") != (call.credentialHeader == "" && !call.signed) ||
		(call.signed && !platformACS3CommandAllowed(command, a.binding)) ||
		command.Generation != a.facts.generation || command.PolicyRevision != a.facts.policyRevision ||
		command.ReleaseSHA256 != a.facts.release ||
		control.ValidateNetworkHTTPRequestMetadata(checked, a.authority, now) != nil {
		return nil, nil, errPlatformServiceEgressDenied
	}
	if expires, err := strconv.ParseInt(command.ExpiresMS, 10, 64); err != nil || expires > state.expiresMS {
		return nil, nil, errPlatformServiceEgressDenied
	}
	key := ed25519.NewKeyFromSeed(a.controlSeed)
	defer clear(key)
	expires, _ := strconv.ParseInt(command.ExpiresMS, 10, 64)
	proof, signature, err := control.SignNetworkHTTPCheckDigest(call.sha, challenge, expires, key, now)
	if err != nil || ctx.Err() != nil {
		return nil, nil, errPlatformServiceEgressDenied
	}
	return proof, signature, nil
}

// platformServiceEgressCall is what a call keeps of its signed command once the
// command has been sent: the fields the checks compare and the digest the proofs
// bind, never the credential value and never the signed bytes (which carry it).
type platformServiceEgressCall struct {
	command control.NetworkHTTPRequestCommand // CredentialValue is always nil
	sha     string
	binding platformSecretBindingUse
	// credentialHeader is the header the value was placed in, or "".
	credentialHeader string
	// signed is whether the command carries Portal-computed ACS3 headers (doc 177). The command
	// then keeps stand-ins of the right shape for the Authorization value and the security token.
	signed bool
	// consistent is whether the command's fields are exactly the signed bytes.
	consistent bool
}

// newPlatformServiceEgressCall keeps the fields and the digest of a signed
// command. It copies; the caller wipes its own command and bytes.
func newPlatformServiceEgressCall(command control.NetworkHTTPRequestCommand, raw []byte, binding platformSecretBindingUse) platformServiceEgressCall {
	kept := command
	kept.PolicyRaw = bytes.Clone(command.PolicyRaw)
	kept.CredentialValue = nil
	signed := command.SigningScheme != ""
	kept.SignedHeaderNames, kept.SignedHeaderValues = nil, nil
	if headers, ok := command.SignedHeaderList(); signed && ok {
		// The checks need the shape of the headers, never the signature or the token.
		kept.SetSignedHeaders(appplatform.ACS3StandInHeaders(headers))
	}
	again, err := marshalPlatformServiceEgressCommand(command)
	consistent := err == nil && bytes.Equal(again, raw)
	clear(again)
	return platformServiceEgressCall{command: kept, sha: platformDigest(raw), binding: binding, credentialHeader: command.CredentialHeader, signed: signed, consistent: consistent}
}

// platformACS3CommandAllowed is whether the action and version a signed command carries are still
// listed by the binding's reviewed policy.
func platformACS3CommandAllowed(c control.NetworkHTTPRequestCommand, binding platformSecretBindingUse) bool {
	headers, ok := c.SignedHeaderList()
	action, version := appplatform.ACS3ActionOf(headers)
	return ok && binding.operation == "acs3_hmac_sha256" && slices.Contains(strings.Split(binding.acs3, ","), action+"@"+version)
}

func cloneSignedHeaders(headers []appplatform.SignedHeader) []appplatform.SignedHeader {
	out := make([]appplatform.SignedHeader, len(headers))
	for i, h := range headers {
		out[i] = appplatform.SignedHeader{Name: h.Name, Value: bytes.Clone(h.Value)}
	}
	return out
}

func marshalPlatformServiceEgressCommand(c control.NetworkHTTPRequestCommand) ([]byte, error) {
	return json.Marshal(c)
}

// platformServiceActiveTuple is the installation's current execution tuple as a
// service reads it: the active activation and the installation row, both of
// which must agree with the service facts read in the same transaction
// (generation, policy revision, release, epoch). It is the read
// preparePlatformServiceTask performs before it asks for the history.
type platformServiceActiveTuple struct {
	activation, release, namespace, readiness string
	generation, policy, format, observed      int64
	fence                                     int64
	runtimeID                                 string
}

func loadPlatformServiceActiveTuple(e migExec, installation string, facts platformServiceFacts) (platformServiceActiveTuple, error) {
	var t platformServiceActiveTuple
	if err := e.queryRow(`SELECT id,release_sha256,data_namespace_id,
		generation,policy_revision,data_format,observed_ms,readiness_evidence
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`, installation).
		Scan(&t.activation, &t.release, &t.namespace, &t.generation, &t.policy,
			&t.format, &t.observed, &t.readiness); err != nil ||
		t.release != facts.release ||
		platformCounterString(t.generation) != facts.generation ||
		platformCounterString(t.policy) != facts.policyRevision || t.format < 1 {
		return platformServiceActiveTuple{}, errAppPlatformServiceUnavailable
	}
	var runtimeID sql.NullString
	if err := e.queryRow(`SELECT runtime_id,control_fence
		FROM app_platform_installations
		WHERE id=? AND desired_state='enabled' AND phase='ready'
		AND generation=? AND policy_revision=? AND controller_epoch=?`,
		installation, t.generation, t.policy, facts.epoch).
		Scan(&runtimeID, &t.fence); err != nil || !runtimeID.Valid {
		return platformServiceActiveTuple{}, errAppPlatformServiceUnavailable
	}
	t.runtimeID = runtimeID.String
	return t, nil
}
