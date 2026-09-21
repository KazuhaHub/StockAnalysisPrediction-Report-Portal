package app

import "net/http"

// Route registration, in one table.
//
// The portal has one ServeMux and something over two hundred registrations, and the question that
// matters about them is not visible in any one line: WHICH of them accept a cookie session. That
// question is what a policy that has to hold every reader somewhere (the forced-enrolment gate, and
// anything like it) has to answer completely — miss one route and the policy has a hole, and a hole
// is not something a reader of a 200-line registration block can be asked to check.
//
// So registration goes through a helper per authentication class, and the helper records the class.
// The route table that produces is then the thing tests can be written against — including, in a
// test, the assertion that nothing registers a route any other way.

// authClass is what a route accepts. It is deliberately about the CREDENTIAL rather than about the
// handler: two routes with the same class may still wrap it differently (the HTML pages redirect to
// the login form, the JSON ones answer 401), and that difference is not what the table is for.
type authClass int

const (
	// authPublic accepts no credential at all.
	authPublic authClass = iota
	// authSession requires one.
	authSession
	// authSessionOptional uses one when it is presented and works without it: the route cannot be
	// gated, because it is part of signing in or out.
	authSessionOptional
	// authAdmin / authPerm are a session plus a role check.
	authAdmin
	authPerm
	// authBearer is the machine surface: a token opens it, a session does not.
	authBearer
	// authSessionOrBearer is either one (canQuery) — a session IS accepted, so it is gated when a
	// session is what the caller used.
	authSessionOrBearer
)

// routeRow is one registration.
type routeRow struct {
	pattern string
	class   authClass
	perm    string // for authPerm
}

// route records the registration and hands it to the mux. Every registration in the server goes
// through here, and routes_test.go asserts that by reading this package's source.
func (s *Server) route(mux *http.ServeMux, pattern string, class authClass, perm string, h http.HandlerFunc) {
	s.routeTable = append(s.routeTable, routeRow{pattern: pattern, class: class, perm: perm})
	mux.HandleFunc(pattern, h)
}

func (s *Server) public(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	s.route(mux, pattern, authPublic, "", h)
}

// session is the JSON session class: 401 rather than a redirect, because the caller is the SPA.
func (s *Server) session(mux *http.ServeMux, pattern string, h handler) {
	s.route(mux, pattern, authSession, "", s.requireUserJSON(h))
}

// sessionHTML is the same class for the server-rendered report pages, which send a browser to the
// login form rather than answering a status code it would display.
func (s *Server) sessionHTML(mux *http.ServeMux, pattern string, h handler) {
	s.route(mux, pattern, authSession, "", s.requireUser(h))
}

func (s *Server) admin(mux *http.ServeMux, pattern string, h handler) {
	s.route(mux, pattern, authAdmin, "", s.requireAdminJSON(h))
}

func (s *Server) perm(mux *http.ServeMux, pattern, perm string, h handler) {
	s.route(mux, pattern, authPerm, perm, s.requirePermJSON(perm, h))
}

// sessionOptional is for the routes that are part of signing in or out: they read a session when one
// is there, must answer without one, and therefore cannot be gated.
func (s *Server) sessionOptional(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	s.route(mux, pattern, authSessionOptional, "", h)
}

func (s *Server) sessionOrBearer(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	s.route(mux, pattern, authSessionOrBearer, "", h)
}

// bearer is the machine surface. The per-source request ceiling is applied here rather than at each
// registration, for the same reason the classes are: a ceiling with a hole in it is a ceiling an
// abusive caller uses the hole in.
func (s *Server) bearer(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	s.route(mux, pattern, authBearer, "", s.rateLimitV1(h))
}

// sessionRoutes is every pattern a cookie session opens, which is the set a whole-portal policy has
// to cover.
func (s *Server) sessionRoutes() []routeRow {
	var out []routeRow
	for _, row := range s.routeTable {
		switch row.class {
		case authSession, authAdmin, authPerm, authSessionOrBearer:
			out = append(out, row)
		}
	}
	return out
}

// wireRoutes registers every route there is, and records the credential each one accepts. It is one
// function so that a test can install the real registration on a mux of its own and then ask the
// table what any given pattern accepts — which is a question no reading of the call sites answers
// reliably, and the question a whole-portal policy is built on.
func (s *Server) wireRoutes(mux *http.ServeMux) {
	s.public(mux, "GET /healthz", s.handleHealthz)
	// Session-gated, not public: build identity (version/commit) is only shown in the signed-in
	// app footer, so an anonymous scanner can't fingerprint the build against known CVEs.
	s.session(mux, "GET /api/version", s.handleVersion)
	// The deployed build's committed release note, for the update dialog. Session-gated like
	// /api/version: nothing in it is secret, but the endpoint is app infrastructure, not a public
	// path to probe.
	s.session(mux, "GET /api/release-notes", s.handleReleaseNotes)
	s.session(mux, "GET /api/release-history", s.handleReleaseHistory)
	s.public(mux, "GET /api/site", s.apiSite) // public: brand title/logo for login + browser chrome
	// The announcement feed is NOT on /api/site, and the wrapper is the reason (ADR 0025): a row can
	// be addressed to one OU, and who is being told what is itself disclosure. Polled, so it answers
	// with a 304 while nothing changes.
	s.session(mux, "GET /api/announcements", s.apiAnnouncements)
	s.public(mux, "GET /api/openapi.json", s.apiOpenAPI) // public: OpenAPI 3.1 spec for the v1 machine API
	s.public(mux, "GET /manifest.webmanifest", s.pwaManifest)
	s.public(mux, "GET /pwa-icon", s.pwaIcon)

	// The machine API is /api/v1/* (apiv1.go) — the pre-v1 routes that used to sit here were
	// retired once Dify's tool schemas spoke only v1. This one is the remainder, and it is not
	// a machine route at all: the omnibox's autocomplete, called from the browser.
	// SSO (ADR 0023). Unauthenticated by necessity — they ARE the login. Each 404s when the
	// addressed provider is missing or disabled, so a portal with SSO off is indistinguishable
	// from one that never had the feature.
	s.public(mux, "GET /api/sso/providers", s.apiSSOProviders)
	// Public-form captcha and self-service registration.
	s.public(mux, "GET /api/captcha", s.apiCaptcha)
	s.public(mux, "GET /api/register/config", s.apiRegisterConfig)
	s.public(mux, "POST /api/register", s.apiRegister)
	s.public(mux, "POST /api/register/verify", s.apiRegisterVerify)
	s.public(mux, "POST /api/login/2fa", s.apiLoginTOTP) // second leg of a password login
	s.session(mux, "POST /api/me/password", s.apiChangePassword)
	s.session(mux, "POST /api/me/2fa/setup", s.apiTOTPSetup)
	s.session(mux, "POST /api/me/2fa/enable", s.apiTOTPEnable)
	s.session(mux, "POST /api/me/2fa/disable", s.apiTOTPDisable)
	s.public(mux, "POST /api/login/passkey/begin", s.apiPasskeyLoginBegin)
	s.public(mux, "POST /api/login/passkey/finish", s.apiPasskeyLoginFinish)
	s.session(mux, "GET /api/me/passkeys", s.apiPasskeyList)
	s.session(mux, "POST /api/me/passkeys/register/begin", s.apiPasskeyRegisterBegin)
	s.session(mux, "POST /api/me/passkeys/register/finish", s.apiPasskeyRegisterFinish)
	s.session(mux, "DELETE /api/me/passkeys/{id}", s.apiPasskeyDelete)
	s.session(mux, "GET /api/account/stepup/policy", s.apiStepUpPolicy)
	s.public(mux, "GET /api/auth/oidc/{slug}/start", s.oidcStart)
	s.public(mux, "GET /api/auth/oidc/{slug}/callback", s.oidcCallback)
	s.public(mux, "GET /api/auth/saml/{slug}/start", s.samlStart)
	s.public(mux, "POST /api/auth/saml/{slug}/acs", s.samlACS)
	s.public(mux, "GET /api/auth/saml/{slug}/metadata", s.samlMetadata)
	s.sessionOrBearer(mux, "GET /api/symbols", s.apiSymbols) // stock list / autocomplete (omnibox)

	// ---- Dify machine API v1: clean contract (JSON errors, portal-derived identity, envelopes) ----
	// v1 registers a machine-API route behind the per-source request ceiling (limits.go). Every
	// route goes through it, including the ones that are cheap to serve: a ceiling with a hole in
	// it is a ceiling an abusive caller uses the hole in. It is off until an admin sets one.
	v1 := func(pattern string, h http.HandlerFunc) { s.bearer(mux, pattern, h) }
	v1("POST /api/v1/reports", s.v1Ingest)
	v1("GET /api/v1/reports", s.v1QueryReports)
	v1("GET /api/v1/reports/manifest", s.v1Manifest) // more specific than {id}, matched first
	v1("GET /api/v1/reports/{id}", s.v1GetReport)
	v1("DELETE /api/v1/reports/{id}", s.v1DeleteReport)
	v1("GET /api/v1/runs", s.v1Runs)
	v1("GET /api/v1/symbols", s.v1Symbols)
	v1("GET /api/v1/tracking", s.v1Tracking)
	v1("PATCH /api/v1/tracking/{id}", s.v1TrackingUpdate)
	v1("GET /api/v1/now", s.v1Now) // authoritative clock: UTC instant + panel-tz civil date

	// ---- Browser (React SPA) API: signed-cookie session auth ----
	// A session route like the rest, registered without the shared wrapper because it resolves the
	// session itself: it answers 401 without one (verified in routes_test.go), so the class — not the
	// wrapper — is what says a policy has to account for it.
	s.route(mux, "GET /api/me", authSession, "", s.apiMe)
	s.public(mux, "POST /api/login", s.apiLogin)
	s.sessionOptional(mux, "POST /api/logout", s.apiLogout)
	// Public password reset (no session).
	s.public(mux, "POST /api/password/forgot", s.apiForgotPassword)
	s.public(mux, "POST /api/password/reset", s.apiResetPassword)
	s.session(mux, "GET /api/home", s.apiHome)
	s.session(mux, "GET /api/stock/{symbol}", s.apiStock)
	s.session(mux, "GET /api/quote/{symbol}", s.apiQuote) // one symbol: live quote + its series; cached and single-flighted (quote_cache.go)
	// Many symbols at once, for the home feed's cards: ONE upstream call for a page of codes, the
	// same cache as the line above, and per-symbol results so one bad code cannot blank the page.
	// Behind a session for the same reason /api/quote is — it makes this server fetch on the
	// caller's behalf — and behind the home_quotes switch, because it is the one quote request a
	// reader makes without asking for a quote.
	s.session(mux, "GET /api/quotes", s.apiQuotes)
	// Personal stock favorites (ADR 0033). Ownership comes only from the signed session; the path
	// carries the canonical market identity and never a username.
	s.session(mux, "GET /api/favorites", s.apiFavorites)
	s.session(mux, "PUT /api/favorites/order", s.apiFavoriteReorder)
	s.session(mux, "PUT /api/favorites/{market}/{symbol}", s.apiFavoriteAdd)
	s.session(mux, "DELETE /api/favorites/{market}/{symbol}", s.apiFavoriteDelete)
	s.session(mux, "GET /api/run/{key}", s.apiRun)
	// The review queue (tracking items). Session-scoped, unlike /api/v1/tracking, which runs on an
	// ingest token that already has access to everything.
	s.session(mux, "GET /api/tracking", s.apiTracking)
	s.session(mux, "PATCH /api/tracking/{id}", s.apiTrackingUpdate)
	// Compare any two reports the caller may read (both ids are scoped).
	s.session(mux, "GET /api/reports/diff", s.apiReportDiff)
	s.session(mux, "GET /api/reports/comparable", s.apiComparableReports)
	// Hand-written reports (ADR 0026). PermEditReport, not PermManage: writing the commentary and
	// administering the portal are different jobs, and 编辑员 holds only the first. Every write is
	// additionally constrained to the manual version inside the store, so none of these can reach a
	// report that records what a workflow said.
	s.perm(mux, "GET /api/reports/editor", PermEditReport, s.apiReportEditor)
	s.perm(mux, "POST /api/reports", PermEditReport, s.apiReportCreate)
	s.perm(mux, "PUT /api/reports/{id}", PermEditReport, s.apiReportSave)
	s.perm(mux, "DELETE /api/reports/{id}", PermEditReport, s.apiReportDelete)
	// The edit history. Same permission and same parent-report authorization as the editor: a
	// revision carries no viewer rows of its own, so the report's read scope is the only scope there
	// is. Restore is a save, not a fourth write path — see report_revision_api.go.
	s.perm(mux, "GET /api/reports/{id}/revisions", PermEditReport, s.apiReportRevisions)
	s.perm(mux, "GET /api/reports/{id}/revisions/{rev}", PermEditReport, s.apiReportRevision)
	s.perm(mux, "POST /api/reports/{id}/revisions/{rev}/restore", PermEditReport, s.apiReportRevisionRestore)
	s.session(mux, "POST /api/mermaid-cache", s.apiMermaidCache)

	// ---- Admin API: session + admin ----
	s.admin(mux, "GET /api/admin/links", s.apiAdminLinks)
	s.admin(mux, "POST /api/admin/links", s.apiLinkAdd)
	s.admin(mux, "PUT /api/admin/links/{id}", s.apiLinkEdit)
	s.admin(mux, "DELETE /api/admin/links/{id}", s.apiLinkDelete)
	s.admin(mux, "POST /api/admin/links/layout", s.apiLinkLayout)
	// Site announcements (ADR 0025). PATCH is the list's two inline switches and only those: a
	// whole-row PUT to flip one would also write back a stale audience.
	s.admin(mux, "GET /api/admin/announcements", s.apiAdminAnnouncements)
	// "Preview as": an admin does not receive a targeted announcement either, so without this,
	// addressed-correctly and addressed-to-nobody look identical from the console.
	s.admin(mux, "GET /api/admin/announcements/preview", s.apiAnnouncementPreview)
	s.admin(mux, "POST /api/admin/announcements", s.apiAnnouncementAdd)
	s.admin(mux, "POST /api/admin/announcements/reorder", s.apiAnnouncementReorder)
	s.admin(mux, "PUT /api/admin/announcements/{id}", s.apiAnnouncementSave)
	s.admin(mux, "PATCH /api/admin/announcements/{id}", s.apiAnnouncementToggle)
	s.admin(mux, "DELETE /api/admin/announcements/{id}", s.apiAnnouncementDelete)
	s.admin(mux, "POST /api/admin/link-groups", s.apiLinkGroupAdd)
	s.admin(mux, "PUT /api/admin/link-groups/{id}", s.apiLinkGroupEdit)
	s.admin(mux, "DELETE /api/admin/link-groups/{id}", s.apiLinkGroupDelete)
	// Report versions (ADR 0024): the registry, who may read each version, and the reader-side
	// switcher over the forms of one report.
	s.admin(mux, "GET /api/admin/security", s.apiAdminSecurity)
	s.admin(mux, "POST /api/admin/security", s.apiAdminSecuritySave)
	s.admin(mux, "GET /api/admin/versions", s.apiAdminVersions)
	s.admin(mux, "POST /api/admin/versions", s.apiAdminVersionSave)
	s.admin(mux, "DELETE /api/admin/versions/{name}", s.apiAdminVersionDelete)
	s.session(mux, "GET /api/report/{id}/versions", s.apiReportVersions)
	s.admin(mux, "GET /api/admin/types", s.apiAdminTypes)
	s.admin(mux, "POST /api/admin/types/save", s.apiTypesSave)
	s.admin(mux, "POST /api/admin/types/add", s.apiTypesAdd)
	s.admin(mux, "POST /api/admin/types/reorder", s.apiTypesReorder)
	s.admin(mux, "POST /api/admin/types/recompute", s.apiTypesRecompute)
	s.admin(mux, "POST /api/admin/types/restore-defaults", s.apiTypesRestoreDefaults)
	s.admin(mux, "DELETE /api/admin/types/{name}", s.apiTypesDelete)
	s.admin(mux, "POST /api/admin/kind-colors", s.apiKindColorSave)
	s.admin(mux, "GET /api/admin/users", s.apiAdminUsers)
	s.admin(mux, "POST /api/admin/users", s.apiUserAdd)
	s.admin(mux, "POST /api/admin/users/bulk", s.apiUsersBulk)
	s.admin(mux, "PUT /api/admin/users/{name}", s.apiUserSave)
	s.admin(mux, "DELETE /api/admin/users/{name}", s.apiUserDelete)
	// Seeing and revoking the identity-provider binding on an account (ADR 0023). The store could do
	// both from the day SSO shipped; nothing could reach either until now.
	s.admin(mux, "GET /api/admin/users/{name}/identity", s.apiAdminUserIdentity)
	s.admin(mux, "DELETE /api/admin/users/{name}/identity", s.apiAdminUserUnlink)
	// Organizational user groups (labels; permissions still come from the role).
	s.admin(mux, "GET /api/admin/groups", s.apiAdminGroups)
	s.admin(mux, "POST /api/admin/groups", s.apiGroupAdd)
	s.admin(mux, "PUT /api/admin/groups/{id}", s.apiGroupSave)
	// Asked before the delete is offered: deleting an OU can leave an announcement addressed to
	// nobody, and DELETE refuses until the caller says what should happen to those (ADR 0025).
	s.admin(mux, "GET /api/admin/groups/{id}/announcements", s.apiGroupAnnouncements)
	s.admin(mux, "DELETE /api/admin/groups/{id}", s.apiGroupDelete)
	// Per-OU run allow-list matrix (ADR 0022 R3): which workflows an OU may run, on which surfaces.
	// SSO administration (ADR 0023). Admin-only; a secret is never returned by any of these.
	s.admin(mux, "GET /api/admin/sso/providers", s.apiAdminSSOProviders)
	s.admin(mux, "POST /api/admin/sso/providers", s.apiAdminSSOSave)
	s.admin(mux, "DELETE /api/admin/sso/providers/{id}", s.apiAdminSSODelete)
	s.admin(mux, "POST /api/admin/sso/providers/{slug}/metadata", s.apiAdminSSOFetchMetadata)
	s.admin(mux, "GET /api/admin/sso/providers/{slug}/last-seen", s.apiAdminSSOLastSeen)
	s.admin(mux, "POST /api/admin/sso/allow-private", s.apiAdminSSOAllowPrivate)
	s.admin(mux, "GET /api/admin/sso/rules", s.apiAdminSSORules)
	s.admin(mux, "PUT /api/admin/sso/rules", s.apiAdminSSORulesSave)
	s.admin(mux, "GET /api/admin/groups/{id}/targets", s.apiGroupTargets)
	s.admin(mux, "PUT /api/admin/groups/{id}/targets", s.apiGroupTargetsSave)
	s.admin(mux, "GET /api/admin/settings", s.apiAdminSettings)
	// The audit log. requirePermJSON, not requireAdminJSON: see apiAdminAudit.
	s.perm(mux, "GET /api/admin/audit", PermManage, s.apiAdminAudit)
	s.admin(mux, "POST /api/admin/settings", s.apiSettingsSave)
	s.admin(mux, "GET /api/admin/email", s.apiEmailGet)
	s.admin(mux, "POST /api/admin/email", s.apiEmailSave)
	s.admin(mux, "POST /api/admin/email/test", s.apiEmailTest)
	s.admin(mux, "POST /api/admin/site-asset", s.apiSiteAssetUpload)
	s.admin(mux, "GET /api/admin/tokens", s.apiAdminTokens)
	s.admin(mux, "POST /api/admin/tokens", s.apiTokenAdd)
	s.admin(mux, "DELETE /api/admin/tokens/{id}", s.apiTokenDelete)

	// ---- Batch-run feature (see docs/adr/0001-batch-run-engine.md) ----
	// Executor manifests + targets + config are admin-only (PermManage); running jobs is PermRunBatch.
	s.admin(mux, "GET /api/admin/batch/plugins", s.apiBatchPlugins)
	s.admin(mux, "POST /api/admin/batch/plugins/import", s.apiBatchPluginImport)
	s.admin(mux, "DELETE /api/admin/batch/plugins/{slug}", s.apiBatchPluginDelete)
	s.admin(mux, "GET /api/admin/batch/config", s.apiBatchConfigGet)
	s.admin(mux, "POST /api/admin/batch/config", s.apiBatchConfigSave)
	s.perm(mux, "GET /api/admin/batch/targets", PermRunBatch, s.apiBatchTargets)
	s.admin(mux, "POST /api/admin/batch/targets", s.apiBatchTargetAdd)
	s.admin(mux, "POST /api/admin/batch/targets/reorder", s.apiBatchTargetReorder)
	s.admin(mux, "PUT /api/admin/batch/targets/{id}/surfaces", s.apiBatchTargetSurfaces)
	// Uploading a file for a run is part of SUBMITTING one, not of configuring a target, so it
	// carries PermRunBatch like POST .../jobs — admin-only here would leave operators unable to
	// run any workflow that takes a file.
	s.perm(mux, "POST /api/admin/batch/targets/{id}/upload", PermRunBatch, s.apiBatchDifyFileUpload)
	// Dify-native config (docs/adr/0006-dify-native.md): probe a workflow by key, then save it.
	s.admin(mux, "POST /api/admin/batch/dify/probe", s.apiBatchDifyProbe)
	s.admin(mux, "POST /api/admin/batch/dify/refresh", s.apiBatchDifyRefresh)
	s.admin(mux, "POST /api/admin/batch/dify/refresh/apply", s.apiBatchDifyRefreshApply)
	s.admin(mux, "POST /api/admin/batch/dify/targets", s.apiBatchDifyTargetAdd)
	s.admin(mux, "GET /api/admin/batch/dify/targets/{id}", s.apiBatchDifyTargetGet)
	s.admin(mux, "PUT /api/admin/batch/dify/targets/{id}", s.apiBatchDifyTargetUpdate)
	s.admin(mux, "DELETE /api/admin/batch/targets/{id}", s.apiBatchTargetDelete)
	s.perm(mux, "GET /api/admin/batch/tickets", PermRunBatch, s.apiBatchTickets)
	s.perm(mux, "GET /api/admin/batch/run-quota", PermRunBatch, s.apiRunQuota) // daily run quota (ADR 0022 R2)
	s.perm(mux, "GET /api/admin/batch/queue", PermRunBatch, s.apiBatchQueue)
	s.perm(mux, "GET /api/admin/batch/jobs", PermRunBatch, s.apiBatchJobs)
	s.perm(mux, "POST /api/admin/batch/jobs", PermRunBatch, s.apiBatchJobCreate)
	s.perm(mux, "GET /api/admin/batch/jobs/{id}", PermRunBatch, s.apiBatchJobDetail)
	// Queue mutations are admin-only, except cancel — a non-admin may cancel their own
	// run (ownership is checked inside apiBatchJobCancel).
	s.admin(mux, "POST /api/admin/batch/jobs/clear-finished", s.apiBatchClearFinished)
	s.admin(mux, "DELETE /api/admin/batch/jobs/{id}", s.apiBatchJobDelete)
	s.perm(mux, "POST /api/admin/batch/jobs/{id}/cancel", PermRunBatch, s.apiBatchJobCancel)
	s.perm(mux, "POST /api/admin/batch/jobs/{id}/items/cancel", PermRunBatch, s.apiBatchItemsCancel)
	s.admin(mux, "POST /api/admin/batch/items/{id}/reconcile", s.apiBatchItemReconcile)
	s.admin(mux, "POST /api/admin/batch/jobs/{id}/retry", s.apiBatchJobRetry)
	s.admin(mux, "POST /api/admin/batch/jobs/{id}/priority", s.apiBatchJobReprioritize)
	s.admin(mux, "POST /api/admin/batch/jobs/{id}/schedule", s.apiBatchJobSchedule)
	// Preset low-peak scheduling windows (ADR 0014): the run form reads the list (PermRunBatch);
	// admins manage them.
	s.perm(mux, "GET /api/admin/batch/presets", PermRunBatch, s.apiRunPresets)
	s.admin(mux, "POST /api/admin/batch/presets", s.apiRunPresetCreate)
	s.admin(mux, "POST /api/admin/batch/presets/reorder", s.apiRunPresetReorder)
	s.admin(mux, "PUT /api/admin/batch/presets/{id}", s.apiRunPresetUpdate)
	s.admin(mux, "DELETE /api/admin/batch/presets/{id}", s.apiRunPresetDelete)

	// ---- Recurring tasks (scheduled tasks; docs/adr/0018-recurring-tasks.md) ----
	// PermRunBatch (operators schedule their own recurring runs, not admin-only); every handler
	// checks ownership in-line (a non-admin sees/edits only their own tasks, an admin all).
	s.perm(mux, "GET /api/admin/batch/recurring", PermRunBatch, s.apiRecurringList)
	s.perm(mux, "POST /api/admin/batch/recurring", PermRunBatch, s.apiRecurringCreate)
	s.perm(mux, "GET /api/admin/batch/recurring/{id}", PermRunBatch, s.apiRecurringDetail)
	s.perm(mux, "PUT /api/admin/batch/recurring/{id}", PermRunBatch, s.apiRecurringUpdate)
	s.perm(mux, "POST /api/admin/batch/recurring/{id}/enable", PermRunBatch, s.apiRecurringEnable)
	s.perm(mux, "POST /api/admin/batch/recurring/{id}/run", PermRunBatch, s.apiRecurringRunNow)
	s.perm(mux, "DELETE /api/admin/batch/recurring/{id}", PermRunBatch, s.apiRecurringDelete)

	// ---- Storage cleanup console (docs/adr/0017-storage-cleanup.md): admin-only (PermManage) ----
	s.admin(mux, "GET /api/admin/cleanup/config", s.apiCleanupConfigGet)
	s.admin(mux, "POST /api/admin/cleanup/config", s.apiCleanupConfigSave)
	s.admin(mux, "GET /api/admin/cleanup/usage", s.apiCleanupUsage)
	s.admin(mux, "POST /api/admin/cleanup/preview", s.apiCleanupPreview)
	s.admin(mux, "POST /api/admin/cleanup/run", s.apiCleanupRunNow)
	// The IP database: where it lives, what is loaded, and a button to fetch a new one.
	s.admin(mux, "GET /api/admin/geoip", s.apiGeoStatus)
	s.admin(mux, "POST /api/admin/geoip", s.apiGeoSave)
	s.admin(mux, "POST /api/admin/geoip/update", s.apiGeoUpdate)
	s.admin(mux, "GET /api/admin/cleanup/history", s.apiCleanupHistory)
	// ---- Market data sources (ADR 0028): per-source health, the failover order and the two TTLs.
	// The vendor URLs are deliberately NOT editable — a source is a parser, not a host
	// (quote_admin_api.go).
	s.admin(mux, "GET /api/admin/quote", s.apiAdminQuotes)
	s.admin(mux, "POST /api/admin/quote", s.apiAdminQuotesSave)
	s.admin(mux, "POST /api/admin/quote/cache/clear", s.apiAdminQuotesCacheClear)

	// ---- Interactive chat / assistant (docs/adr/0012-interactive-chat.md) ----
	// Cookie session, gated by PermRunBatch (a chat turn runs a Dify app). Conversations
	// are personal — each handler is scoped to the caller's own rows.
	s.perm(mux, "GET /api/chat/config", PermRunBatch, s.apiChatConfig)
	s.perm(mux, "GET /api/chat/targets", PermRunBatch, s.apiChatTargets)
	s.perm(mux, "GET /api/chat/targets/{id}/intro", PermRunBatch, s.apiChatTargetIntro)
	s.perm(mux, "GET /api/chat/conversations", PermRunBatch, s.apiChatConversations)
	s.perm(mux, "POST /api/chat/conversations", PermRunBatch, s.apiChatConversationCreate)
	s.perm(mux, "DELETE /api/chat/conversations/{id}", PermRunBatch, s.apiChatConversationDelete)
	s.perm(mux, "POST /api/chat/conversations/{id}/rename", PermRunBatch, s.apiChatConversationRename)
	s.perm(mux, "POST /api/chat/conversations/{id}/star", PermRunBatch, s.apiChatConversationStar)
	s.perm(mux, "GET /api/chat/conversations/{id}/messages", PermRunBatch, s.apiChatHistory)
	s.perm(mux, "POST /api/chat/conversations/{id}/messages", PermRunBatch, s.apiChatSend)
	s.perm(mux, "POST /api/chat/conversations/{id}/messages/stream", PermRunBatch, s.apiChatSendStream)
	s.perm(mux, "GET /api/chat/conversations/{id}/outcome", PermRunBatch, s.apiChatOutcome)
	s.perm(mux, "POST /api/chat/conversations/{id}/stop", PermRunBatch, s.apiChatStop) // owner stops their in-flight turn
	// Assistant admin: the concurrency ceiling, the live "who is chatting now" view, and read-only
	// oversight of any user's conversations + messages (ADR 0012).
	s.admin(mux, "GET /api/admin/chat/conversations", s.apiAdminChatConversations)
	s.admin(mux, "GET /api/admin/chat/conversations/{id}/messages", s.apiAdminChatHistory)
	s.admin(mux, "GET /api/admin/chat/live", s.apiAdminChatLive)
	s.admin(mux, "POST /api/admin/chat/stop/{id}", s.apiAdminChatStop) // admin stops any in-flight turn
	s.admin(mux, "POST /api/admin/chat/config", s.apiAdminChatConfigSave)

	// ---- Downloadable iframe apps (see docs/adr/0003-downloadable-apps.md) ----
	// List/open is any-user; install/uninstall is admin; assets are served publicly.
	s.session(mux, "GET /api/apps", s.apiApps)
	s.session(mux, "POST /api/apps/{id}/token", s.apiAppToken)
	s.admin(mux, "POST /api/admin/apps/install", s.apiAppInstall)
	s.admin(mux, "GET /api/admin/apps/market", s.apiAppMarket)
	s.admin(mux, "POST /api/admin/apps/market/install", s.apiAppMarketInstall)
	s.admin(mux, "POST /api/admin/apps/market/index", s.apiAppMarketIndexSave)
	s.admin(mux, "DELETE /api/admin/apps/{id}", s.apiAppDelete)
	s.public(mux, "GET /app-assets/{id}/{path...}", s.appAssets)
	s.public(mux, "GET /site-assets/{name}", s.siteAsset)

	// ---- Outbound event webhooks (extension point; see docs/adr/0002-extension-architecture.md) ----
	s.admin(mux, "GET /api/admin/webhooks", s.apiWebhooks)
	s.admin(mux, "POST /api/admin/webhooks", s.apiWebhookAdd)
	s.admin(mux, "DELETE /api/admin/webhooks/{id}", s.apiWebhookDelete)
	s.admin(mux, "POST /api/admin/webhooks/{id}/test", s.apiWebhookTest)

	// ---- Downloads: MD / PDF (cookie session) ----
	s.sessionHTML(mux, "GET /report/{id}/md", s.reportMD)
	s.sessionHTML(mux, "GET /report/{id}/pdf", s.reportPDF)
	s.sessionHTML(mux, "GET /report/day.zip", s.reportDayZip) // every report a stock has on one date, as a ZIP of .md + .pdf

	// ---- SPA: hand all other paths to React (deep links fall back to index.html) ----
	s.public(mux, "GET /", s.spaHandler())
}
