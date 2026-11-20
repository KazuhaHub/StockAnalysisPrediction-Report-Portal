package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The administrator routes of the network.postgres grant review (doc 178, amendment C2). Like the
// capability policy review they sit under the admin management pattern, so a non-administrator, a
// cross-origin request or a body that is not closed JSON is refused before any plan is written.

func platformPostgresTargetFromJSON(raw json.RawMessage) (appplatform.PostgresTarget, error) {
	var t appplatform.PostgresTarget
	fields, err := platformManagementObject(raw, "id", "host", "port", "database", "network", "caPem")
	if err != nil {
		return t, err
	}
	for _, key := range []string{"id", "host", "port", "database", "network"} {
		if _, ok := fields[key]; !ok {
			return t, errPlatformManagementInput
		}
	}
	if t.ID, err = platformManagementString(fields, "id"); err != nil {
		return t, err
	}
	if t.Host, err = platformManagementString(fields, "host"); err != nil {
		return t, err
	}
	if t.Database, err = platformManagementString(fields, "database"); err != nil {
		return t, err
	}
	if t.Network, err = platformManagementString(fields, "network"); err != nil {
		return t, err
	}
	port, ok := platformManagementInt(fields["port"])
	if !ok || port < 1 || port > 65535 {
		return t, errPlatformManagementInput
	}
	t.Port = int(port)
	if _, present := fields["caPem"]; present {
		pem, err := platformManagementString(fields, "caPem")
		if err != nil {
			return t, err
		}
		// The reviewed authority is re-encoded into its one canonical spelling; a value that is not
		// exactly one CA certificate is refused here, never stored.
		if t.CAPEM, _, err = appplatform.CanonicalPostgresCA(pem); err != nil {
			return t, errPlatformManagementInput
		}
	}
	return t, nil
}

func platformPostgresRequestFromBody(w http.ResponseWriter, r *http.Request) (platformPostgresRequest, error) {
	var req platformPostgresRequest
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformManagementBodyLimit))
	if err != nil {
		return req, errPlatformManagementInput
	}
	fields, err := platformManagementObject(raw, "policyRevision", "previousPolicySha256", "targets", "deadlineMs", "concurrency")
	if err != nil {
		return req, err
	}
	for _, key := range []string{"policyRevision", "previousPolicySha256", "targets"} {
		if _, ok := fields[key]; !ok {
			return req, errPlatformManagementInput
		}
	}
	revision, err := platformManagementString(fields, "policyRevision")
	if err != nil || !platformPositiveCounter(revision) {
		return req, errPlatformManagementInput
	}
	req.PolicyRevision, _ = strconv.ParseInt(revision, 10, 64)
	if req.PreviousPolicySHA256, err = platformManagementString(fields, "previousPolicySha256"); err != nil || !platformValidSHA(req.PreviousPolicySHA256) {
		return req, errPlatformManagementInput
	}
	var items []json.RawMessage
	if json.Unmarshal(fields["targets"], &items) != nil || items == nil || len(items) > appplatform.MaxPostgresTargets {
		return req, errPlatformManagementInput
	}
	for _, item := range items {
		t, err := platformPostgresTargetFromJSON(item)
		if err != nil {
			return req, err
		}
		req.Targets = append(req.Targets, t)
	}
	if len(req.Targets) == 0 {
		// A withdrawal names no bounds.
		if len(fields) != 3 {
			return req, errPlatformManagementInput
		}
		return req, nil
	}
	ms, ok := platformManagementInt(fields["deadlineMs"])
	if !ok || ms < 1 || ms > appplatform.MaxPostgresDeadlineMS {
		return req, errPlatformManagementInput
	}
	req.Deadline = time.Duration(ms) * time.Millisecond
	n, ok := platformManagementInt(fields["concurrency"])
	if !ok || n < 1 || n > appplatform.MaxPostgresConcurrency {
		return req, errPlatformManagementInput
	}
	req.Concurrency = int(n)
	return req, nil
}

func platformPostgresError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPlatformPostgresBusy):
		jsonErrorCode(w, 409, "app_platform_installation_not_ready", "the installation must be ready with no operation in progress")
	case errors.Is(err, errPlatformPostgresUnsupported):
		jsonErrorCode(w, 409, "app_platform_postgres_unsupported", "the runner does not serve the PostgreSQL probe")
	case errors.Is(err, errPlatformPrivateTargetNotAllowed):
		jsonErrorCode(w, 409, "app_platform_private_target_not_allowed", "a private target is not admitted by the runner allowlist")
	default:
		platformManagementError(w, err)
	}
}

type platformPostgresDiffTarget struct {
	platformPostgresTargetView
	Allowlist *appplatform.PrivateNetwork `json:"allowlist"`
}

// platformPostgresDiff is what the administrator confirms: every target as postgres://host:port/db
// with its class, the allowlist entry that admits a private one, the fingerprint of the reviewed
// certificate authority, the bounds and the one thing the probe can do.
func platformPostgresDiff(plan platformPostgresPolicyPlan) (map[string]any, error) {
	targets := make([]platformPostgresDiffTarget, 0, len(plan.Targets))
	var policy appplatform.PostgresPolicy
	if plan.Granted {
		parsed, err := appplatform.ParsePostgresPolicy([]byte(plan.Policy))
		if err != nil {
			return nil, errPlatformCapabilityPolicy
		}
		policy = parsed.Policy
	}
	for _, view := range plan.Targets {
		item := platformPostgresDiffTarget{platformPostgresTargetView: view}
		if t, ok := policy.Select(view.ID); ok && t.Network == appplatform.HTTPNetworkPrivate {
			for _, n := range plan.PrivateNetworks {
				if (appplatform.PostgresPolicy{Targets: []appplatform.PostgresTarget{t}}).CheckPrivate([]appplatform.PrivateNetwork{n}) {
					entry := n
					item.Allowlist = &entry
					break
				}
			}
		}
		targets = append(targets, item)
	}
	return map[string]any{
		"granted": plan.Granted, "previous_granted": plan.PreviousGranted, "targets": targets,
		"deadline_ms": policy.Deadline.Milliseconds(), "concurrency": policy.Concurrency,
		"previous_policy_sha256": plan.PreviousPolicySHA256, "policy_sha256": platformDigest([]byte(plan.Policy)),
		"private_networks_sha256": plan.PrivateNetworksSHA256, "disclosure": platformPostgresDisclosure,
	}, nil
}

func (s *Server) apiPlatformPostgresPolicyPlan(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	req, err := platformPostgresRequestFromBody(w, r)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok {
		platformManagementError(w, errAppPlatformRuntimeConflict)
		return
	}
	result, err := s.createPlatformPostgresPolicyReview(r.Context(), session, endpoint, installation, req)
	if err != nil {
		platformPostgresError(w, err)
		return
	}
	diff, err := platformPostgresDiff(result.Plan)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"plan_id": result.ID, "plan_sha256": result.SHA256, "expires_ms": strconv.FormatInt(result.ExpiresMS, 10),
		"policy_revision": strconv.FormatInt(result.Plan.PolicyRevision, 10), "diff": diff,
	})
}

func (s *Server) apiPlatformPostgresPolicyConsume(w http.ResponseWriter, r *http.Request, user string) {
	if !platformValidUUID(r.PathValue("installation")) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "plan_id", "plan_sha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	planID, err1 := platformManagementString(fields, "plan_id")
	planSHA, err2 := platformManagementString(fields, "plan_sha256")
	if err1 != nil || err2 != nil || !platformValidUUID(planID) || !platformValidSHA(planSHA) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok {
		platformManagementError(w, errAppPlatformRuntimeConflict)
		return
	}
	change, err := s.consumePlatformPostgresPolicyReview(r.Context(), session, endpoint, r.PathValue("installation"), planID, planSHA)
	if err != nil {
		platformPostgresError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "granted": change.Granted, "policy_revision": strconv.FormatInt(change.PolicyRevision, 10)})
}

type platformPostgresPreview struct {
	PolicyRevision       string                       `json:"policy_revision"`
	Declared             bool                         `json:"declared"`
	Granted              bool                         `json:"granted"`
	Ready                bool                         `json:"ready"`
	PreviousPolicySHA256 string                       `json:"previous_policy_sha256"`
	Targets              []platformPostgresTargetView `json:"targets"`
	DeadlineMS           int64                        `json:"deadline_ms"`
	Concurrency          int                          `json:"concurrency"`
	PrivateNetworksSHA   string                       `json:"private_networks_sha256"`
	Disclosure           string                       `json:"disclosure"`
}

// apiPlatformPostgresPolicyPreview is what the settings page shows before a review: whether the
// release declares the capability, the granted policy with its targets and the policy revision and
// digest a plan must name.
func (s *Server) apiPlatformPostgresPolicyPreview(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	out := platformPostgresPreview{Targets: []platformPostgresTargetView{}, Disclosure: platformPostgresDisclosure}
	err = s.withAppPlatformActorRead(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var desired, phase, release string
		var revision int64
		var descriptor []byte
		if err := e.queryRow(`SELECT i.desired_state,i.phase,i.policy_revision,a.release_sha256,r.descriptor
			FROM app_platform_installations i
			JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active'
			JOIN app_platform_releases r ON r.sha256=a.release_sha256 WHERE i.id=?`, installation).Scan(&desired, &phase, &revision, &release, &descriptor); err != nil {
			return errPlatformInstallationMissing
		}
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.SHA256 != release {
			return errPlatformInstallationMissing
		}
		for _, name := range parsed.Manifest.Capabilities.Optional {
			out.Declared = out.Declared || name == platformCapabilityNetworkPostgres
		}
		operations, err := platformUnresolvedOperationCount(e, installation)
		if err != nil {
			return err
		}
		effects, err := platformUnresolvedOperationEffectCount(e, installation)
		if err != nil {
			return err
		}
		var granted int
		var stored string
		if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.postgres' AND policy_revision=?`, installation, revision).Scan(&granted, &stored); err != nil {
			return nil
		}
		out.PolicyRevision, out.Granted, out.PreviousPolicySHA256 = strconv.FormatInt(revision, 10), granted == 1, platformDigest([]byte(stored))
		out.Ready = out.Declared && desired == "enabled" && phase == "ready" && operations+effects == 0
		if granted == 1 {
			if policy, err := appplatform.ParsePostgresPolicy([]byte(stored)); err == nil {
				if views, err := platformPostgresTargetViews(policy.Policy); err == nil {
					out.Targets, out.DeadlineMS, out.Concurrency, out.PrivateNetworksSHA = views, policy.Policy.Deadline.Milliseconds(), policy.Policy.Concurrency, policy.Policy.PrivateNetworksSHA256
				}
			}
		}
		return nil
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, out)
}
