package app

import (
	"encoding/json"
	"log"
	"time"
)

// Outbound event dispatch. fireEvent is the single entry point the rest of the app
// calls when something worth broadcasting happens. See docs/adr/0002-extension-architecture.md.

// Event type constants (the initial catalogue).
const (
	EventReportIngested = "report.ingested"
	EventBatchFinished  = "batch.job.finished"
	// A structured-prediction payload the ledger could not use (ADR 0029 §3). Its own event rather
	// than a flag on report.ingested, because it is the one outcome a subscriber should alert on and
	// the report itself is stored and looks normal.
	EventPredictionsRejected = "predictions.rejected"
	// A covered ingest whose ledger write failed after a retry (P2, plan B1). The report exists and
	// the set does not, and it cannot be re-created after the fact, so this is an incident: the one
	// event a subscriber should page on.
	EventPredictionsStoreFailed = "predictions.store_failed"
	// The evaluator has stopped or fallen behind (P9, plan §6.1). One event per reason per window,
	// de-duplicated through forecast_alert_state, so a subscriber sees "it stopped" once rather than
	// once a minute.
	EventForecastEvaluatorUnhealthy = "forecast.evaluator.unhealthy"
)

const webhookTimeout = 15 * time.Second

// fireEvent captures operational work after business commit. It never performs
// network I/O or starts a goroutine per recipient. Workers own all retries.
func (s *Server) fireEvent(kind string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		log.Print("notification: event serialization failed")
		return
	}
	id := randToken()
	event := NotificationEvent{ID: id, SourceKey: kind + ":" + id, Type: kind, Payload: string(body), ResourceType: "operations", CreatedAt: time.Now().UTC().UnixMilli()}
	if kind == EventReportIngested {
		var identity struct {
			ID json.Number `json:"id"`
		}
		if json.Unmarshal(body, &identity) == nil && identity.ID != "" {
			event.ResourceType = "report"
			event.ResourceID = identity.ID.String()
		}
	}
	deliveries, err := s.legacyNotificationDeliveries(kind)
	var recipients []string
	if err == nil {
		deliveries, recipients, err = s.routeNotification(&event, deliveries)
	}
	if err == nil {
		err = s.st.CaptureNotification(event, deliveries, recipients)
	}
	if err != nil {
		log.Print("notification: operational event capture failed")
	}
}

func (s *Server) legacyNotificationDeliveries(kind string) ([]NotificationDelivery, error) {
	var channels []NotificationChannel
	for _, w := range s.st.WebhooksForEvent(kind) {
		c, err := s.ensureLegacyNotificationChannel("webhook:"+itoa64(w.ID), "webhook", "Webhook #"+itoa64(w.ID))
		if err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	if kind == EventForecastEvaluatorUnhealthy || kind == EventPredictionsStoreFailed {
		for _, spec := range []struct {
			ref, adapter, label string
			configured          bool
		}{
			{"forecast-email", "email", "Forecast email", len(alertRecipients(s.st.GetSetting(setForecastAlertEmailTo, ""))) > 0},
			{"forecast-slack", "slack", "Forecast Slack", s.st.GetSetting(setForecastAlertSlackURL, "") != ""},
		} {
			if !spec.configured {
				continue
			}
			c, err := s.ensureLegacyNotificationChannel(spec.ref, spec.adapter, spec.label)
			if err != nil {
				return nil, err
			}
			channels = append(channels, c)
		}
	}
	deliveries := make([]NotificationDelivery, 0, len(channels))
	for _, c := range channels {
		_, revision, err := s.effectiveNotificationChannel(c)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, NotificationDelivery{ChannelID: c.ID, ChannelRevision: revision})
	}
	return deliveries, nil
}
