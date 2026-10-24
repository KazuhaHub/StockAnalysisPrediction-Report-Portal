package app

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// routeNotification adds explicit admin rules to the source's compatibility
// fan-out. Candidate selection cannot expand current authority to the resource.
// Inbox candidates are materialized at capture; future grants cannot reveal an
// old event to an account that was not an original recipient.
func (s *Server) routeNotification(event *NotificationEvent, deliveries []NotificationDelivery) ([]NotificationDelivery, []string, error) {
	if event.candidateGenerations == nil {
		event.candidateGenerations = map[string]int64{}
	}
	routes, err := s.st.NotificationRoutes()
	if err != nil {
		return nil, nil, err
	}
	if len(routes) > 100 {
		return nil, nil, errNotificationCapacity
	}
	var users []string
	loadedUsers := false
	recipients := map[string]bool{}
	for _, route := range routes {
		if !route.Enabled || !notificationSelects(route.Events, event.Type) {
			continue
		}
		c, err := s.st.NotificationChannel(route.ChannelID)
		if err != nil {
			return nil, nil, err
		}
		if !c.Enabled {
			continue
		}
		_, revision, err := s.effectiveNotificationChannel(c)
		if err != nil {
			return nil, nil, err
		}
		base := NotificationDelivery{ChannelID: c.ID, ChannelRevision: revision, RouteID: route.ID, RouteRevision: route.Revision}
		if route.RecipientMode == "destination" {
			if c.Adapter != "email" && c.Adapter != "webhook" && c.Adapter != "slack" {
				return nil, nil, fmt.Errorf("invalid destination adapter")
			}
			deliveries = append(deliveries, base)
			continue
		}
		if (route.RecipientMode != "grant" && route.RecipientMode != "creator") || (c.Adapter != "email" && c.Adapter != "inbox") {
			return nil, nil, fmt.Errorf("invalid recipient strategy")
		}
		if !loadedUsers {
			rows, err := s.st.query(`SELECT username FROM users WHERE active=1 ORDER BY username LIMIT 10001`)
			if err != nil {
				return nil, nil, err
			}
			for rows.Next() {
				var username string
				if err := rows.Scan(&username); err != nil {
					rows.Close()
					return nil, nil, err
				}
				users = append(users, username)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, nil, err
			}
			if len(users) > 10000 {
				return nil, nil, errNotificationCapacity
			}
			loadedUsers = true
		}
		for _, username := range users {
			if _, ok := event.candidateGenerations[username]; !ok {
				generation, err := notificationAccountGeneration(migExec{s: s.st, ex: s.st.db}, username)
				if err != nil {
					return nil, nil, err
				}
				event.candidateGenerations[username] = generation
			}
			if route.RecipientMode == "grant" && !s.notificationRouteAllows(route.ID, username) {
				continue
			}
			if route.RecipientMode == "creator" && event.ResourceType != "job" {
				continue
			}
			if !s.notificationMayRead(username, *event) {
				continue
			}
			var enabled int
			err := s.st.queryRow(`SELECT enabled FROM notification_preferences WHERE username=? AND event_type=?`, username, event.Type).Scan(&enabled)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, nil, err
			}
			if err == nil && enabled == 0 && !route.Mandatory {
				continue
			}
			if c.Adapter == "inbox" {
				recipients[username] = true
				continue
			}
			d := base
			d.Username = username
			deliveries = append(deliveries, d)
			if len(deliveries) > 1000 {
				return nil, nil, errNotificationCapacity
			}
		}
	}
	usernames := make([]string, 0, len(recipients))
	for username := range recipients {
		usernames = append(usernames, username)
	}
	return deliveries, usernames, nil
}

// Deletion advances a lightweight invalidation cursor in the approved source
// state table. This fences candidates selected before deletion without adding an
// account-ID column or retaining a plaintext deleted username in that cursor.
func notificationAccountKey(username string) string {
	return fmt.Sprintf("notification-account:%x", sha256.Sum256([]byte(username)))
}

func notificationAccountGeneration(e migExec, username string) (int64, error) {
	var generation int64
	err := e.queryRow(`SELECT generation FROM notification_source_cycles WHERE source_key=?`, notificationAccountKey(username)).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return generation, err
}

func notificationSelects(selectors, kind string) bool {
	for _, selector := range strings.Split(selectors, ",") {
		if strings.TrimSpace(selector) == kind {
			return true
		}
	}
	return false
}
