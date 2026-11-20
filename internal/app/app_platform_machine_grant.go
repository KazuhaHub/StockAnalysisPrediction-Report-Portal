package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The machine.read grant (doc 183 section 5.5). The capability is optional and install leaves it off.
// Until the doc 175 capability review carries it, an administrator enables it from the settings page
// after reading the disclosure, choosing the reviewed routes and the installation limits; the POST
// must echo the digest of what was shown. Disabling is immediate, needs no review, and revokes every
// token of the installation in the same transaction.

const (
	platformMachineEnabledAudit       = "app_platform.machine_read_enabled"
	platformMachinePolicyChangedAudit = "app_platform.machine_read_policy_changed"
	platformMachineDisabledAudit      = "app_platform.machine_read_disabled"
	platformMachineSummaryAudit       = "app_platform.machine_read_summary"

	// The sentence an administrator must be shown before enabling (doc 183 section 5). The settings
	// page shows it in en-US, zh-CN and zh-TW; the digest binds the English text.
	platformMachineDisclosure = "Holders of a machine token for this app can read these responses without signing in, until the token expires or is revoked."
)

// platformReadMachineGrant is the sessionless access check of one machine read: the granted
// machine.read row of the current policy revision, a policy that is a subset of the release's declared
// block, and the token the request is made with, still active, unexpired and bound to this
// installation. It returns one string naming exactly that state, so an access read before and after the
// relay can be compared: a withdrawal, a policy change or a revocation in between changes it.
func platformReadMachineGrant(e migExec, installation string, policyRevision int64, m appplatform.Manifest, tokenID string, nowMS int64, lock string) (string, error) {
	declares := false
	for _, name := range m.Capabilities.Optional {
		declares = declares || name == "machine.read"
	}
	if !declares || m.Machine == nil || m.Backend == nil {
		return "", errAppPlatformLaunchUnavailable
	}
	var granted int
	var policy string
	if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='machine.read' AND policy_revision=?`+lock, installation, policyRevision).Scan(&granted, &policy); err != nil || granted != 1 {
		return "", errAppPlatformLaunchUnavailable
	}
	if _, err := appplatform.ParseMachinePolicy(policy, *m.Machine); err != nil {
		return "", errAppPlatformLaunchUnavailable
	}
	var state, owner string
	var expires, revision int64
	if err := e.queryRow(`SELECT state,installation_id,expires_ms,revision FROM app_platform_machine_tokens WHERE id=?`+lock, tokenID).Scan(&state, &owner, &expires, &revision); err != nil || state != "active" || owner != installation || expires <= nowMS {
		return "", errAppPlatformLaunchUnavailable
	}
	return policy + "\x00" + tokenID + "\x00" + strconv.FormatInt(revision, 10), nil
}

// platformMachineDisclosureDigest binds an enable to the exact declaration and installation the
// administrator was shown: a different release, revision or declared block gives another digest.
func platformMachineDisclosureDigest(installation string, policyRevision int64, release string, declared appplatform.Machine) string {
	block, _ := json.Marshal(declared)
	frame, _ := json.Marshal([]string{"portal.machine-read-disclosure/1-dev.1", installation, strconv.FormatInt(policyRevision, 10), release, string(block), platformMachineDisclosure})
	return platformDigest(frame)
}

type platformMachinePreview struct {
	PolicyRevision   string                     `json:"policy_revision"`
	Granted          bool                       `json:"granted"`
	Ready            bool                       `json:"ready"`
	Declared         *platformMachineDeclared   `json:"declared"`
	Policy           *appplatform.MachinePolicy `json:"policy"`
	OpenAPIPath      string                     `json:"openapi_path"`
	Disclosure       string                     `json:"disclosure"`
	DisclosureSHA256 string                     `json:"disclosure_sha256"`
}

type platformMachineDeclared struct {
	Routes []appplatform.MachineRoute `json:"routes"`
}

func (s *Server) apiPlatformMachinePreview(w http.ResponseWriter, r *http.Request, user string) {
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
	var out platformMachinePreview
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
		if err != nil || parsed.SHA256 != release || parsed.Manifest.Machine == nil {
			return errPlatformInstallationMissing
		}
		declared := *parsed.Manifest.Machine
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
		if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='machine.read' AND policy_revision=?`, installation, revision).Scan(&granted, &stored); err != nil {
			return errPlatformInstallationMissing
		}
		policy := appplatform.DefaultMachinePolicy(declared)
		if granted == 1 {
			if current, err := appplatform.ParseMachinePolicy(stored, declared); err == nil {
				policy = current
			}
		}
		out = platformMachinePreview{
			PolicyRevision: strconv.FormatInt(revision, 10), Granted: granted == 1, Ready: desired == "enabled" && phase == "ready" && operations+effects == 0,
			Declared: &platformMachineDeclared{Routes: declared.Routes}, Policy: &policy,
			OpenAPIPath: "/machine/apps/" + installation + "/openapi.json", Disclosure: platformMachineDisclosure,
			DisclosureSHA256: platformMachineDisclosureDigest(installation, revision, release, declared),
		}
		return nil
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, out)
}

type platformMachineChange struct {
	enabled          bool
	revision         string
	policy           appplatform.MachinePolicy
	disclosureSHA256 string
}

func (s *Server) apiPlatformMachineGrant(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "enabled", "policyRevision", "routes", "installationPerSecond", "installationConcurrency", "disclosureSha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var change platformMachineChange
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
		var perSecond, concurrency int
		if json.Unmarshal(fields["routes"], &routes) != nil || routes == nil ||
			json.Unmarshal(fields["installationPerSecond"], &perSecond) != nil || json.Unmarshal(fields["installationConcurrency"], &concurrency) != nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		if change.disclosureSHA256, err = platformManagementString(fields, "disclosureSha256"); err != nil || !platformValidSHA(change.disclosureSHA256) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		change.policy = appplatform.MachinePolicy{Format: appplatform.MachinePolicyFormat, Routes: routes, InstallationPerSecond: perSecond, InstallationConcurrency: concurrency}
	} else {
		// A withdrawal carries nothing else: it must not be possible to refuse it for a bad policy.
		for _, key := range []string{"routes", "installationPerSecond", "installationConcurrency", "disclosureSha256"} {
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
	if err := s.setPlatformMachineGrant(r.Context(), session, installation, change); err != nil {
		if errors.Is(err, errPlatformPublicGrantBusy) {
			jsonErrorCode(w, 409, "app_platform_installation_not_ready", "the installation must be ready with no operation in progress")
			return
		}
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "granted": change.enabled})
}

// setPlatformMachineGrant flips the existing grant row of the installation's current policy revision
// and stores or clears its policy; it never creates a row. A withdrawal is allowed in any state and
// revokes every token of the installation in the same transaction. Switching the capability on revokes
// the tokens a previous period of it left behind (an update resets the grant without going through a
// withdrawal), so no old token comes back to life with the capability.
func (s *Server) setPlatformMachineGrant(ctx context.Context, session platformActorSession, installation string, change platformMachineChange) error {
	expected, err := strconv.ParseInt(change.revision, 10, 64)
	if err != nil || expected < 1 {
		return errPlatformManagementInput
	}
	return s.withAppPlatformActorContext(ctx, session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
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
		var granted int
		var stored string
		readGrant := func() error {
			if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='machine.read' AND policy_revision=?`+lock, installation, current).Scan(&granted, &stored); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errAppPlatformPlanChanged
				}
				return err
			}
			return nil
		}
		if !change.enabled {
			if err := readGrant(); err != nil {
				return err
			}
			if granted == 1 || stored != "{}" {
				changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=0,policy_json='{}',updated_ms=? WHERE installation_id=? AND capability='machine.read' AND policy_revision=? AND granted=? AND policy_json=?`, control.nowMS, installation, current, granted, stored)
				if err := platformLeaseCAS(changed, err); err != nil {
					return errAppPlatformPlanChanged
				}
				if err := platformMachineAudit(e, actor, control.nowMS, platformMachineDisabledAudit, installation, map[string]any{"capability": "machine.read", "policy_revision": current, "policy": json.RawMessage("{}")}); err != nil {
					return err
				}
			}
			_, err := revokePlatformMachineTokens(e, actor, control.nowMS, installation, "capability_disabled")
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
		if err != nil || parsed.SHA256 != release || parsed.Manifest.Machine == nil {
			return errAppPlatformPlanChanged
		}
		if err := readGrant(); err != nil {
			return err
		}
		if change.disclosureSHA256 != platformMachineDisclosureDigest(installation, current, release, *parsed.Manifest.Machine) {
			return errAppPlatformPlanChanged
		}
		if _, err := appplatform.ParseMachinePolicy(appplatform.CanonicalMachinePolicy(change.policy), *parsed.Manifest.Machine); err != nil {
			return errPlatformManagementInput
		}
		newPolicy, action := appplatform.CanonicalMachinePolicy(change.policy), platformMachineEnabledAudit
		if granted == 1 {
			action = platformMachinePolicyChangedAudit
		} else if _, err := revokePlatformMachineTokens(e, actor, control.nowMS, installation, "capability_reenabled"); err != nil {
			return err
		}
		if granted == 1 && stored == newPolicy {
			return nil
		}
		changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=1,policy_json=?,updated_ms=? WHERE installation_id=? AND capability='machine.read' AND policy_revision=? AND granted=? AND policy_json=?`, newPolicy, control.nowMS, installation, current, granted, stored)
		if err := platformLeaseCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		return platformMachineAudit(e, actor, control.nowMS, action, installation, map[string]any{"capability": "machine.read", "policy_revision": current, "policy": json.RawMessage(newPolicy)})
	})
}
