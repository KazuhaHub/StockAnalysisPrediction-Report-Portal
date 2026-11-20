package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Machine token management (doc 183 section 5.5): administrators only, through the same management
// wrapper as every other application-platform change (session, origin and content checks). The
// secret is returned by the create and rotate calls and by nothing else.

type platformMachineAuditRow struct {
	At     string          `json:"at"`
	Action string          `json:"action"`
	Detail json.RawMessage `json:"detail"`
}

type platformMachineTokenList struct {
	Tokens []platformMachineTokenView `json:"tokens"`
	// Usage is the recent traffic summaries and first-seen source addresses, newest first: what an
	// administrator needs to spot a leaked token used from a new place.
	Usage []platformMachineAuditRow `json:"usage"`
	Now   string                    `json:"now"`
}

type platformMachineTokenCreatedView struct {
	Token       string                   `json:"token"`
	TokenInfo   platformMachineTokenView `json:"tokenInfo"`
	OpenAPIPath string                   `json:"openapiPath"`
}

func (s *Server) apiPlatformMachineTokens(w http.ResponseWriter, r *http.Request, user string) {
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
	var out platformMachineTokenList
	err = s.withAppPlatformActorRead(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var exists int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_installations WHERE id=?`, installation).Scan(&exists); err != nil || exists != 1 {
			return errPlatformInstallationMissing
		}
		rows, err := listPlatformMachineTokens(e, installation)
		if err != nil {
			return err
		}
		out.Tokens = []platformMachineTokenView{}
		for _, t := range rows {
			out.Tokens = append(out.Tokens, t.view(control.nowMS))
		}
		out.Usage = []platformMachineAuditRow{}
		audit, err := e.query(`SELECT at,action,detail FROM audit_log WHERE target_type='app_platform_installation' AND target_id=? AND action IN (?,?) ORDER BY id DESC LIMIT 50`, installation, platformMachineSummaryAudit, platformMachineTokenSourceAudit)
		if err != nil {
			return err
		}
		defer audit.Close()
		for audit.Next() {
			var row platformMachineAuditRow
			var detail string
			if err := audit.Scan(&row.At, &row.Action, &detail); err != nil {
				return err
			}
			if json.Valid([]byte(detail)) {
				row.Detail = json.RawMessage(detail)
			} else {
				row.Detail = json.RawMessage("{}")
			}
			out.Usage = append(out.Usage, row)
		}
		out.Now = platformMachineTime(control.nowMS)
		return audit.Err()
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, out)
}

func platformMachineManagementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPlatformMachineGrantOff):
		jsonErrorCode(w, 409, "app_platform_machine_read_off", "machine read is not enabled for this installation")
	case errors.Is(err, errPlatformMachineTokenLimit):
		jsonErrorCode(w, 409, "app_platform_machine_token_limit", "too many active machine tokens for this installation")
	case errors.Is(err, errPlatformMachineTokenGone):
		jsonErrorCode(w, 404, "app_platform_machine_token_missing", "no active machine token has this handle")
	default:
		platformManagementError(w, err)
	}
}

func (s *Server) apiPlatformMachineTokenCreate(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "label", "routes", "perMinute", "sourceCidrs", "expiresInDays")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var in platformMachineTokenInput
	if in.Label, err = platformManagementString(fields, "label"); err != nil {
		platformManagementError(w, err)
		return
	}
	var perMinute, days int
	if json.Unmarshal(fields["routes"], &in.Routes) != nil || in.Routes == nil || json.Unmarshal(fields["sourceCidrs"], &in.SourceCIDRs) != nil || in.SourceCIDRs == nil ||
		json.Unmarshal(fields["perMinute"], &perMinute) != nil || json.Unmarshal(fields["expiresInDays"], &days) != nil {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	in.PerMinute, in.ExpiresInDay = int64(perMinute), days
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var created platformMachineCreated
	var nowMS int64
	err = s.withAppPlatformActorContext(r.Context(), session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var err error
		nowMS = control.nowMS
		created, err = createPlatformMachineToken(e, actor, control, installation, in)
		return err
	})
	s.writePlatformMachineCreated(w, installation, created, nowMS, err)
}

func (s *Server) apiPlatformMachineTokenRotate(w http.ResponseWriter, r *http.Request, user string) {
	installation, handle := r.PathValue("installation"), r.PathValue("handle")
	if !platformValidUUID(installation) || !appplatform.ValidMachineHandle(handle) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "graceDays", "expiresInDays")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var grace, days int
	if json.Unmarshal(fields["graceDays"], &grace) != nil || json.Unmarshal(fields["expiresInDays"], &days) != nil {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var created platformMachineCreated
	var nowMS int64
	err = s.withAppPlatformActorContext(r.Context(), session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var err error
		nowMS = control.nowMS
		created, err = rotatePlatformMachineToken(e, actor, control, installation, handle, grace, days)
		return err
	})
	s.writePlatformMachineCreated(w, installation, created, nowMS, err)
}

func (s *Server) writePlatformMachineCreated(w http.ResponseWriter, installation string, created platformMachineCreated, nowMS int64, err error) {
	if err != nil {
		platformMachineManagementError(w, err)
		return
	}
	// The one response that carries the secret: it is never cached, and nothing else returns it.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, platformMachineTokenCreatedView{Token: created.Token, TokenInfo: created.Row.view(nowMS), OpenAPIPath: "/machine/apps/" + installation + "/openapi.json"})
}

func (s *Server) apiPlatformMachineTokenRevoke(w http.ResponseWriter, r *http.Request, user string) {
	installation, handle := r.PathValue("installation"), r.PathValue("handle")
	if !platformValidUUID(installation) || !appplatform.ValidMachineHandle(handle) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	if _, err := platformManagementRead(w, r); err != nil {
		platformManagementError(w, err)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	err = s.withAppPlatformActorContext(r.Context(), session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		return revokePlatformMachineToken(e, actor, control, installation, handle)
	})
	if err != nil {
		platformMachineManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
