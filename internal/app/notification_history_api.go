package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type notificationDeliveryView struct {
	ID         string `json:"id"`
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	ChannelID  string `json:"channel_id"`
	RouteID    string `json:"route_id"`
	Username   string `json:"username"`
	State      string `json:"state"`
	Attempts   int    `json:"attempts"`
	NextAt     string `json:"next_at"`
	ExpiresAt  string `json:"expires_at"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	OccurredAt string `json:"occurred_at"`
	ErrorCode  string `json:"error_code"`
	StatusCode int    `json:"status_code"`
	ReplayOf   string `json:"replay_of"`
	Generation int    `json:"generation"`
}

func deliveryView(d NotificationDelivery, kind string, occurredAt int64) notificationDeliveryView {
	return notificationDeliveryView{ID: d.ID, EventID: d.EventID, EventType: kind, ChannelID: d.ChannelID, RouteID: d.RouteID, Username: d.Username, State: d.State, Attempts: d.Attempts, NextAt: notificationTime(d.NextAt), ExpiresAt: notificationTime(d.ExpiresAt), CreatedAt: notificationTime(d.CreatedAt), UpdatedAt: notificationTime(d.UpdatedAt), OccurredAt: notificationTime(occurredAt), ErrorCode: d.LastError, StatusCode: d.StatusCode, ReplayOf: d.ReplayOf, Generation: d.Generation}
}

type notificationHistoryCursor struct {
	CreatedAt int64  `json:"created_at"`
	ID        string `json:"id"`
}

func (s *Server) apiNotificationHistory(w http.ResponseWriter, r *http.Request, user string) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			jsonErrorCode(w, 400, "invalid_param", "invalid history limit")
			return
		}
		limit = n
	}
	where := []string{"1=1"}
	args := []any{}
	if state := r.URL.Query().Get("state"); state != "" {
		if !notificationSelects("pending,leased,retry_wait,accepted,failed,uncertain,canceled,expired", state) {
			jsonErrorCode(w, 400, "invalid_param", "invalid delivery state")
			return
		}
		where = append(where, "d.state=?")
		args = append(args, state)
	}
	if channel := r.URL.Query().Get("channel_id"); channel != "" {
		if len(channel) > 128 {
			jsonErrorCode(w, 400, "invalid_param", "invalid channel id")
			return
		}
		where = append(where, "d.channel_id=?")
		args = append(args, channel)
	}
	if kind := r.URL.Query().Get("event_type"); kind != "" {
		if len(kind) > 100 {
			jsonErrorCode(w, 400, "invalid_param", "invalid event type")
			return
		}
		where = append(where, "e.event_type=?")
		args = append(args, kind)
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var cursor notificationHistoryCursor
		body, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(body) > 256 || json.Unmarshal(body, &cursor) != nil || cursor.CreatedAt < 0 || cursor.ID == "" || len(cursor.ID) > 128 {
			jsonErrorCode(w, 400, "invalid_param", "invalid history cursor")
			return
		}
		where = append(where, "(d.created_at<? OR (d.created_at=? AND d.id<?))")
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	columns := strings.Split(notificationDeliveryCols, ",")
	for i := range columns {
		columns[i] = "d." + columns[i]
	}
	args = append(args, limit+1)
	rows, err := s.st.query(`SELECT `+strings.Join(columns, ",")+`,e.event_type,e.created_at FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE `+strings.Join(where, " AND ")+` ORDER BY d.created_at DESC,d.id DESC LIMIT ?`, args...)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	defer rows.Close()
	views := []notificationDeliveryView{}
	var last notificationHistoryCursor
	next := ""
	for rows.Next() {
		var kind string
		var occurredAt int64
		d, err := scanNotificationDelivery(rows, &kind, &occurredAt)
		if err != nil {
			notificationAPIError(w, err)
			return
		}
		if len(views) == limit {
			body, _ := json.Marshal(last)
			next = base64.RawURLEncoding.EncodeToString(body)
			break
		}
		views = append(views, deliveryView(d, kind, occurredAt))
		last = notificationHistoryCursor{CreatedAt: d.CreatedAt, ID: d.ID}
	}
	if err := rows.Err(); err != nil {
		notificationAPIError(w, err)
		return
	}
	if err := rows.Close(); err != nil {
		notificationAPIError(w, err)
		return
	}
	writeJSON(w, map[string]any{"deliveries": views, "next_cursor": next})
}

func (s *Server) apiNotificationDelivery(w http.ResponseWriter, r *http.Request, user string) {
	d, err := s.st.NotificationDelivery(r.PathValue("id"))
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	event, err := s.st.NotificationEvent(d.EventID)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	rows, err := s.st.query(`SELECT id,started_at,finished_at,outcome,error_code,status_code FROM notification_attempts WHERE delivery_id=? ORDER BY started_at,id LIMIT 100`, d.ID)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	defer rows.Close()
	attempts := []map[string]any{}
	for rows.Next() {
		var id, outcome, code string
		var start, end int64
		var status int
		if err := rows.Scan(&id, &start, &end, &outcome, &code, &status); err != nil {
			notificationAPIError(w, err)
			return
		}
		attempts = append(attempts, map[string]any{"id": id, "started_at": notificationTime(start), "finished_at": notificationTime(end), "outcome": outcome, "error_code": code, "status_code": status})
	}
	if err := rows.Err(); err != nil {
		notificationAPIError(w, err)
		return
	}
	subject, text := notificationMessage(event)
	writeJSON(w, map[string]any{"delivery": deliveryView(d, event.Type, event.CreatedAt), "attempts": attempts, "preview": map[string]string{"subject": subject, "text": text}})
}

func (s *Server) apiNotificationReplay(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		RequestID        string `json:"request_id"`
		ChannelRevision  int64  `json:"channel_revision"`
		RouteRevision    int64  `json:"route_revision"`
		ConfirmDuplicate bool   `json:"confirm_duplicate"`
		Reason           string `json:"reason"`
	}
	if readJSON(r, &in) != nil || len(in.RequestID) < 8 || len(in.RequestID) > 128 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 {
		jsonErrorCode(w, 400, "invalid_param", "replay requires a request id and review reason")
		return
	}
	id := r.PathValue("id")
	// An HTTP retry returns the same generation even if configuration subsequently
	// changed; it must never enqueue a second outgoing message for this request.
	var previous string
	if err := s.st.queryRow(`SELECT id FROM notification_deliveries WHERE replay_key=?`, notificationReplayKey(id, in.RequestID)).Scan(&previous); err == nil {
		writeJSON(w, map[string]any{"ok": true, "id": previous, "created": false})
		return
	}
	original, err := s.st.NotificationDelivery(id)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	if original.Username != "" {
		event, err := s.st.NotificationEvent(original.EventID)
		if err != nil {
			notificationAPIError(w, err)
			return
		}
		allowed := s.notificationMayRead(original.Username, event)
		if allowed && original.RouteID != "" {
			var mode string
			if err := s.st.queryRow(`SELECT recipient_mode FROM notification_routes WHERE id=?`, original.RouteID).Scan(&mode); err != nil {
				allowed = false
			} else if mode == "grant" {
				allowed = s.notificationRouteAllows(original.RouteID, original.Username)
			} else {
				allowed = mode == "creator" && event.ResourceType == "job"
			}
		}
		if !allowed {
			notificationAPIError(w, errNotificationConflict)
			return
		}
	}
	c, err := s.st.NotificationChannel(original.ChannelID)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	_, effective, err := s.effectiveNotificationChannel(c)
	if err != nil {
		jsonErrorCode(w, 409, "notification_unavailable", "notification destination is unavailable")
		return
	}
	d, created, err := s.st.ReplayNotification(id, in.RequestID, in.ChannelRevision, effective, in.RouteRevision, in.ConfirmDuplicate, time.Now().UTC())
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	if created {
		s.recordChange(r, user, AuditNotificationReplay, "notification_delivery", d.ID, map[string]any{"replay_of": id, "reason": in.Reason, "duplicate_confirmed": in.ConfirmDuplicate})
	}
	writeJSON(w, map[string]any{"ok": true, "id": d.ID, "created": created})
}

func (s *Server) apiNotificationReview(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Decision  string `json:"decision"`
		Reason    string `json:"reason"`
		UpdatedAt string `json:"updated_at"`
	}
	if readJSON(r, &in) != nil || in.Decision != "dismiss" || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 {
		jsonErrorCode(w, 400, "invalid_param", "review requires a dismissal decision and reason")
		return
	}
	var expected int64
	if in.UpdatedAt != "" {
		instant, err := time.Parse(time.RFC3339Nano, in.UpdatedAt)
		if err != nil {
			jsonErrorCode(w, 400, "invalid_param", "invalid review timestamp")
			return
		}
		expected = instant.UnixMilli()
	}
	id := r.PathValue("id")
	res, err := s.st.exec(`UPDATE notification_deliveries SET state='canceled',last_error=CASE WHEN last_error='account_deleted' THEN last_error ELSE 'reviewed_uncertain' END,updated_at=? WHERE id=? AND state='uncertain' AND updated_at=?`, time.Now().UTC().UnixMilli(), id, expected)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		notificationAPIError(w, errNotificationConflict)
		return
	}
	s.recordChange(r, user, AuditNotificationReview, "notification_delivery", id, map[string]any{"decision": in.Decision, "reason": in.Reason})
	writeJSON(w, okJSON)
}

func notificationQueueReview(e migExec) (string, map[string]int, error) {
	paused := e.setting("notification_dispatch_paused")
	query := `SELECT id,state,updated_at,channel_revision,route_revision FROM notification_deliveries WHERE state IN ('pending','leased','retry_wait','uncertain') ORDER BY id`
	if e.s.driver == "postgres" {
		query += ` FOR UPDATE`
	}
	rows, err := e.query(query)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "paused=%s\n", paused)
	counts := map[string]int{"pending": 0, "leased": 0, "retry_wait": 0, "uncertain": 0}
	for rows.Next() {
		var id, state string
		var updated, channel, route int64
		if err := rows.Scan(&id, &state, &updated, &channel, &route); err != nil {
			return "", nil, err
		}
		counts[state]++
		_, _ = fmt.Fprintf(hash, "%s/%s/%d/%d/%d\n", id, state, updated, channel, route)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), counts, nil
}

func (s *Store) notificationQueueReview() (string, map[string]int, error) {
	var token string
	var counts map[string]int
	tx, err := s.db.Begin()
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780714)`); err != nil {
			return "", nil, err
		}
	}
	token, counts, err = notificationQueueReview(e)
	return token, counts, err
}

func (s *Server) apiNotificationDispatch(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Paused      *bool  `json:"paused"`
		ReviewToken string `json:"review_token"`
	}
	if readJSON(r, &in) != nil || in.Paused == nil {
		jsonErrorCode(w, 400, "invalid_param", "dispatch state is required")
		return
	}
	err := s.st.withNotificationConfig(func(e migExec) error {
		if s.st.driver == "postgres" {
			if _, err := e.exec(`SELECT pg_advisory_xact_lock(780714)`); err != nil {
				return err
			}
		}
		token, _, err := notificationQueueReview(e)
		if err != nil {
			return err
		}
		if !*in.Paused && e.setting("notification_dispatch_paused") == "1" && (in.ReviewToken == "" || in.ReviewToken != token) {
			return errNotificationConflict
		}
		return e.setSetting("notification_dispatch_paused", boolStr(*in.Paused))
	})
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	token, counts, err := s.st.notificationQueueReview()
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	s.recordChange(r, user, AuditNotificationDispatch, "notification_dispatch", "operational", map[string]any{"paused": *in.Paused})
	writeJSON(w, map[string]any{"ok": true, "dispatch_paused": *in.Paused, "review_token": token, "counts": counts})
}
