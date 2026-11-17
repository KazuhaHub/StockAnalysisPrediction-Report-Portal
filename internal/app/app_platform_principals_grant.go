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
)

// Enable and withdraw the principals.read grant (doc 181 section 4). The capability is optional
// and install leaves it off; an administrator turns it on after reading the disclosure, and turning
// it off is immediate because the directory reads the grant row on every call.
const (
	platformPrincipalsGrantedAudit   = "app_platform.capability_granted"
	platformPrincipalsWithdrawnAudit = "app_platform.capability_withdrawn"
)

func (s *Server) apiPlatformPrincipalsGrant(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "enabled", "policyRevision")
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
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if err := s.setPlatformPrincipalsGrant(r.Context(), session, installation, enabled, revision); err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "granted": enabled})
}

// setPlatformPrincipalsGrant flips the existing grant row of the installation's current policy
// revision. It never creates a row: a missing row means the installed release does not declare the
// capability, and a changed revision means the administrator reviewed something that is gone.
func (s *Server) setPlatformPrincipalsGrant(ctx context.Context, session platformActorSession, installation string, enabled bool, policyRevision string) error {
	expected, err := strconv.ParseInt(policyRevision, 10, 64)
	if err != nil || expected < 1 {
		return errPlatformManagementInput
	}
	return s.withAppPlatformActorContext(ctx, session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var current int64
		var desired string
		if err := e.queryRow(`SELECT policy_revision,desired_state FROM app_platform_installations WHERE id=?`+lock, installation).Scan(&current, &desired); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errPlatformInstallationMissing
			}
			return err
		}
		if current != expected || (enabled && desired == "removed") {
			return errAppPlatformPlanChanged
		}
		var granted int
		if err := e.queryRow(`SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='principals.read' AND policy_revision=?`+lock, installation, current).Scan(&granted); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAppPlatformPlanChanged
			}
			return err
		}
		if granted == boolInt(enabled) {
			return nil
		}
		changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=?,updated_ms=? WHERE installation_id=? AND capability='principals.read' AND policy_revision=? AND granted=?`, boolInt(enabled), control.nowMS, installation, current, granted)
		if err := platformLeaseCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		action := platformPrincipalsWithdrawnAudit
		if enabled {
			action = platformPrincipalsGrantedAudit
		}
		detail, err := json.Marshal(map[string]any{"capability": "principals.read", "policy_revision": current})
		if err != nil {
			return err
		}
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, action, installation, string(detail))
		return err
	})
}
