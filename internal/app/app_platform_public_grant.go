package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The public.projection grant (doc 179 with amendments C2 and C3). The capability is optional and
// install leaves it off. An administrator enables it from the settings page after reading the
// disclosure, choosing the reviewed routes and limits; disabling is immediate and needs no review.
// The public handlers read the row on every request, so a withdrawal has no delay of its own.

const (
	platformPublicEnabledAudit       = "app_platform.public_projection_enabled"
	platformPublicPolicyChangedAudit = "app_platform.public_projection_policy_changed"
	platformPublicDisabledAudit      = "app_platform.public_projection_disabled"
	platformPublicSummaryAudit       = "app_platform.public_projection_summary"

	// The sentence an administrator must be shown before anonymous exposure (doc 179 section 4).
	// The settings page shows it in en-US, zh-CN and zh-TW; the digest binds the English text.
	platformPublicDisclosure = "Anyone who knows this address can read these responses without signing in."
)

// platformReadPublicGrant returns the stored policy of a granted public.projection row after
// checking it against the release's declared block. Any other state is unavailable.
func platformReadPublicGrant(e migExec, installation string, policyRevision int64, m appplatform.Manifest, lock string) (string, error) {
	declares := false
	for _, name := range m.Capabilities.Optional {
		declares = declares || name == "public.projection"
	}
	if !declares || m.Public == nil || m.Backend == nil {
		return "", errAppPlatformLaunchUnavailable
	}
	var granted int
	var policy string
	if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection' AND policy_revision=?`+lock, installation, policyRevision).Scan(&granted, &policy); err != nil || granted != 1 {
		return "", errAppPlatformLaunchUnavailable
	}
	if _, err := appplatform.ParsePublicPolicy(policy, *m.Public); err != nil {
		return "", errAppPlatformLaunchUnavailable
	}
	return policy, nil
}

var errPlatformPublicGrantBusy = errors.New("installation is not ready for a grant change")

// platformPublicDisclosureDigest binds an enable to the exact declaration and installation the
// administrator was shown: a different release, revision or declared block gives another digest.
func platformPublicDisclosureDigest(installation string, policyRevision int64, release string, declared appplatform.Public) string {
	block, _ := json.Marshal(declared)
	frame, _ := json.Marshal([]string{"portal.public-projection-disclosure/1-dev.1", installation, strconv.FormatInt(policyRevision, 10), release, string(block), platformPublicDisclosure})
	return platformDigest(frame)
}

type platformPublicPreview struct {
	PolicyRevision   string                    `json:"policy_revision"`
	Granted          bool                      `json:"granted"`
	Ready            bool                      `json:"ready"`
	Declared         *platformPublicDeclared   `json:"declared"`
	Policy           *appplatform.PublicPolicy `json:"policy"`
	URL              string                    `json:"url"`
	Disclosure       string                    `json:"disclosure"`
	DisclosureSHA256 string                    `json:"disclosure_sha256"`
}

type platformPublicDeclared struct {
	Routes   []appplatform.PublicRoute `json:"routes"`
	Document string                    `json:"document"`
}

func (s *Server) apiPlatformPublicPreview(w http.ResponseWriter, r *http.Request, user string) {
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
	var out platformPublicPreview
	err = s.withAppPlatformActorRead(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var desired, phase, release string
		var revision int64
		var descriptor []byte
		if err := e.queryRow(`SELECT i.desired_state,i.phase,i.policy_revision,a.release_sha256,r.descriptor
			FROM app_platform_installations i
			JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active'
			JOIN app_platform_releases r ON r.sha256=a.release_sha256 WHERE i.id=?`, installation).Scan(&desired, &phase, &revision, &release, &descriptor); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errPlatformInstallationMissing
			}
			return err
		}
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.SHA256 != release || parsed.Manifest.Public == nil {
			return errPlatformInstallationMissing
		}
		declared := *parsed.Manifest.Public
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
		if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection' AND policy_revision=?`, installation, revision).Scan(&granted, &stored); err != nil {
			return errPlatformInstallationMissing
		}
		policy := appplatform.DefaultPublicPolicy(declared)
		if granted == 1 {
			if current, err := appplatform.ParsePublicPolicy(stored, declared); err == nil {
				policy = current
			}
		}
		out = platformPublicPreview{
			PolicyRevision: strconv.FormatInt(revision, 10), Granted: granted == 1, Ready: desired == "enabled" && phase == "ready" && operations+effects == 0,
			Declared: &platformPublicDeclared{Routes: declared.Routes, Document: declared.Document}, Policy: &policy,
			URL: "/public/apps/" + installation + "/", Disclosure: platformPublicDisclosure,
			DisclosureSHA256: platformPublicDisclosureDigest(installation, revision, release, declared),
		}
		return nil
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, out)
}

type platformPublicChange struct {
	enabled          bool
	revision         string
	policy           appplatform.PublicPolicy
	disclosureSHA256 string
}

func (s *Server) apiPlatformPublicGrant(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "enabled", "policyRevision", "routes", "document", "clientPerMinute", "backendPerSecond", "disclosureSha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var change platformPublicChange
	switch string(bytes.TrimSpace(fields["enabled"])) {
	case "true":
		change.enabled = true
	case "false":
	default:
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	if change.revision, err = platformManagementString(fields, "policyRevision"); err != nil || !platformPositiveCounter(change.revision) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	if change.enabled {
		var routes []string
		var document bool
		var client, backend int
		if json.Unmarshal(fields["routes"], &routes) != nil || routes == nil || json.Unmarshal(fields["document"], &document) != nil ||
			json.Unmarshal(fields["clientPerMinute"], &client) != nil || json.Unmarshal(fields["backendPerSecond"], &backend) != nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		if change.disclosureSHA256, err = platformManagementString(fields, "disclosureSha256"); err != nil || !platformValidSHA(change.disclosureSHA256) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		change.policy = appplatform.PublicPolicy{Format: appplatform.PublicPolicyFormat, Routes: routes, Document: document, ClientPerMinute: client, BackendPerSecond: backend}
	} else {
		// A withdrawal carries nothing else: it must not be possible to refuse it for a bad policy.
		for _, key := range []string{"routes", "document", "clientPerMinute", "backendPerSecond", "disclosureSha256"} {
			if !bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
				platformManagementError(w, errPlatformManagementInput)
				return
			}
		}
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if err := s.setPlatformPublicGrant(r.Context(), session, installation, change); err != nil {
		if errors.Is(err, errPlatformPublicGrantBusy) {
			jsonErrorCode(w, 409, "app_platform_installation_not_ready", "the installation must be ready with no operation in progress")
			return
		}
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "granted": change.enabled})
}

// setPlatformPublicGrant flips the existing grant row of the installation's current policy revision
// and stores or clears its policy. It never creates a row, as setPlatformPrincipalsGrant does not.
// The cache is emptied after the transaction commits, on every change, so no response of the old
// policy outlives the call.
func (s *Server) setPlatformPublicGrant(ctx context.Context, session platformActorSession, installation string, change platformPublicChange) error {
	expected, err := strconv.ParseInt(change.revision, 10, 64)
	if err != nil || expected < 1 {
		return errPlatformManagementInput
	}
	err = s.withAppPlatformActorContext(ctx, session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var current int64
		var desired, phase string
		if err := e.queryRow(`SELECT policy_revision,desired_state,phase FROM app_platform_installations WHERE id=?`+lock, installation).Scan(&current, &desired, &phase); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errPlatformInstallationMissing
			}
			return err
		}
		// Withdrawal is allowed in any state (doc 179 section 4): it only removes exposure, so
		// neither a pending operation nor a disabled installation may stand in its way, and it
		// acts on the row of the current revision whatever page asked. Enabling or changing
		// the policy needs a ready installation with no unfinished operation, because install,
		// enable and update read the grant rows as evidence.
		if !change.enabled {
			var granted int
			var stored string
			if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection' AND policy_revision=?`+lock, installation, current).Scan(&granted, &stored); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errAppPlatformPlanChanged
				}
				return err
			}
			if granted == 0 && stored == "{}" {
				return nil
			}
			changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=0,policy_json='{}',updated_ms=? WHERE installation_id=? AND capability='public.projection' AND policy_revision=? AND granted=? AND policy_json=?`, control.nowMS, installation, current, granted, stored)
			if err := platformLeaseCAS(changed, err); err != nil {
				return errAppPlatformPlanChanged
			}
			detail, err := json.Marshal(map[string]any{"capability": "public.projection", "policy_revision": current, "policy": json.RawMessage("{}")})
			if err != nil {
				return err
			}
			_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, platformPublicDisabledAudit, installation, string(detail))
			return err
		}
		if desired != "enabled" || phase != "ready" {
			return errPlatformPublicGrantBusy
		}
		operations, err := platformUnresolvedOperationCount(e, installation)
		if err != nil {
			return err
		}
		effects, err := platformUnresolvedOperationEffectCount(e, installation)
		if err != nil {
			return err
		}
		if operations+effects != 0 {
			return errPlatformPublicGrantBusy
		}
		if current != expected {
			return errAppPlatformPlanChanged
		}
		var release string
		var descriptor []byte
		if err := e.queryRow(`SELECT a.release_sha256,r.descriptor FROM app_platform_activations a JOIN app_platform_releases r ON r.sha256=a.release_sha256 WHERE a.installation_id=? AND a.state='active'`, installation).Scan(&release, &descriptor); err != nil {
			return errAppPlatformPlanChanged
		}
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.SHA256 != release || parsed.Manifest.Public == nil {
			return errAppPlatformPlanChanged
		}
		var granted int
		var stored string
		if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection' AND policy_revision=?`+lock, installation, current).Scan(&granted, &stored); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAppPlatformPlanChanged
			}
			return err
		}
		newPolicy, action := "{}", platformPublicDisabledAudit
		if change.enabled {
			if change.disclosureSHA256 != platformPublicDisclosureDigest(installation, current, release, *parsed.Manifest.Public) {
				return errAppPlatformPlanChanged
			}
			if _, err := appplatform.ParsePublicPolicy(appplatform.CanonicalPublicPolicy(change.policy), *parsed.Manifest.Public); err != nil {
				return errPlatformManagementInput
			}
			newPolicy = appplatform.CanonicalPublicPolicy(change.policy)
			action = platformPublicEnabledAudit
			if granted == 1 {
				action = platformPublicPolicyChangedAudit
			}
		}
		if granted == boolInt(change.enabled) && stored == newPolicy {
			return nil
		}
		changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=?,policy_json=?,updated_ms=? WHERE installation_id=? AND capability='public.projection' AND policy_revision=? AND granted=? AND policy_json=?`, boolInt(change.enabled), newPolicy, control.nowMS, installation, current, granted, stored)
		if err := platformLeaseCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		detail, err := json.Marshal(map[string]any{"capability": "public.projection", "policy_revision": current, "policy": json.RawMessage(newPolicy)})
		if err != nil {
			return err
		}
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, action, installation, string(detail))
		return err
	})
	if err == nil {
		s.platformPublic.purge(installation)
		if !change.enabled {
			// The open traffic window ends with the exposure it describes.
			s.flushPlatformPublicSummary(installation)
		}
	}
	return err
}
