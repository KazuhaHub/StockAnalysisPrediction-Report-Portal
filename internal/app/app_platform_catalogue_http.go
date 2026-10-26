package app

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformManagementPageSize = 32

type platformStagedSummary struct {
	Release       string                  `json:"release_sha256"`
	App           string                  `json:"app_id"`
	Publisher     string                  `json:"publisher_id"`
	Version       string                  `json:"version"`
	TrustRevision string                  `json:"trust_revision"`
	Display       *appplatform.AppDisplay `json:"display,omitempty"`
	Installable   bool                    `json:"installable"`
	Reason        string                  `json:"reason,omitempty"`
}

func platformManagementQuery(r *http.Request, keys ...string) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errPlatformManagementInput
	}
	for key, values := range q {
		known := false
		for _, allowed := range keys {
			known = known || key == allowed
		}
		if !known || len(values) != 1 || values[0] == "" {
			return nil, errPlatformManagementInput
		}
	}
	return q, nil
}

func (s *Server) apiPlatformStagedReleases(w http.ResponseWriter, r *http.Request, user string) {
	q, err := platformManagementQuery(r, "after")
	if err != nil || (q.Get("after") != "" && !platformValidSHA(q.Get("after"))) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	items := make([]platformStagedSummary, 0)
	next := ""
	err = s.withPlatformTrustActorContext(r.Context(), actor, func(e migExec, facts platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		// Only signed descriptor metadata: do not read/decompress archive content
		// to render a directory. Full artifact verification remains in review.
		rows, err := e.query(`SELECT sha256,app_id,publisher_id,verification_key_id,frontend_sha256,version,descriptor,signature,managed FROM app_platform_releases WHERE sha256>? ORDER BY sha256 LIMIT 33`, q.Get("after"))
		if err != nil {
			return err
		}
		next = ""
		records := make([]platformDisplayRecord, 0)
		for rows.Next() {
			var record platformDisplayRecord
			if err = rows.Scan(&record.release, &record.app, &record.publisher, &record.key, &record.frontend, &record.version, &record.raw, &record.signature, &record.managed); err != nil {
				rows.Close()
				return err
			}
			record.coherent = true
			records = append(records, record)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(records) > platformManagementPageSize {
			next = records[platformManagementPageSize-1].release
			records = records[:platformManagementPageSize]
		}
		// Contention retries rebuild output rather than append stale records.
		items = make([]platformStagedSummary, 0)
		accepted := make([]platformDisplayRecord, 0)
		final, err := platformRefreshActor(e, actor, facts, control)
		if err != nil {
			return err
		}
		for _, record := range records {
			display, valid := record.verified(trust, final.nowMS, record.app, record.publisher)
			if !valid {
				continue
			}
			accepted = append(accepted, record)
			verified, err := appplatform.VerifyPublisherDescriptor(record.raw, record.signature, trust, strconv.FormatInt(final.nowMS, 10))
			if err != nil {
				continue
			}
			item := platformStagedSummary{Release: record.release, App: record.app, Publisher: record.publisher, Version: record.version, TrustRevision: verified.PublisherRevision, Display: display, Installable: true}
			if platformInstallRequirements(verified.Descriptor.Manifest) != nil || verified.Descriptor.Manifest.Backend != nil || verified.Descriptor.Manifest.Data.WritesOnStartup {
				item.Installable = false
				item.Reason = "requirements_unavailable"
			}
			if err = platformNoPriorInstall(e, record.app); err != nil {
				if !errors.Is(err, errAppPlatformOperationConflict) {
					return err
				}
				item.Installable = false
				item.Reason = "already_installed"
			}
			items = append(items, item)
		}
		final, err = platformRefreshActor(e, actor, facts, final)
		if err != nil {
			return err
		}
		for _, record := range accepted {
			if !record.currentKeyTime(trust, final.nowMS) {
				return errAppPlatformTrustConflict
			}
		}
		return nil
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items, "next_cursor": next})
}

type platformSubjectSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) apiPlatformSubjects(w http.ResponseWriter, r *http.Request, user string) {
	q, err := platformManagementQuery(r, "kind", "after")
	kind, after := q.Get("kind"), q.Get("after")
	if err != nil || (kind != "user" && kind != "ou") || len(after) > 320 || !utf8.ValidString(after) || strings.ContainsAny(after, "\x00\r\n") || (kind == "ou" && after != "" && !platformPositiveCounter(after)) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	items := make([]platformSubjectSummary, 0)
	next := ""
	err = s.withPlatformTrustActorContext(r.Context(), actor, func(e migExec, facts platformActorFacts, control platformActorControl, _ appplatform.PublisherTrust) error {
		query := `SELECT username,COALESCE(NULLIF(display_name,''),username) FROM users WHERE COALESCE(active,1)=1 AND username>? ORDER BY username LIMIT 33`
		var cursor any = after
		if kind == "ou" {
			query = `SELECT id,name FROM user_groups WHERE id>? ORDER BY id LIMIT 33`
			cursor = int64(0)
			if after != "" {
				cursor, _ = strconv.ParseInt(after, 10, 64)
			}
		}
		rows, err := e.query(query, cursor)
		if err != nil {
			return err
		}
		items = make([]platformSubjectSummary, 0)
		for rows.Next() {
			var item platformSubjectSummary
			if err = rows.Scan(&item.ID, &item.Name); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		next = ""
		if len(items) > platformManagementPageSize {
			next = items[platformManagementPageSize-1].ID
			items = items[:platformManagementPageSize]
		}
		_, err = platformRefreshActor(e, actor, facts, control)
		return err
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items, "next_cursor": next})
}
