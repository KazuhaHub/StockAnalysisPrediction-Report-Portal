package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

var notificationPublisherRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var notificationOccurrenceRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func notificationServiceScope(scope string) (string, bool) {
	name, ok := strings.CutPrefix(scope, "notify:")
	return name, ok && notificationPublisherRe.MatchString(name)
}

// The namespace is credential-derived. The caller cannot name a native event,
// recipient, route, resource, destination or HTML action in its request.
func (s *Server) notificationPublisher(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || token == "" {
		return "", false
	}
	token = strings.TrimSpace(token)
	if s.appTok != nil {
		if entry, ok := s.appTok.publisher(token, time.Now()); ok {
			app, installed := s.st.GetApp(entry.appID)
			user := s.st.GetUser(entry.username)
			if !installed || !slices.Contains(app.Scopes, "notify") || user == nil || !user.Active || !user.IsAdmin() || s.accountExpired(user) || user.SessionRev != entry.sessionRev || user.CreatedAt != entry.accountCreated {
				return "", false
			}
			return "app." + app.ID, true
		}
	}
	var scope string
	if err := s.st.queryRow(`SELECT scope FROM api_tokens WHERE token_hash=? OR token=?`, tokenDigest(token), token).Scan(&scope); err != nil {
		return "", false
	}
	name, ok := notificationServiceScope(scope)
	if !ok || !s.st.TokenValid(token, scope) {
		return "", false
	}
	return "service." + name, true
}

func (s *Server) notificationEvents() []string {
	events := availableEvents()
	for _, token := range s.st.ListTokens() {
		if name, ok := notificationServiceScope(token.Scope); ok {
			events = append(events, "extension.service."+name+".notice")
		}
	}
	for _, app := range s.st.ListApps() {
		if slices.Contains(app.Scopes, "notify") {
			events = append(events, "extension.app."+app.ID+".notice")
		}
	}
	rows, err := s.st.query(`SELECT DISTINCT i.app_id FROM app_platform_installations i JOIN app_platform_capability_grants g ON g.installation_id=i.id AND g.capability='notifications.publish' AND g.granted=1 WHERE i.desired_state='enabled' AND i.phase='ready' ORDER BY i.app_id`)
	if err == nil {
		for rows.Next() {
			var appID string
			if rows.Scan(&appID) == nil && platformDirectoryAppID.MatchString(appID) {
				events = append(events, "extension.app."+appID+".notice")
			}
		}
		rows.Close()
	}
	// Keep already configured selectors manageable after a publisher is revoked.
	if routes, err := s.st.NotificationRoutes(); err == nil {
		for _, route := range routes {
			for _, kind := range strings.Split(route.Events, ",") {
				if strings.HasPrefix(kind, "extension.") {
					events = append(events, kind)
				}
			}
		}
	}
	sort.Strings(events)
	unique := events[:0]
	for _, kind := range events {
		if len(unique) == 0 || unique[len(unique)-1] != kind {
			unique = append(unique, kind)
		}
	}
	return unique
}

func (s *Server) v1PublishNotification(w http.ResponseWriter, r *http.Request) {
	publisher, ok := s.notificationPublisher(r)
	if !ok {
		v1err(w, 401, "unauthorized", "a dedicated notification publication credential is required")
		return
	}
	var in struct {
		OccurrenceID string  `json:"occurrence_id"`
		Title        string  `json:"title"`
		Message      *string `json:"message"`
		Severity     string  `json:"severity"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || !notificationOccurrenceRe.MatchString(in.OccurrenceID) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 240 || in.Message == nil || len(*in.Message) > 8192 || !notificationSelects("info,warning,error,resolved", in.Severity) {
		v1err(w, 400, "invalid_param", "provide occurrence_id, title, message and a registered severity only")
		return
	}
	// Reject trailing JSON as well as unknown destination/identity fields.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		v1err(w, 400, "invalid_param", "one JSON object is required")
		return
	}
	id, created, err := s.capturePublishedNotification(publisher, in.OccurrenceID, in.Title, *in.Message, in.Severity)
	if err != nil {
		s.notificationPublishError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "created": created})
}

func (s *Server) capturePublishedNotification(publisher, occurrenceID, title, message, severity string) (string, bool, error) {
	payload, _ := json.Marshal(map[string]string{"publisher": publisher, "title": title, "message": message, "severity": severity})
	id := randToken()
	event := NotificationEvent{ID: id, SourceKey: "extension:" + publisher + ":" + occurrenceID, Type: "extension." + publisher + ".notice", ResourceType: "extension", ResourceID: publisher, Payload: string(payload), CreatedAt: time.Now().UTC().UnixMilli()}
	var existing string
	err := s.st.queryRow(`SELECT id FROM notification_events WHERE source_key=?`, event.SourceKey).Scan(&existing)
	if err == nil {
		if err := s.st.CaptureNotification(event, nil, nil); err != nil {
			return "", false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	deliveries, err := s.legacyNotificationDeliveries(event.Type)
	var recipients []string
	if err == nil {
		deliveries, recipients, err = s.routeNotification(&event, deliveries)
	}
	if err == nil {
		err = s.st.CaptureNotification(event, deliveries, recipients)
	}
	if err != nil {
		return "", false, err
	}
	if err := s.st.queryRow(`SELECT id FROM notification_events WHERE source_key=?`, event.SourceKey).Scan(&existing); err != nil {
		return "", false, err
	}
	s.st.WriteAudit(AuditEntry{Action: AuditNotificationPublish, TargetType: "notification_event", TargetID: existing, Detail: `{"publisher":"` + publisher + `"}`})
	return existing, existing == id, nil
}

func (s *Server) notificationPublishError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotificationConflict):
		v1err(w, 409, "notification_conflict", "source occurrence was already used with different content")
	case errors.Is(err, errNotificationCapacity):
		v1err(w, 429, "notification_capacity", "notification publication capacity unavailable")
	default:
		v1err(w, 500, "db_error", "notification capture failed")
	}
}

// v1NotificationPublisherIdentity lets an external outbox verify its configured
// namespace without publishing a test event or inspecting administrator records.
func (s *Server) v1NotificationPublisherIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	publisher, ok := s.notificationPublisher(r)
	if !ok {
		v1err(w, 401, "unauthorized", "a dedicated notification publication credential is required")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "publisher": publisher, "event_type": "extension." + publisher + ".notice"})
}
