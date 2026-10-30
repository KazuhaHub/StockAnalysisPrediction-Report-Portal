package app

import (
	"database/sql"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// v1ExtensionPrincipals is an app-bound, administrator-only directory. It exposes
// opaque grant identities and labels, with no account credentials or profile data.
func (s *Server) v1ExtensionPrincipals(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	deny := func() { v1err(w, 403, "extension_forbidden", "current administrator permission is required") }
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || s.appTok == nil {
		deny()
		return
	}
	entry, ok := s.appTok.extensionEntry(raw, time.Now())
	if !ok {
		// Unknown, expired or revoked: the 401 every /api/v1 route answers, which tells the host page
		// to mint a new token. A live token that is not allowed stays a 403 (ADR 0048).
		v1err(w, http.StatusUnauthorized, "unauthorized", "the app token is missing, expired or revoked: mint a new one")
		return
	}
	if entry.appID != r.PathValue("id") || !entry.scopes["extension.manage"] || !entry.accountGenerationKnown {
		deny()
		return
	}
	app, ok := s.st.GetApp(entry.appID)
	if !ok || !slices.Contains(app.Scopes, "extension.manage") {
		deny()
		return
	}
	c, err := s.st.extensionConnection(entry.appID)
	if err != nil || !c.Enabled || c.InstanceID != entry.backendInstance {
		deny()
		return
	}
	key, err := s.extensionSecret(c)
	if err != nil {
		deny()
		return
	}
	issuer, err := s.st.extensionIssuer()
	if err != nil {
		deny()
		return
	}
	b := extensionIdentityBinding{Issuer: issuer, AppID: c.AppID, InstanceID: c.InstanceID, Key: key.IdentitySeed}
	viewer, err := s.st.extensionViewerIdentity(r.Context(), entry, b, time.Now(), s.panelLocation())
	if err != nil || viewer.Role != "admin" {
		deny()
		return
	}
	holds, err := s.mustEnroll(entry.username)
	if err != nil || holds {
		deny()
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "ou" && kind != "user" {
		v1err(w, 400, "invalid_param", "kind must be ou or user")
		return
	}
	offset := 0
	if value := r.URL.Query().Get("offset"); value != "" {
		offset, err = strconv.Atoi(value)
		if err != nil || offset < 0 || offset > 1000000 {
			v1err(w, 400, "invalid_param", "invalid directory offset")
			return
		}
	}
	opts := &sql.TxOptions{ReadOnly: true}
	if s.st.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.st.db.BeginTx(r.Context(), opts)
	if err != nil {
		deny()
		return
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	type principal struct {
		Principal string `json:"principal"`
		Label     string `json:"label"`
	}
	items := []principal{}
	if kind == "ou" {
		rows, err := e.query(`SELECT id,name,COALESCE(created_at,'') FROM user_groups ORDER BY id LIMIT 100 OFFSET ?`, offset)
		if err != nil {
			deny()
			return
		}
		for rows.Next() {
			var id int64
			var name, created string
			if rows.Scan(&id, &name, &created) != nil || created == "" {
				rows.Close()
				deny()
				return
			}
			items = append(items, principal{"ou:" + extensionOpaqueIdentity(b, "ou", strconv.FormatInt(id, 10), created), name})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			deny()
			return
		}
	} else {
		rows, err := e.query(`SELECT username,COALESCE(created_at,'') FROM users WHERE COALESCE(active,1)=1 ORDER BY username LIMIT 100 OFFSET ?`, offset)
		if err != nil {
			deny()
			return
		}
		type account struct{ name, created string }
		accounts := []account{}
		for rows.Next() {
			var a account
			if rows.Scan(&a.name, &a.created) != nil {
				rows.Close()
				deny()
				return
			}
			accounts = append(accounts, a)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			deny()
			return
		}
		for _, a := range accounts {
			generation, err := notificationAccountGeneration(e, a.name)
			if err != nil {
				deny()
				return
			}
			items = append(items, principal{"user:" + extensionOpaqueIdentity(b, "account", a.name, a.created, strconv.FormatInt(generation, 10)), a.name})
		}
	}
	if err := tx.Commit(); err != nil {
		deny()
		return
	}
	writeJSON(w, map[string]any{"ok": true, "items": items, "count": len(items), "offset": offset, "limit": 100})
}
