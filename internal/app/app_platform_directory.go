package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const platformDirectoryPageSize = 32

var platformDirectoryAppID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type platformAppDirectoryItem struct {
	InstallationID string                  `json:"installationId"`
	ApplicationID  string                  `json:"applicationId"`
	State          string                  `json:"state"`
	Openable       bool                    `json:"openable"`
	Display        *appplatform.AppDisplay `json:"display,omitempty"`
}
type platformAppDirectory struct {
	Items      []platformAppDirectoryItem `json:"items"`
	NextCursor string                     `json:"nextCursor"`
}

// Directory status projects control records, not physical readiness or a launch
// permit. The actual launch/document route must still authorize and verify bytes.
func (s *Server) listPlatformApps(ctx context.Context, session platformActorSession, after string) (platformAppDirectory, error) {
	var zero platformAppDirectory
	if after != "" && !platformValidUUID(after) {
		return zero, errAppPlatformLaunchUnavailable
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	account, err := platformReadSessionAccount(e, session, now, true, false)
	if err != nil {
		return zero, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	chain, err := platformAudienceChain(e, account)
	if err != nil {
		return zero, err
	}
	// Filter before LIMIT so another audience's records cannot consume this
	// reader's page or leak a cursor naming an unauthorized installation.
	principals := []string{`(g.principal_type='user' AND g.username=? AND g.account_created=? AND g.account_generation=?)`}
	args := []any{after, account.username, account.created, account.generation}
	for _, group := range chain {
		principals = append(principals, `(g.principal_type='ou' AND g.group_id=? AND g.group_created=?)`)
		args = append(args, group.id, group.created)
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE OF i"
	}
	rows, err := e.query(`SELECT i.id,i.app_id,i.publisher_id,i.desired_state,i.phase,i.controller_epoch,i.runtime_id FROM app_platform_installations i WHERE i.id>? AND i.desired_state<>'removed' AND EXISTS(SELECT 1 FROM app_platform_access_grants g WHERE g.installation_id=i.id AND (`+strings.Join(principals, " OR ")+`)) ORDER BY i.id LIMIT 33`+lock, args...)
	if err != nil {
		return zero, err
	}
	type candidate struct {
		id, app, publisher, desired, phase, epoch string
		runtime                                   sql.NullString
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.app, &c.publisher, &c.desired, &c.phase, &c.epoch, &c.runtime); err != nil {
			rows.Close()
			return zero, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	var epoch, paused sql.NullString
	err = e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch)
	if err != nil && err != sql.ErrNoRows {
		return zero, err
	}
	err = e.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).Scan(&paused)
	if err != nil && err != sql.ErrNoRows {
		return zero, err
	}
	result := platformAppDirectory{Items: []platformAppDirectoryItem{}}
	records := []*platformDisplayRecord{}
	for i, c := range candidates {
		if i == platformDirectoryPageSize {
			result.NextCursor = result.Items[len(result.Items)-1].InstallationID
			break
		}
		if !platformValidUUID(c.id) || !platformDirectoryAppID.MatchString(c.app) {
			return zero, errAppPlatformLaunchUnavailable
		}
		if err := platformAudienceGrant(e, account, c.id, chain); err != nil {
			return zero, err
		}
		item := platformAppDirectoryItem{InstallationID: c.id, ApplicationID: c.app, State: "attention"}
		switch {
		case c.desired == "disabled":
			item.State = "disabled"
		case c.phase == "pending" || c.phase == "setup_required":
			item.State = "pending"
		case c.desired == "enabled" && c.phase == "ready" && epoch.Valid && platformValidUUID(epoch.String) && c.epoch == epoch.String && paused.Valid && paused.String == "0":
			eligible := !c.runtime.Valid
			if c.runtime.Valid {
				var err error
				eligible, err = platformWritableLaunchEligible(e, c.id, c.runtime.String, epoch.String)
				if err != nil {
					return zero, err
				}
			}
			if eligible {
				item.State = "installed"
				item.Openable = true
			}
		}
		record, err := platformReadDisplayRecord(e, c.id)
		if err != nil {
			return zero, err
		}
		records = append(records, record)
		result.Items = append(result.Items, item)
	}
	// Parse/signature work precedes the final clock sample. Trust and release rows
	// stay pinned, so only session/key time windows need a final lightweight check.
	for i, record := range records {
		if record == nil {
			continue
		}
		display, valid := record.verified(trust, now, candidates[i].app, candidates[i].publisher)
		if !valid {
			result.Items[i].State = "attention"
			result.Items[i].Openable = false
		} else {
			result.Items[i].Display = display
		}
	}
	now, err = platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	current, err := platformReadSessionAccount(e, session, now, true, false)
	if err != nil || current != account {
		return zero, errAppPlatformLaunchUnavailable
	}
	for i, record := range records {
		if record != nil && !record.currentKeyTime(trust, now) {
			result.Items[i].Display = nil
			result.Items[i].State = "attention"
			result.Items[i].Openable = false
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
func (s *Server) apiPlatformApps(w http.ResponseWriter, r *http.Request, user string) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) > 1 || r.URL.EscapedPath() != r.URL.Path || r.URL.ForceQuery && r.URL.RawQuery == "" {
		platformDocumentFailure(w)
		return
	}
	after := ""
	if len(query) != 0 {
		values, ok := query["after"]
		if !ok || len(values) != 1 || !platformValidUUID(values[0]) {
			platformDocumentFailure(w)
			return
		}
		after = values[0]
	}
	session, release, ok := s.platformHTTPAdmission(w, r, user)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), platformDocumentTimeout)
	defer cancel()
	result, err := s.listPlatformApps(ctx, session, after)
	if err != nil || ctx.Err() != nil {
		platformDocumentFailure(w)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(result)
}
