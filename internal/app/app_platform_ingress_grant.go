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

// The ingress.webhook grant (doc 180 section 4). The capability is optional and install leaves it
// off. Until the doc 175 capability policy review exists, an administrator enables it from the
// settings page after reading the routes and the URL pattern, through an interim route modelled on
// public.projection: a preview that returns the disclosure and a digest, and a change that must echo
// it. Disabling is immediate and needs no review. The handler reads the row on every request, so a
// withdrawal has no delay of its own.

const (
	platformIngressEnabledAudit  = "app_platform.ingress_enabled"
	platformIngressDisabledAudit = "app_platform.ingress_disabled"

	// The sentence an administrator must be shown before inbound exposure. The settings page shows it
	// in en-US, zh-CN and zh-TW; the digest binds the English text.
	platformIngressDisclosure = "Anyone who holds the signing secret of an endpoint can send requests that reach this app's backend. Portal checks each request and never gives the app the secret."
	platformIngressURLPattern = "/ingress/apps/{installation}/{route}/{endpoint}"
)

// platformIngressDisclosureDigest binds an enable to the exact declaration and installation the
// administrator was shown: a different release, revision or declared block gives another digest.
func platformIngressDisclosureDigest(installation string, policyRevision int64, release string, declared appplatform.Ingress) string {
	block, _ := json.Marshal(declared)
	frame, _ := json.Marshal([]string{"portal.ingress-disclosure/1-dev.1", installation, strconv.FormatInt(policyRevision, 10), release, string(block), platformIngressURLPattern, platformIngressDisclosure})
	return platformDigest(frame)
}

type platformIngressPreview struct {
	PolicyRevision   string                     `json:"policy_revision"`
	Granted          bool                       `json:"granted"`
	Ready            bool                       `json:"ready"`
	Routes           []platformIngressRouteView `json:"routes"`
	URLPattern       string                     `json:"url_pattern"`
	Disclosure       string                     `json:"disclosure"`
	DisclosureSHA256 string                     `json:"disclosure_sha256"`
}

type platformIngressRouteView struct {
	ID                   string `json:"id"`
	Path                 string `json:"path"`
	BodyBytes            int    `json:"body_bytes"`
	PerEndpointPerMinute int    `json:"per_endpoint_per_minute"`
}

func (s *Server) apiPlatformIngressPreview(w http.ResponseWriter, r *http.Request, user string) {
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
	var out platformIngressPreview
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
		if err != nil || parsed.SHA256 != release || parsed.Manifest.Ingress == nil {
			return errPlatformInstallationMissing
		}
		declared := *parsed.Manifest.Ingress
		operations, err := platformUnresolvedOperationCount(e, installation)
		if err != nil {
			return err
		}
		effects, err := platformUnresolvedOperationEffectCount(e, installation)
		if err != nil {
			return err
		}
		var granted int
		if err := e.queryRow(`SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='ingress.webhook' AND policy_revision=?`, installation, revision).Scan(&granted); err != nil {
			return errPlatformInstallationMissing
		}
		routes := make([]platformIngressRouteView, 0, len(declared.Routes))
		for _, route := range declared.Routes {
			routes = append(routes, platformIngressRouteView{route.ID, route.Path, route.BodyBytes, route.PerEndpointPerMinute})
		}
		out = platformIngressPreview{
			PolicyRevision: strconv.FormatInt(revision, 10), Granted: granted == 1, Ready: desired == "enabled" && phase == "ready" && operations+effects == 0,
			Routes: routes, URLPattern: platformIngressURLPattern, Disclosure: platformIngressDisclosure,
			DisclosureSHA256: platformIngressDisclosureDigest(installation, revision, release, declared),
		}
		return nil
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) apiPlatformIngressGrant(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "enabled", "policyRevision", "disclosureSha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var enabled bool
	switch string(bytes.TrimSpace(fields["enabled"])) {
	case "true":
		enabled = true
	case "false":
	default:
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	revision, err := platformManagementString(fields, "policyRevision")
	if err != nil || !platformPositiveCounter(revision) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	var disclosure string
	if enabled {
		if disclosure, err = platformManagementString(fields, "disclosureSha256"); err != nil || !platformValidSHA(disclosure) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
	} else if !bytes.Equal(bytes.TrimSpace(fields["disclosureSha256"]), []byte("null")) {
		// A withdrawal carries nothing else: it must not be possible to refuse it for a bad digest.
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if err := s.setPlatformIngressGrant(r.Context(), session, installation, enabled, revision, disclosure); err != nil {
		if errors.Is(err, errPlatformIngressGrantBusy) {
			jsonErrorCode(w, 409, "app_platform_installation_not_ready", "the installation must be ready with no operation in progress")
			return
		}
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "granted": enabled})
}

var errPlatformIngressGrantBusy = errors.New("installation is not ready for a grant change")

// setPlatformIngressGrant flips the existing grant row of the installation's current policy revision.
// It never creates a row: a missing row means the installed release does not declare the capability,
// and a changed revision means the administrator reviewed something that is gone. Withdrawal is
// allowed in any state, because it only removes exposure.
func (s *Server) setPlatformIngressGrant(ctx context.Context, session platformActorSession, installation string, enabled bool, policyRevision, disclosure string) error {
	expected, err := strconv.ParseInt(policyRevision, 10, 64)
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
		if enabled {
			if desired != "enabled" || phase != "ready" {
				return errPlatformIngressGrantBusy
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
				return errPlatformIngressGrantBusy
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
			if err != nil || parsed.SHA256 != release || parsed.Manifest.Ingress == nil {
				return errAppPlatformPlanChanged
			}
			if disclosure != platformIngressDisclosureDigest(installation, current, release, *parsed.Manifest.Ingress) {
				return errAppPlatformPlanChanged
			}
		}
		var granted int
		if err := e.queryRow(`SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='ingress.webhook' AND policy_revision=?`+lock, installation, current).Scan(&granted); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAppPlatformPlanChanged
			}
			return err
		}
		if granted == boolInt(enabled) {
			return nil
		}
		changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=?,updated_ms=? WHERE installation_id=? AND capability='ingress.webhook' AND policy_revision=? AND granted=?`, boolInt(enabled), control.nowMS, installation, current, granted)
		if err := platformLeaseCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		action := platformIngressDisabledAudit
		if enabled {
			action = platformIngressEnabledAudit
		}
		detail, err := json.Marshal(map[string]any{"capability": "ingress.webhook", "policy_revision": current})
		if err != nil {
			return err
		}
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, action, installation, string(detail))
		return err
	})
	if err == nil {
		s.platformIngress.forget(installation)
	}
	return err
}
