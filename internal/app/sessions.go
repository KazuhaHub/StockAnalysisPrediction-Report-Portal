package app

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type AccountSession struct {
	ID        string         `json:"id"`
	CreatedAt int64          `json:"created_at"`
	LastSeen  int64          `json:"last_seen"`
	ExpiresAt int64          `json:"expires_at"`
	IP        string         `json:"ip"`
	Client    *ClientSummary `json:"client,omitempty"`
	Method    string         `json:"method,omitempty"`
	Current   bool           `json:"current"`
}

func (s *Server) managedCookie(u User, id string, exp int64) string {
	msg := fmt.Sprintf("v2|%s|%d|%s|%d", u.Username, u.SessionRev, id, exp)
	return encodeSessionMessage(msg) + "." + s.hmac(msg)
}

// Called only after authentication (or on a cookie we just minted). The id itself
// cannot authenticate: the server requires the signed cookie and current revision.
func legacySessionID(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return "legacy-" + hex.EncodeToString(sum[:])
}

func sessionID(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(c.Value, ".", 2)[0])
	if err != nil {
		return ""
	}
	if !strings.HasPrefix(string(raw), "v2|") {
		return legacySessionID(c.Value)
	}
	msg := string(raw)
	end := strings.LastIndex(msg, "|")
	start := strings.LastIndex(msg[:end], "|")
	if start < 0 {
		return ""
	}
	return msg[start+1 : end]
}

func setRequestSession(r *http.Request, value string) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != cookieName {
			r.AddCookie(c)
		}
	}
	r.AddCookie(&http.Cookie{Name: cookieName, Value: value})
}

func (s *Server) writeSessionCookie(w http.ResponseWriter, r *http.Request, value string, exp int64) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true,
		Secure: requestIsHTTPS(r, s.trustedNets), SameSite: http.SameSiteLaxMode,
		MaxAge: max(1, int(exp-time.Now().Unix()))})
	setRequestSession(r, value)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: requestIsHTTPS(r, s.trustedNets), SameSite: http.SameSiteLaxMode})
}

func (s *Server) newManagedSession(w http.ResponseWriter, r *http.Request, u User, exp int64, id string) bool {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		jsonError(w, 500, "could not create session")
		return false
	}
	if id == "" {
		id = hex.EncodeToString(b)
	}
	client := "{}"
	if info := clientInfoFromRequest(r); info != nil {
		raw, _ := json.Marshal(info.Summary())
		client = string(raw)
	}
	now := time.Now().Unix()
	// Account-instance and revision checks protect issuance racing with account deletion
	// or logout-all. A session issued with an old revision must never become current.
	res, err := s.st.exec(`INSERT INTO user_sessions(id,username,revision,created_at,last_seen,expires_at,ip,client)
		SELECT ?,username,session_rev,?,?,?,?,? FROM users WHERE username=? AND session_rev=? ON CONFLICT(id) DO NOTHING`,
		id, now, now, exp, s.auditIP(r), client, u.Username, u.SessionRev)
	if err != nil {
		jsonError(w, 500, "could not save session")
		return false
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		var valid int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM user_sessions WHERE id=? AND username=? AND revision=? AND expires_at=? AND revoked=0`, id, u.Username, u.SessionRev, exp).Scan(&valid); err != nil || valid != 1 {
			jsonErrorCode(w, 401, "session_expired", "please sign in again")
			return false
		}
	}
	s.writeSessionCookie(w, r, s.managedCookie(u, id, exp), exp)
	return true
}

// Upgrade a legacy cookie without extending its signed lifetime. Other legacy
// cookies remain valid until expiry or a revision change; bulk revocation covers them.
func (s *Server) ensureManagedSession(w http.ResponseWriter, r *http.Request, user string) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(c.Value, ".", 2)[0])
	if err != nil {
		return false
	}
	msg := string(raw)
	if strings.HasPrefix(msg, "v2|") {
		return true
	}
	originalUser, originalRev := s.verify(c.Value)
	if originalUser != user {
		jsonErrorCode(w, 401, "session_expired", "please sign in again")
		return false
	}
	end := strings.LastIndex(msg, "|")
	if end < 0 {
		return false
	}
	exp, err := strconv.ParseInt(msg[end+1:], 10, 64)
	if err != nil {
		return false
	}
	u := s.st.GetUser(user)
	if u == nil || u.SessionRev != originalRev {
		jsonErrorCode(w, 401, "session_expired", "please sign in again")
		return false
	}
	return s.newManagedSession(w, r, *u, exp, legacySessionID(c.Value))
}

// A migrated legacy cookie resolves to the same session, including its revocation
// tombstone. Replaying the old cookie must not recreate a session that was kicked out.
func (s *Server) verifyLegacy(user string, rev int64, cookie string, exp int64) (string, int64) {
	if s.st == nil {
		return user, rev
	}
	var owner string
	var storedRev, storedExp int64
	var revoked int
	err := s.st.queryRow(`SELECT username,revision,expires_at,revoked FROM user_sessions WHERE id=?`, legacySessionID(cookie)).Scan(&owner, &storedRev, &storedExp, &revoked)
	if err == sql.ErrNoRows {
		return user, rev
	}
	if err != nil || owner != user || storedRev != rev || storedExp != exp || revoked != 0 {
		return "", 0
	}
	return user, rev
}

func (s *Server) apiSessions(w http.ResponseWriter, r *http.Request, user string) {
	if !s.ensureManagedSession(w, r, user) {
		return
	}
	u := s.st.GetUser(user)
	if u == nil {
		jsonErrorCode(w, 401, "session_expired", "please sign in again")
		return
	}
	rows, err := s.st.query(`SELECT id,created_at,last_seen,expires_at,ip,client,method FROM user_sessions
		WHERE username=? AND revision=? AND expires_at>? AND revoked=0 ORDER BY last_seen DESC,id`, user, u.SessionRev, time.Now().Unix())
	if err != nil {
		jsonError(w, 500, "could not read sessions")
		return
	}
	defer rows.Close()
	items := []AccountSession{}
	for rows.Next() {
		var item AccountSession
		var client string
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.LastSeen, &item.ExpiresAt, &item.IP, &client, &item.Method); err != nil {
			jsonError(w, 500, "could not read sessions")
			return
		}
		json.Unmarshal([]byte(client), &item.Client)
		item.Current = item.ID == sessionID(r)
		items = append(items, item)
	}
	if rows.Err() != nil {
		jsonError(w, 500, "could not read sessions")
		return
	}
	writeJSON(w, map[string]any{"items": items, "timezone": s.st.GetSetting("timezone", "")})
}

func (s *Server) apiSessionDelete(w http.ResponseWriter, r *http.Request, user string) {
	id := r.PathValue("id")
	res, err := s.st.exec(`UPDATE user_sessions SET revoked=1 WHERE id=? AND username=? AND revoked=0`, id, user)
	if err != nil {
		jsonError(w, 500, "could not revoke session")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		jsonError(w, 404, "session not found")
		return
	}
	current := id == sessionID(r)
	if current {
		s.clearSessionCookie(w, r)
	}
	s.recordAuth(r, AuditSessionRevoke, user, user, map[string]any{"session_id": id})
	writeJSON(w, map[string]any{"ok": true, "signed_out": current})
}

func (s *Server) apiSessionsRevoke(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Scope string `json:"scope"`
	}
	if err := readJSON(r, &in); err != nil || (in.Scope != "all" && in.Scope != "others") {
		jsonError(w, 400, "scope must be all or others")
		return
	}
	if in.Scope == "others" && !s.ensureManagedSession(w, r, user) {
		return
	}
	u := s.st.GetUser(user)
	if u == nil {
		jsonErrorCode(w, 401, "session_expired", "please sign in again")
		return
	}
	tx, err := s.st.db.Begin()
	if err != nil {
		jsonError(w, 500, "could not revoke sessions")
		return
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	res, err := e.exec(`UPDATE users SET session_rev=session_rev+1 WHERE username=? AND session_rev=?`, user, u.SessionRev)
	if err != nil {
		jsonError(w, 500, "could not revoke sessions")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		jsonErrorCode(w, 401, "session_expired", "please sign in again")
		return
	}
	var exp int64
	id := sessionID(r)
	if in.Scope == "others" {
		if err := e.queryRow(`SELECT expires_at FROM user_sessions WHERE id=? AND username=? AND revision=? AND expires_at>? AND revoked=0`, id, user, u.SessionRev, time.Now().Unix()).Scan(&exp); err != nil {
			jsonErrorCode(w, 401, "session_expired", "please sign in again")
			return
		}
		if _, err = e.exec(`UPDATE user_sessions SET revision=? WHERE id=? AND username=?`, u.SessionRev+1, id, user); err != nil {
			jsonError(w, 500, "could not retain current session")
			return
		}
	}
	if _, err = e.exec(`DELETE FROM user_sessions WHERE username=? AND revision<>?`, user, u.SessionRev+1); err != nil {
		jsonError(w, 500, "could not revoke sessions")
		return
	}
	if err = tx.Commit(); err != nil {
		jsonError(w, 500, "could not revoke sessions")
		return
	}
	if in.Scope == "all" {
		s.clearSessionCookie(w, r)
	} else {
		u.SessionRev++
		s.writeSessionCookie(w, r, s.managedCookie(*u, id, exp), exp)
	}
	s.recordAuth(r, AuditSessionsRevoke, user, user, map[string]any{"scope": in.Scope})
	writeJSON(w, map[string]any{"ok": true, "signed_out": in.Scope == "all"})
}

func (s *Store) purgeSessions(now time.Time) error {
	_, err := s.exec(`DELETE FROM user_sessions WHERE expires_at<=? OR NOT EXISTS
		(SELECT 1 FROM users WHERE users.username=user_sessions.username AND users.session_rev=user_sessions.revision)`, now.Unix())
	return err
}
