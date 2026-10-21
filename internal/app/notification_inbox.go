package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type notificationInboxItem struct {
	ID         string `json:"id"`
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	Subject    string `json:"subject"`
	Text       string `json:"text"`
	OccurredAt string `json:"occurred_at"`
	ReadAt     string `json:"read_at"`
	event      NotificationEvent
}

// Original recipient membership is necessary but never sufficient. Content is
// projected only after current resource authorization succeeds, for counts too.
func (s *Server) notificationInboxItems(user string) ([]notificationInboxItem, error) {
	rows, err := s.st.query(`SELECT r.id,r.read_at,e.id,e.source_key,e.event_type,e.resource_type,e.resource_id,e.created_at,e.expires_at FROM notification_recipients r JOIN notification_events e ON e.id=r.event_id WHERE r.username=? AND r.dismissed_at=0 ORDER BY e.created_at DESC,r.id DESC LIMIT 10001`, user)
	if err != nil {
		return nil, err
	}
	var candidates []notificationInboxItem
	for rows.Next() {
		var item notificationInboxItem
		var read int64
		e := &item.event
		if err := rows.Scan(&item.ID, &read, &e.ID, &e.SourceKey, &e.Type, &e.ResourceType, &e.ResourceID, &e.CreatedAt, &e.ExpiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		item.ReadAt = notificationTime(read)
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates) > 10000 {
		return nil, errNotificationCapacity
	}
	items := make([]notificationInboxItem, 0, len(candidates))
	routes, err := s.st.NotificationRoutes()
	if err != nil {
		return nil, err
	}
	channels, err := s.st.NotificationChannels()
	if err != nil {
		return nil, err
	}
	inboxChannels := map[string]bool{}
	for _, c := range channels {
		if c.Adapter == "inbox" && c.Enabled {
			inboxChannels[c.ID] = true
		}
	}
	selectedEvents := map[string]bool{}
	creatorEvents := map[string]bool{}
	for _, route := range routes {
		if !route.Enabled || !inboxChannels[route.ChannelID] {
			continue
		}
		if route.RecipientMode == "grant" && s.notificationRouteAllows(route.ID, user) {
			for _, kind := range strings.Split(route.Events, ",") {
				selectedEvents[kind] = true
			}
		}
		if route.RecipientMode == "creator" {
			for _, kind := range strings.Split(route.Events, ",") {
				creatorEvents[kind] = true
			}
		}
	}
	authority := map[string]bool{}
	jobConsent := map[string]bool{}
	for _, item := range candidates {
		resource := item.event.ResourceType + ":" + item.event.ResourceID
		allowed, checked := authority[resource]
		if !checked {
			allowed = s.notificationMayRead(user, item.event)
			authority[resource] = allowed
		}
		if !allowed {
			continue
		}
		if item.event.ExpiresAt > 0 && item.event.ExpiresAt <= time.Now().UnixMilli() {
			continue
		}
		audience := false
		if item.event.ResourceType == "job" {
			jobID, _, _ := strings.Cut(item.event.ResourceID, "@")
			consented, checked := jobConsent[jobID]
			if !checked {
				var n int
				if err := s.st.queryRow(`SELECT COUNT(*) FROM job_notification_intents WHERE job_id=? AND username=? AND consumed_at>0`, jobID, user).Scan(&n); err != nil {
					return nil, err
				}
				consented = n > 0
				jobConsent[jobID] = consented
			}
			audience = consented || creatorEvents[item.event.Type]
		}
		audience = audience || selectedEvents[item.event.Type]
		if !audience {
			continue
		}
		item.EventID, item.EventType, item.OccurredAt = item.event.ID, item.event.Type, notificationTime(item.event.CreatedAt)
		items = append(items, item)
	}
	return items, nil
}

func (s *Server) apiNotificationInbox(w http.ResponseWriter, r *http.Request, user string) {
	items, err := s.notificationInboxItems(user)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	unread := 0
	for _, item := range items {
		if item.ReadAt == "" {
			unread++
		}
	}
	cursor := r.URL.Query().Get("cursor")
	start := 0
	if cursor != "" {
		found := false
		for i, item := range items {
			if item.ID == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			notificationAPIError(w, errNotificationConflict)
			return
		}
	}
	next := ""
	end := min(len(items), start+50)
	if end < len(items) {
		next = items[end-1].ID
	}
	for i := start; i < end; i++ {
		event, err := s.st.NotificationEvent(items[i].EventID)
		if err != nil {
			notificationAPIError(w, err)
			return
		}
		items[i].Subject, items[i].Text = notificationMessage(event)
	}
	writeJSON(w, map[string]any{"items": items[start:end], "unread": unread, "next_cursor": next})
}

func (s *Server) apiNotificationUnread(w http.ResponseWriter, r *http.Request, user string) {
	items, err := s.notificationInboxItems(user)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	count := 0
	for _, item := range items {
		if item.ReadAt == "" {
			count++
		}
	}
	writeJSON(w, map[string]any{"unread": count})
}

func (s *Server) apiNotificationInboxUpdate(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Action string `json:"action"`
	}
	if readJSON(r, &in) != nil || (in.Action != "read" && in.Action != "dismiss") {
		jsonErrorCode(w, 400, "invalid_param", "select a notification action")
		return
	}
	items, err := s.notificationInboxItems(user)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	var selected *notificationInboxItem
	for i := range items {
		if items[i].ID == r.PathValue("id") {
			selected = &items[i]
			break
		}
	}
	if selected == nil {
		notificationAPIError(w, sql.ErrNoRows)
		return
	}
	generation, err := notificationAccountGeneration(migExec{s: s.st, ex: s.st.db}, user)
	if err == nil {
		err = s.st.updateNotificationInbox(user, generation, []string{selected.ID}, in.Action)
	}
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	writeJSON(w, okJSON)
}

func (s *Store) updateNotificationInbox(user string, generation int64, ids []string, action string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	query := `SELECT username FROM users WHERE username=? AND active=1`
	if s.driver == "postgres" {
		query += ` FOR SHARE`
	}
	var actual string
	if err := e.queryRow(query, user).Scan(&actual); err != nil {
		return errNotificationConflict
	}
	current, err := notificationAccountGeneration(e, user)
	if err != nil {
		return err
	}
	if current != generation {
		return errNotificationConflict
	}
	field := "read_at"
	if action == "dismiss" {
		field = "dismissed_at"
	}
	now := time.Now().UTC().UnixMilli()
	for _, id := range ids {
		if _, err := e.exec(`UPDATE notification_recipients SET `+field+`=? WHERE id=? AND username=? AND `+field+`=0`, now, id, user); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type notificationReadSnapshot struct {
	User       string   `json:"user"`
	Generation int64    `json:"generation"`
	Expires    int64    `json:"expires"`
	IDs        []string `json:"ids"`
}

func (s *Server) notificationSnapshotSignature(body string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.SecretKey))
	_, _ = m.Write([]byte("notification-read-all\x00" + body))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) apiNotificationReadSnapshot(w http.ResponseWriter, r *http.Request, user string) {
	generation, err := notificationAccountGeneration(migExec{s: s.st, ex: s.st.db}, user)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	items, err := s.notificationInboxItems(user)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	snap := notificationReadSnapshot{User: user, Generation: generation, Expires: time.Now().Add(5 * time.Minute).Unix(), IDs: []string{}}
	for _, item := range items {
		if item.ReadAt == "" {
			snap.IDs = append(snap.IDs, item.ID)
		}
	}
	body, _ := json.Marshal(snap)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	writeJSON(w, map[string]any{"token": encoded + "." + s.notificationSnapshotSignature(encoded), "count": len(snap.IDs)})
}

func (s *Server) apiNotificationReadAll(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Token string `json:"token"`
	}
	if readJSON(r, &in) != nil || len(in.Token) > 900000 {
		jsonErrorCode(w, 400, "invalid_param", "invalid read snapshot")
		return
	}
	body, signature, ok := strings.Cut(in.Token, ".")
	if !ok || !hmac.Equal([]byte(signature), []byte(s.notificationSnapshotSignature(body))) {
		notificationAPIError(w, errNotificationConflict)
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(body)
	var snap notificationReadSnapshot
	if err != nil || json.Unmarshal(decoded, &snap) != nil || snap.User != user || snap.Expires < time.Now().Unix() || len(snap.IDs) > 10000 {
		notificationAPIError(w, errNotificationConflict)
		return
	}
	items, err := s.notificationInboxItems(user)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	allowed := map[string]bool{}
	for _, item := range items {
		allowed[item.ID] = true
	}
	ids := []string{}
	for _, id := range snap.IDs {
		if allowed[id] {
			ids = append(ids, id)
		}
	}
	if err := s.st.updateNotificationInbox(user, snap.Generation, ids, "read"); err != nil {
		notificationAPIError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(ids)})
}

func (s *Server) apiNotificationPreferences(w http.ResponseWriter, r *http.Request, user string) {
	values := map[string]bool{}
	for _, kind := range s.notificationEvents() {
		values[kind] = true
	}
	rows, err := s.st.query(`SELECT event_type,enabled FROM notification_preferences WHERE username=?`, user)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var enabled int
		if err := rows.Scan(&kind, &enabled); err != nil {
			notificationAPIError(w, err)
			return
		}
		if _, known := values[kind]; known {
			values[kind] = enabled != 0
		}
	}
	if err := rows.Err(); err != nil {
		notificationAPIError(w, err)
		return
	}
	writeJSON(w, map[string]any{"preferences": values})
}

func (s *Server) apiNotificationPreferencesSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Preferences map[string]bool `json:"preferences"`
	}
	if readJSON(r, &in) != nil || len(in.Preferences) > len(s.notificationEvents()) {
		jsonErrorCode(w, 400, "invalid_param", "invalid notification preferences")
		return
	}
	for kind := range in.Preferences {
		if !notificationSelects(strings.Join(s.notificationEvents(), ","), kind) {
			jsonErrorCode(w, 400, "invalid_param", "unknown notification preference")
			return
		}
	}
	tx, err := s.st.db.Begin()
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	query := `SELECT username FROM users WHERE username=? AND active=1`
	if s.st.driver == "postgres" {
		query += ` FOR SHARE`
	}
	var actual string
	if err := e.queryRow(query, user).Scan(&actual); err != nil {
		notificationAPIError(w, errNotificationConflict)
		return
	}
	for kind, enabled := range in.Preferences {
		if _, err := e.exec(`INSERT INTO notification_preferences(username,event_type,enabled) VALUES(?,?,?) ON CONFLICT(username,event_type) DO UPDATE SET enabled=excluded.enabled`, user, kind, boolInt(enabled)); err != nil {
			notificationAPIError(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		notificationAPIError(w, err)
		return
	}
	writeJSON(w, okJSON)
}
