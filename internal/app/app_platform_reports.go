package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const maxPlatformReportRequestBytes = 16 << 10

type platformReportBinding struct {
	Generation     string `json:"generation"`
	PolicyRevision string `json:"policyRevision"`
	ReleaseSHA256  string `json:"releaseSha256"`
}

type platformReportQuery struct {
	Symbol  string `json:"symbol,omitempty"`
	Q       string `json:"q,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Subtype string `json:"subtype,omitempty"`
	Date    string `json:"date,omitempty"`
	Since   string `json:"since,omitempty"`
	Until   string `json:"until,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
}

type platformReportRequest struct {
	Binding platformReportBinding `json:"binding"`
	Query   *platformReportQuery  `json:"query,omitempty"`
	ID      int64                 `json:"id,omitempty"`
}

type platformReportAccess struct {
	launch  platformLaunchAccess
	account platformActorAccount
	scope   *ownerScope
}

func platformReportJSON(rep Rep, withBody bool) map[string]any {
	result := map[string]any{
		"id": rep.ID, "run_id": rep.RunID, "symbol": rep.Symbol,
		"name": firstNonEmpty(rep.Name, rep.Symbol), "date": rep.Date, "time": rep.Time,
		"kind": rep.Kind, "subtype": rep.RType, "title": rep.Title, "source": rep.Source,
		"version": rep.Version,
	}
	if withBody {
		result["body_md"] = rep.MD
	}
	return result
}

func platformReportsFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	writeJSON(w, map[string]any{"ok": false, "error": map[string]string{"code": "app_platform_reports_unavailable", "message": "reports unavailable"}})
}

func decodePlatformReportRequest(r *http.Request) (platformReportRequest, int64, bool) {
	var zero platformReportRequest
	if r.ContentLength > maxPlatformReportRequestBytes || !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return zero, 0, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxPlatformReportRequestBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxPlatformReportRequestBytes {
		return zero, 0, false
	}
	var request platformReportRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return zero, 0, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || !platformPositiveCounter(request.Binding.Generation) || !platformPositiveCounter(request.Binding.PolicyRevision) || !platformValidSHA(request.Binding.ReleaseSHA256) || (request.ID != 0 && request.Query != nil) || (request.ID <= 0 && request.Query == nil) {
		return zero, 0, false
	}
	if request.ID > 0 {
		return request, request.ID, true
	}
	query := request.Query
	for _, value := range []string{query.Symbol, query.Q, query.Kind, query.Subtype, query.Date, query.Since, query.Until} {
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return zero, 0, false
		}
	}
	for _, value := range []string{query.Date, query.Since, query.Until} {
		if value != "" && value != "today" && !validReportDate(value) {
			return zero, 0, false
		}
	}
	if query.Limit < 0 || query.Limit > 50 || query.Offset < 0 {
		return zero, 0, false
	}
	return request, 0, true
}

func (s *Server) preparePlatformReportAccess(ctx context.Context, session platformActorSession, installation string, expected platformReportBinding) (platformReportAccess, error) {
	var zero platformReportAccess
	var account platformActorAccount
	binding, err := s.withPlatformLaunchAccountReadContext(ctx, session, installation, func(e migExec, current platformLaunchAccess, _ appplatform.Manifest, actor platformActorAccount) error {
		if current.generation != expected.Generation || current.policyRevision != expected.PolicyRevision || current.releaseSHA != expected.ReleaseSHA256 {
			return errAppPlatformLaunchUnavailable
		}
		var granted int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='reports.read' AND granted=1 AND policy_revision=?`, installation, current.policyRevision).Scan(&granted); err != nil || granted != 1 {
			return errAppPlatformLaunchUnavailable
		}
		account = actor
		return nil
	})
	if err != nil {
		return zero, err
	}
	scope, err := s.st.viewerScopeSnapshot(account.username)
	if err != nil {
		return zero, err
	}
	return platformReportAccess{launch: binding, account: account, scope: scope}, nil
}

func samePlatformReportAccess(a, b platformReportAccess) bool {
	return a.launch == b.launch && a.account == b.account && reflect.DeepEqual(a.scope, b.scope)
}

func (s *Server) apiPlatformReports(w http.ResponseWriter, r *http.Request, user string) {
	if !platformHTTPPath(r) || !platformValidUUID(r.PathValue("installation")) {
		platformReportsFailure(w)
		return
	}
	session, release, ok := s.platformHTTPAdmission(w, r, user)
	if !ok {
		return
	}
	defer release()
	request, detailID, ok := decodePlatformReportRequest(r)
	if !ok {
		platformReportsFailure(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), platformDocumentTimeout)
	defer cancel()
	initial, err := s.preparePlatformReportAccess(ctx, session, r.PathValue("installation"), request.Binding)
	if err != nil {
		platformReportsFailure(w)
		return
	}
	if detailID > 0 {
		rep := s.v1ReportByPathID(strconv.FormatInt(detailID, 10), initial.scope)
		if rep == nil {
			http.NotFound(w, r)
			return
		}
		final, err := s.preparePlatformReportAccess(ctx, session, r.PathValue("installation"), request.Binding)
		if err != nil || !samePlatformReportAccess(initial, final) || ctx.Err() != nil {
			platformReportsFailure(w)
			return
		}
		s.recordV1Read(r, *rep)
		writeJSON(w, map[string]any{"ok": true, "report": platformReportJSON(*rep, true)})
		return
	}
	query := *request.Query
	since, until := query.Since, query.Until
	if query.Date != "" {
		date := query.Date
		if date == "today" {
			date = s.panelToday()
		}
		since, until = date, date
	}
	limit := query.Limit
	if limit == 0 {
		limit = 20
	}
	reports, total, err := s.st.QueryReports(ReportQuery{Symbol: query.Symbol, Q: query.Q, Kind: query.Kind, RType: query.Subtype, Since: since, Until: until, Limit: limit, Offset: query.Offset}, initial.scope)
	if err != nil {
		platformReportsFailure(w)
		return
	}
	final, err := s.preparePlatformReportAccess(ctx, session, r.PathValue("installation"), request.Binding)
	if err != nil || !samePlatformReportAccess(initial, final) || ctx.Err() != nil {
		platformReportsFailure(w)
		return
	}
	items := make([]map[string]any, 0, len(reports))
	for _, rep := range reports {
		items = append(items, platformReportJSON(rep, false))
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(items), "total": total, "offset": query.Offset, "limit": limit, "items": items})
}
