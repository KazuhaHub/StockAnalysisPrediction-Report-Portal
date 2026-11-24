package app

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The read-only operation history of the apps console. It lists rows of app_platform_operations, with the
// versions of the plan's source and target release, so the console can show what ran, when, how long it took and
// why it failed. It adds no state: the progress read (GET operations/{id}) stays the one view of a single run.

const (
	platformOperationListDefault = 20
	platformOperationListMax     = 50
	// platformOperationLatestMax bounds the one-per-installation listing; a console with more installations than
	// this is far past what the apps page renders.
	platformOperationLatestMax = 500
)

// platformOperationItem is one operation as the history shows it. Times are decimal milliseconds, as everywhere in
// the management API. Revision is the operation's state revision, which the page needs to place a failure on a step.
type platformOperationItem struct {
	OperationID    string `json:"operation_id"`
	InstallationID string `json:"installation_id"`
	Action         string `json:"action"`
	Stage          string `json:"stage"`
	Managed        bool   `json:"managed"`
	Revision       string `json:"revision"`
	Terminal       bool   `json:"terminal"`
	ErrorCode      string `json:"error_code,omitempty"`
	CreatedMS      string `json:"created_ms"`
	UpdatedMS      string `json:"updated_ms"`
	CompletedMS    string `json:"completed_ms,omitempty"`
	FromVersion    string `json:"from_version,omitempty"`
	ToVersion      string `json:"to_version,omitempty"`
}

const platformOperationListSelect = `SELECT o.id,o.installation_id,o.action,o.managed,o.state_revision,o.stage,o.error_code,o.created_ms,o.updated_ms,o.completed_ms,sr.version,tr.version
	FROM app_platform_operations o
	JOIN app_platform_review_plans p ON p.id=o.review_plan_id
	LEFT JOIN app_platform_releases sr ON sr.sha256=p.source_release_sha256
	LEFT JOIN app_platform_releases tr ON tr.sha256=p.target_release_sha256`

// platformOperationListQuery reads the query string. `installation` (a UUID) lists that installation's newest
// operations and `limit` bounds them; `latest=1` lists the newest operation of every installation. Anything else is
// refused, so a typo never widens a read.
func platformOperationListQuery(r *http.Request) (installation string, limit int, latest bool, err error) {
	limit = platformOperationListDefault
	values := r.URL.Query()
	for key, list := range values {
		if len(list) != 1 || (key != "installation" && key != "limit" && key != "latest") {
			return "", 0, false, errPlatformManagementInput
		}
	}
	if v := values.Get("latest"); v != "" {
		if v != "1" {
			return "", 0, false, errPlatformManagementInput
		}
		latest = true
	}
	if v := values.Get("installation"); v != "" {
		if !platformValidUUID(v) {
			return "", 0, false, errPlatformManagementInput
		}
		installation = v
	}
	if v := values.Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 || n > platformOperationListMax || strconv.Itoa(n) != v {
			return "", 0, false, errPlatformManagementInput
		}
		limit = n
	}
	if latest == (installation != "") {
		// Exactly one selection: an installation's history, or the latest of every installation.
		return "", 0, false, errPlatformManagementInput
	}
	return installation, limit, latest, nil
}

func (s *Server) apiPlatformOperationList(w http.ResponseWriter, r *http.Request, user string) {
	installation, limit, latest, err := platformOperationListQuery(r)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	items := []platformOperationItem{}
	err = s.withAppPlatformActorRead(session, func(e migExec, _ platformActorFacts, _ platformActorControl) error {
		var query string
		var args []any
		if latest {
			query = platformOperationListSelect + ` WHERE o.id=(SELECT o2.id FROM app_platform_operations o2 WHERE o2.installation_id=o.installation_id ORDER BY o2.created_ms DESC,o2.id DESC LIMIT 1) ORDER BY o.created_ms DESC,o.id DESC LIMIT ?`
			args = []any{platformOperationLatestMax}
		} else {
			var exists int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_installations WHERE id=?`, installation).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return errPlatformInstallationMissing
			}
			query = platformOperationListSelect + ` WHERE o.installation_id=? ORDER BY o.created_ms DESC,o.id DESC LIMIT ?`
			args = []any{installation, limit}
		}
		rows, err := e.query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item platformOperationItem
			var managed int
			var revision, created, updated int64
			var code sql.NullString
			var completed sql.NullInt64
			var from, to sql.NullString
			if err := rows.Scan(&item.OperationID, &item.InstallationID, &item.Action, &managed, &revision, &item.Stage, &code, &created, &updated, &completed, &from, &to); err != nil {
				return err
			}
			item.Managed = managed == 1
			item.Revision = strconv.FormatInt(revision, 10)
			item.Terminal = item.Stage == string(appplatform.StageComplete) || item.Stage == string(appplatform.StageFailed) || item.Stage == string(appplatform.StageRecoveryRequired)
			if code.Valid && code.String != "" {
				item.ErrorCode = platformPublicOperationErrorCode(code.String)
			}
			item.CreatedMS = strconv.FormatInt(created, 10)
			item.UpdatedMS = strconv.FormatInt(updated, 10)
			if completed.Valid {
				item.CompletedMS = strconv.FormatInt(completed.Int64, 10)
			}
			item.FromVersion, item.ToVersion = from.String, to.String
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}
