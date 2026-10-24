package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

type jobNotificationSnapshot struct {
	Job             BatchJob `json:"job"`
	OccurredAt      int64    `json:"occurred_at"`
	OwnerGeneration int64    `json:"owner_generation"`
}

// Bind notification ownership at submission, not when a later completion is
// captured. A deleted account's old jobs must not notify a replacement holder
// of its reusable username through a creator rule.
func jobNotificationOwnerGeneration(e migExec, username string) (int64, error) {
	query := `SELECT username FROM users WHERE username=? AND active=1`
	if e.s.driver == "postgres" {
		query += ` FOR SHARE`
	}
	var actual string
	if err := e.queryRow(query, username).Scan(&actual); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return -1, nil
		}
		return 0, err
	}
	return notificationAccountGeneration(e, username)
}

// recordJobNotificationOccurrence records completion evidence with the business
// terminal transition. It does not enqueue or send a notification. Capturing the
// event happens after commit; recovery never calls a provider or executes a run.
func recordJobNotificationOccurrence(e migExec, j BatchJob) error {
	key := "job:" + itoa64(j.ID)
	if _, err := e.exec(`INSERT INTO notification_source_cycles(source_key,occurrence_id) VALUES(?,?) ON CONFLICT(source_key) DO NOTHING`, key, randToken()); err != nil {
		return err
	}
	var token string
	var owner string
	var generation int64
	if err := e.queryRow(`SELECT occurrence_id,generation,signature FROM notification_source_cycles WHERE source_key=?`, key).Scan(&token, &generation, &owner); err != nil {
		return err
	}
	ownerGeneration := int64(-1)
	if owner != "" {
		var err error
		ownerGeneration, err = strconv.ParseInt(owner, 10, 64)
		if err != nil {
			return err
		}
	}
	body, err := json.Marshal(jobNotificationSnapshot{Job: j, OccurredAt: time.Now().UTC().UnixMilli(), OwnerGeneration: ownerGeneration})
	if err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO notification_source_cycles(source_key,generation,occurrence_id,signature) VALUES(?,?,?,?) ON CONFLICT(source_key) DO NOTHING`, "job-completion:"+itoa64(j.ID)+":"+token, generation, token, string(body))
	return err
}

// Remove old consent and pending snapshots in the deleting transaction. This
// also prevents inheritance after deliberate ID reuse during database repair.
func clearJobNotificationEvidence(e migExec, predicate string, args ...any) error {
	if e.s.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780714)`); err != nil {
			return err
		}
	}
	if _, err := e.exec(`DELETE FROM job_notification_intents WHERE job_id IN (SELECT id FROM batch_jobs WHERE `+predicate+`)`, args...); err != nil {
		return err
	}
	_, err := e.exec(`UPDATE notification_source_cycles SET signature='' WHERE signature<>'' AND EXISTS (SELECT 1 FROM batch_jobs WHERE `+predicate+` AND notification_source_cycles.source_key LIKE 'job-completion:' || CAST(batch_jobs.id AS TEXT) || ':%')`, args...)
	return err
}

// notifyJobDone captures durable work only; a failed destination can no longer
// erase the submitter's consent before the delivery has been persisted.
func (s *Server) notifyJobDone(j BatchJob) {
	if err := s.captureJobNotifications(j.ID); err != nil {
		log.Print("notification: job completion capture failed")
	}
}

func (s *Server) recoverJobNotifications() error { return s.captureJobNotifications(0) }

func (s *Server) captureJobNotifications(jobID int64) error {
	// Close the query before capture opens its transaction (SQLite uses one
	// connection). The bound also limits recovery work on each coordinator tick.
	rows, err := s.st.query(`SELECT source_key,signature FROM notification_source_cycles WHERE source_key LIKE 'job-completion:%' AND signature<>'' ORDER BY generation,source_key LIMIT 100`)
	if err != nil {
		return err
	}
	type occurrence struct{ key, body string }
	var pending []occurrence
	for rows.Next() {
		var o occurrence
		if err := rows.Scan(&o.key, &o.body); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range pending {
		var snapshot jobNotificationSnapshot
		if err := json.Unmarshal([]byte(o.body), &snapshot); err != nil || snapshot.OccurredAt <= 0 {
			return fmt.Errorf("invalid completion evidence")
		}
		j := snapshot.Job
		if jobID != 0 && j.ID != jobID {
			continue
		}
		payload, _ := json.Marshal(map[string]any{"job_id": j.ID, "status": j.Status, "total": j.Total, "succeeded": j.Succeeded, "partial": j.Partial, "failed": j.Failed})
		event := NotificationEvent{ID: o.key[strings.LastIndex(o.key, ":")+1:], SourceKey: o.key, Type: EventBatchFinished, Payload: string(payload), ResourceType: "job", ResourceID: itoa64(j.ID) + "@" + strconv.FormatInt(snapshot.OwnerGeneration, 10), CreatedAt: snapshot.OccurredAt, candidateGenerations: map[string]int64{j.CreatedBy: snapshot.OwnerGeneration}}
		ds, err := s.legacyNotificationDeliveries(EventBatchFinished)
		if err != nil {
			return err
		}

		c, err := s.ensureLegacyNotificationChannel("smtp", "email", "Job completion email")
		if err != nil {
			return err
		}
		_, revision, err := s.effectiveNotificationChannel(c)
		if err != nil {
			return err
		}
		ds = append(ds, NotificationDelivery{ChannelID: c.ID, ChannelRevision: revision, Username: j.CreatedBy})
		ds, recipients, err := s.routeNotification(&event, ds)
		if err != nil {
			return err
		}
		if err := s.st.captureNotification(event, ds, recipients, j.ID, o.key); err != nil {
			return err
		}
	}
	return nil
}

// A deterministic reference never copies legacy credentials or creates a second
// editable destination. Concurrent adoption converges on the same channel.
func (s *Server) ensureLegacyNotificationChannel(ref, adapter, label string) (NotificationChannel, error) {
	id := "legacy:" + ref
	if err := s.st.InsertNotificationChannel(NotificationChannel{ID: id, Label: label, Adapter: adapter, LegacyRef: ref, Config: `{}`, Enabled: true}); err != nil {
		return NotificationChannel{}, err
	}
	if _, err := s.st.exec(`INSERT INTO notification_config_mappings(legacy_key,channel_id) VALUES(?,?) ON CONFLICT(legacy_key) DO NOTHING`, ref, id); err != nil {
		return NotificationChannel{}, err
	}
	return s.st.NotificationChannel(id)
}
