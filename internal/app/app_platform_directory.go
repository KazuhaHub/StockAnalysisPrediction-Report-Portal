package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformDirectoryPageSize = 32

var platformDirectoryAppID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type platformAppDirectoryItem struct {
	InstallationID string `json:"installationId"`
	ApplicationID  string `json:"applicationId"`
	State          string `json:"state"`
	Openable       bool   `json:"openable"`
	// Manageable is true when the viewer administers the installation and its active release declares an
	// admin page (doc 191). It is independent of Openable: an app administrator outside the audience
	// sees the installation as manageable but not openable.
	Manageable    bool                 `json:"manageable"`
	Display       *platformDisplayView `json:"display,omitempty"`
	PublisherName string               `json:"publisherName,omitempty"`
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
	chain, err := platformAccessChain(e, account)
	if err != nil {
		return zero, err
	}
	// Filter before LIMIT so another audience's records cannot consume this
	// reader's page or leak a cursor naming an unauthorized installation. The
	// filter is the OU policy: the installations whose key is open on the chain.
	open, err := platformOpenInstallations(e, chain)
	if err != nil {
		return zero, err
	}
	audience := map[string]bool{}
	for _, id := range open {
		audience[id] = true
	}
	// App administrators also see the installations whose admin page they manage (doc 191), listed
	// by the same keyset as the audience so the order and the cursor rules stay one.
	administered, err := platformAdministeredInstallations(e, account)
	if err != nil {
		return zero, err
	}
	for id, adminPage := range administered {
		if !audience[id] && adminPage {
			open = append(open, id)
		}
	}
	slices.Sort(open)
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE OF i"
	}
	var rows *sql.Rows
	if len(open) > 0 {
		args := []any{after}
		marks := make([]string, 0, len(open))
		for _, id := range open {
			marks = append(marks, "?")
			args = append(args, id)
		}
		rows, err = e.query(`SELECT i.id,i.app_id,i.publisher_id,i.desired_state,i.phase,i.controller_epoch,i.runtime_id FROM app_platform_installations i WHERE i.id>? AND i.desired_state<>'removed' AND i.id IN (`+strings.Join(marks, ",")+`) ORDER BY i.id LIMIT 33`+lock, args...)
		if err != nil {
			return zero, err
		}
	}
	type candidate struct {
		id, app, publisher, desired, phase, epoch string
		runtime                                   sql.NullString
	}
	candidates := []candidate{}
	for rows != nil && rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.app, &c.publisher, &c.desired, &c.phase, &c.epoch, &c.runtime); err != nil {
			rows.Close()
			return zero, err
		}
		candidates = append(candidates, c)
	}
	if rows != nil {
		err = rows.Err()
		rows.Close()
		if err != nil {
			return zero, err
		}
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
		if audience[c.id] {
			if err := platformAudienceGrant(e, c.id, chain); err != nil {
				return zero, err
			}
		}
		item := platformAppDirectoryItem{InstallationID: c.id, ApplicationID: c.app, State: "attention"}
		switch {
		case c.desired == "disabled":
			item.State = "disabled"
		case c.phase == "setup_required":
			item.State = "setup_required"
		case c.phase == "pending":
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
				item.Openable = audience[c.id]
			}
		}
		record, err := platformReadDisplayRecord(e, c.id)
		if err != nil {
			return zero, err
		}
		_, item.Manageable = administered[c.id]
		item.Manageable = item.Manageable && item.State == "installed" && record != nil
		records = append(records, record)
		result.Items = append(result.Items, item)
	}
	// Parse/signature work precedes the final clock sample. Trust and release rows
	// stay pinned, so only session/key time windows need a final lightweight check.
	for i, record := range records {
		if record == nil {
			continue
		}
		display, publisherName, valid := record.verified(trust, now, candidates[i].app, candidates[i].publisher)
		if !valid {
			result.Items[i].State = "attention"
			result.Items[i].Openable, result.Items[i].Manageable = false, false
		} else {
			result.Items[i].Display, result.Items[i].PublisherName = platformDisplayViewOf(display), publisherName
			result.Items[i].Manageable = result.Items[i].Manageable && record.adminPage
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
			result.Items[i].Display, result.Items[i].PublisherName = nil, ""
			result.Items[i].State = "attention"
			result.Items[i].Openable, result.Items[i].Manageable = false, false
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

// platformAdministeredInstallations maps the installations the account administers to whether their
// active release declares an admin page: every live installation for a Portal administrator, the
// delegations still bound to this account's creation for anyone else. This is only the prefilter that
// decides which installations the page lists; the listing verifies the signature of the ones it shows and
// takes the final admin_routes bit from the verified descriptor.
//
// The bit is memoised by release SHA-256 (platformAdminRoutesCache) rather than parsed per page: a
// release is immutable and its SHA-256 is the digest of its descriptor, so the bit can never change for a
// key. Reading it once per page outside the OU mutation lock was the alternative, but this runs inside the
// directory transaction and the reads must see the same activation the page lists; the cache keeps that
// and costs one indexed query per page plus a descriptor read only for a release not seen before.
func platformAdministeredInstallations(e migExec, account platformActorAccount) (map[string]bool, error) {
	query, args := `SELECT i.id,a.release_sha256 FROM app_platform_installations i JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active' WHERE i.desired_state<>'removed'`, []any{}
	if !can(account.role, PermManage) {
		query = `SELECT i.id,a.release_sha256 FROM app_platform_app_admins d JOIN app_platform_installations i ON i.id=d.installation_id JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active' WHERE d.username=? AND d.account_created=? AND i.desired_state<>'removed'`
		args = []any{account.username, account.created}
	}
	rows, err := e.query(query+` ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, release string }
	var found []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.release); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(found))
	for _, c := range found {
		adminPage, ok := platformAdminRoutesCacheLoad(c.release)
		if !ok {
			lock := ""
			if e.s.driver == "postgres" {
				lock = " FOR SHARE"
			}
			var descriptor []byte
			if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`+lock, c.release).Scan(&descriptor); err != nil {
				return nil, err
			}
			// An unparseable descriptor counts as no admin page; that is as immutable as the descriptor.
			if parsed, err := appplatform.ParseDescriptor(descriptor); err == nil {
				adminPage = len(parsed.Manifest.Presentation.AdminRoutes) > 0
			}
			platformAdminRoutesCacheStore(c.release, adminPage)
		}
		result[c.id] = adminPage
	}
	return result, nil
}

// platformAdminRoutesCacheMax bounds the cache. A Portal has a handful of releases; when the bound is
// reached the cache starts over, which costs a re-parse and never a wrong answer.
const platformAdminRoutesCacheMax = 256

var platformAdminRoutesCache = struct {
	sync.Mutex
	bits map[string]bool
}{bits: map[string]bool{}}

func platformAdminRoutesCacheLoad(release string) (adminPage, ok bool) {
	platformAdminRoutesCache.Lock()
	defer platformAdminRoutesCache.Unlock()
	adminPage, ok = platformAdminRoutesCache.bits[release]
	return
}

func platformAdminRoutesCacheStore(release string, adminPage bool) {
	platformAdminRoutesCache.Lock()
	defer platformAdminRoutesCache.Unlock()
	if len(platformAdminRoutesCache.bits) >= platformAdminRoutesCacheMax {
		platformAdminRoutesCache.bits = map[string]bool{}
	}
	platformAdminRoutesCache.bits[release] = adminPage
}

func platformAdminRoutesCacheLen() int {
	platformAdminRoutesCache.Lock()
	defer platformAdminRoutesCache.Unlock()
	return len(platformAdminRoutesCache.bits)
}
