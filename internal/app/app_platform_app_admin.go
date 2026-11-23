package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

// App-owned admin pages (doc 191). A Portal administrator, or a user an administrator delegated the
// installation to, may open the app's admin surface. The surface is carried by the session value
// the HTTP layer hands to every gate, exactly as the anonymous public/ingress/machine reads are: only
// the admin handlers set it, so no reader route can ask for it.

const (
	platformAppAdminGrantedAudit = "app_platform.app_admin_granted"
	platformAppAdminRevokedAudit = "app_platform.app_admin_revoked"
	platformAdminRequestAudit    = "app_platform.admin_request"
)

var (
	errPlatformAppAdminUserNotFound = errors.New("application administrator: user not found")
	errPlatformAppAdminNotFound     = errors.New("application administrator: installation not found")
)

// platformAppAdmin is the one access decision of the admin surface, the counterpart of
// platformLaunchAudience: a Portal administrator, or an active account with a delegation row whose
// account_created still equals the account's users.created_at (a recreated account with the same name
// has another one and inherits nothing). Admin views do not need audience membership. The caller holds
// the OU mutation lock, which every grant and revoke also takes, and the account came from the same
// transaction's session read, so role, activity and created_at are current.
func platformAppAdmin(e migExec, account platformActorAccount, installationID string) error {
	if can(account.role, PermManage) {
		return nil
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	var created string
	if err := e.queryRow(`SELECT account_created FROM app_platform_app_admins WHERE installation_id=? AND username=?`+lock, installationID, account.username).Scan(&created); err != nil || created != account.created {
		return errAppPlatformLaunchUnavailable
	}
	return nil
}

// platformSurfaceAccess decides access for the surface the session was admitted for.
func platformSurfaceAccess(e migExec, session platformActorSession, account platformActorAccount, installationID string) error {
	if session.adminSurface {
		return platformAppAdmin(e, account, installationID)
	}
	return platformLaunchAudience(e, account, installationID)
}

// platformSignViewerAssertion signs a viewer request: SignAdmin on the admin surface, Sign otherwise.
// The admin claim means only that the host verified the viewer as an administrator of this app.
func platformSignViewerAssertion(signer protocol.Signer, admin bool, method, path string, body []byte, viewer protocol.Viewer, now time.Time) (string, error) {
	if admin {
		return signer.SignAdmin(method, path, body, viewer, now)
	}
	return signer.Sign(method, path, body, viewer, now)
}

// deletePlatformAppAdmins removes an installation's delegations in the transaction that removes it.
func deletePlatformAppAdmins(e migExec, installation string) error {
	_, err := e.exec(`DELETE FROM app_platform_app_admins WHERE installation_id=?`, installation)
	return err
}

type platformAppAdminView struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	GrantedBy   string `json:"grantedBy"`
	GrantedAt   int64  `json:"grantedAt"`
}

func platformAppAdminListRows(e migExec, installation string) ([]platformAppAdminView, error) {
	rows, err := e.query(`SELECT a.username,COALESCE(u.display_name,''),a.granted_by,a.granted_at
		FROM app_platform_app_admins a JOIN users u ON u.username=a.username
		WHERE a.installation_id=? AND a.account_created=COALESCE(u.created_at,'') AND COALESCE(u.active,1)=1
		ORDER BY a.username`, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []platformAppAdminView{}
	for rows.Next() {
		var v platformAppAdminView
		if err := rows.Scan(&v.Username, &v.DisplayName, &v.GrantedBy, &v.GrantedAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Server) listPlatformAppAdmins(ctx context.Context, session platformActorSession, installation string) ([]platformAppAdminView, error) {
	var result []platformAppAdminView
	err := s.withAppPlatformActorContextMode(ctx, session, false, true, func(e migExec, _ platformActorFacts, _ platformActorControl) error {
		if _, _, err := platformAppAdminInstallation(e, installation); err != nil {
			return err
		}
		var err error
		result, err = platformAppAdminListRows(e, installation)
		return err
	})
	return result, err
}

// platformAppAdminInstallation resolves a live installation: a missing or removed one is not found.
func platformAppAdminInstallation(e migExec, installation string) (appID string, state string, err error) {
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	if err := e.queryRow(`SELECT app_id,desired_state FROM app_platform_installations WHERE id=?`+lock, installation).Scan(&appID, &state); err != nil || state == "removed" {
		if err == nil || errors.Is(err, sql.ErrNoRows) {
			return "", "", errPlatformAppAdminNotFound
		}
		return "", "", err
	}
	return appID, state, nil
}

func platformAppAdminAudit(e migExec, facts platformActorFacts, nowMS int64, action, installation, appID, username string) error {
	detail, err := json.Marshal(map[string]any{"app_id": appID, "username": username})
	if err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`,
		time.UnixMilli(nowMS).UTC().Format(time.RFC3339), facts.username, facts.ouID, action, installation, string(detail))
	return err
}

// grantPlatformAppAdmin delegates the installation's admin page to an active user. Granting again is
// a no-op that writes no event; a row left by an account of the same name that was since recreated is
// replaced.
func (s *Server) grantPlatformAppAdmin(ctx context.Context, session platformActorSession, installation, username string) (platformAppAdminView, error) {
	var result platformAppAdminView
	err := s.withAppPlatformActorContext(ctx, session, false, func(e migExec, facts platformActorFacts, control platformActorControl) error {
		appID, _, err := platformAppAdminInstallation(e, installation)
		if err != nil {
			return err
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR SHARE"
		}
		var created string
		var active int
		var display string
		if err := e.queryRow(`SELECT COALESCE(created_at,''),COALESCE(active,1),COALESCE(display_name,'') FROM users WHERE username=?`+lock, username).Scan(&created, &active, &display); err != nil || active != 1 {
			if err == nil || errors.Is(err, sql.ErrNoRows) {
				return errPlatformAppAdminUserNotFound
			}
			return err
		}
		var storedCreated, by string
		var at int64
		err = e.queryRow(`SELECT account_created,granted_by,granted_at FROM app_platform_app_admins WHERE installation_id=? AND username=?`+lock, installation, username).Scan(&storedCreated, &by, &at)
		switch {
		case err == nil && storedCreated == created:
			result = platformAppAdminView{Username: username, DisplayName: display, GrantedBy: by, GrantedAt: at}
			return nil
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := e.exec(`DELETE FROM app_platform_app_admins WHERE installation_id=? AND username=?`, installation, username); err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_app_admins(installation_id,username,account_created,granted_by,granted_at) VALUES(?,?,?,?,?)`, installation, username, created, facts.username, control.nowMS); err != nil {
			return err
		}
		result = platformAppAdminView{Username: username, DisplayName: display, GrantedBy: facts.username, GrantedAt: control.nowMS}
		return platformAppAdminAudit(e, facts, control.nowMS, platformAppAdminGrantedAudit, installation, appID, username)
	})
	return result, err
}

// revokePlatformAppAdmin withdraws a delegation. It is idempotent: a row that is not there writes no
// event. A removed installation has no rows left, so it answers like one that never had any.
func (s *Server) revokePlatformAppAdmin(ctx context.Context, session platformActorSession, installation, username string) error {
	return s.withAppPlatformActorContext(ctx, session, false, func(e migExec, facts platformActorFacts, control platformActorControl) error {
		var appID string
		if err := e.queryRow(`SELECT app_id FROM app_platform_installations WHERE id=?`, installation).Scan(&appID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		changed, err := e.exec(`DELETE FROM app_platform_app_admins WHERE installation_id=? AND username=?`, installation, username)
		if err != nil {
			return err
		}
		if n, err := changed.RowsAffected(); err != nil || n == 0 {
			return err
		}
		return platformAppAdminAudit(e, facts, control.nowMS, platformAppAdminRevokedAudit, installation, appID, username)
	})
}

func platformAppAdminStatus(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPlatformAppAdminNotFound):
		jsonErrorCode(w, 404, "app_platform_not_found", "application not found")
	case errors.Is(err, errPlatformAppAdminUserNotFound):
		jsonErrorCode(w, 404, "app_platform_user_not_found", "user not found")
	default:
		platformManagementError(w, err)
	}
}

func platformAppAdminPathOK(r *http.Request) bool {
	return platformValidUUID(r.PathValue("installation")) && len(r.URL.Query()) == 0
}

func (s *Server) apiPlatformAppAdminList(w http.ResponseWriter, r *http.Request, user string) {
	if !platformAppAdminPathOK(r) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	admins, err := s.listPlatformAppAdmins(r.Context(), session, r.PathValue("installation"))
	if err != nil {
		platformAppAdminStatus(w, err)
		return
	}
	writeJSON(w, map[string]any{"admins": admins})
}

func (s *Server) apiPlatformAppAdminGrant(w http.ResponseWriter, r *http.Request, user string) {
	if !platformAppAdminPathOK(r) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "username")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	username, err := platformManagementString(fields, "username")
	if err != nil || username == "" || len(username) > 320 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	admin, err := s.grantPlatformAppAdmin(r.Context(), session, r.PathValue("installation"), username)
	if err != nil {
		platformAppAdminStatus(w, err)
		return
	}
	writeJSON(w, map[string]any{"admin": admin})
}

func (s *Server) apiPlatformAppAdminRevoke(w http.ResponseWriter, r *http.Request, user string) {
	username := r.PathValue("username")
	if !platformAppAdminPathOK(r) || username == "" || len(username) > 320 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if err := s.revokePlatformAppAdmin(r.Context(), session, r.PathValue("installation"), username); err != nil {
		platformAppAdminStatus(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
