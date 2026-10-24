package app

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config contains non-secret adapter settings only. Secret is a sealed value or
// an empty value for channels referring to pre-existing credential storage.
type NotificationChannel struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Adapter   string `json:"adapter"`
	LegacyRef string `json:"legacy_ref"`
	Config    string `json:"config"`
	Secret    string `json:"-"`
	Enabled   bool   `json:"enabled"`
	Paused    bool   `json:"paused"`
	Revision  int64  `json:"revision"`
	CreatedAt int64  `json:"-"`
}

const notificationChannelCols = `id,label,adapter,legacy_ref,config,secret,enabled,paused,revision,created_at`

func scanNotificationChannel(row interface{ Scan(...any) error }) (NotificationChannel, error) {
	var c NotificationChannel
	err := row.Scan(&c.ID, &c.Label, &c.Adapter, &c.LegacyRef, &c.Config, &c.Secret, &c.Enabled, &c.Paused, &c.Revision, &c.CreatedAt)
	return c, err
}

func (s *Store) NotificationChannel(id string) (NotificationChannel, error) {
	return scanNotificationChannel(s.queryRow(`SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=?`, id))
}

func (s *Store) NotificationChannels() ([]NotificationChannel, error) {
	rows, err := s.query(`SELECT ` + notificationChannelCols + ` FROM notification_channels ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationChannel{}
	for rows.Next() {
		c, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) InsertNotificationChannel(c NotificationChannel) error {
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().UnixMilli()
	}
	if c.Config == "" {
		c.Config = "{}"
	}
	_, err := s.exec(`INSERT INTO notification_channels(id,label,adapter,legacy_ref,config,secret,enabled,paused,revision,created_at) VALUES(?,?,?,?,?,?,?,?,1,?) ON CONFLICT(id) DO NOTHING`, c.ID, c.Label, c.Adapter, c.LegacyRef, c.Config, c.Secret, boolInt(c.Enabled), boolInt(c.Paused), c.CreatedAt)
	return err
}

func (s *Store) UpdateNotificationChannel(c NotificationChannel, expected int64) error {
	res, err := s.exec(`UPDATE notification_channels SET label=?,config=?,secret=?,enabled=?,paused=?,revision=revision+1 WHERE id=? AND revision=?`, c.Label, c.Config, c.Secret, boolInt(c.Enabled), boolInt(c.Paused), c.ID, expected)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errNotificationConflict
	}
	return nil
}

func (s *Store) NotificationEvent(id string) (NotificationEvent, error) {
	var e NotificationEvent
	err := s.queryRow(`SELECT id,source_key,event_type,payload,resource_type,resource_id,created_at,expires_at FROM notification_events WHERE id=?`, id).Scan(&e.ID, &e.SourceKey, &e.Type, &e.Payload, &e.ResourceType, &e.ResourceID, &e.CreatedAt, &e.ExpiresAt)
	return e, err
}

type NotificationRoute struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	ChannelID     string   `json:"channel_id"`
	Events        string   `json:"events"`
	RecipientMode string   `json:"recipient_mode"`
	Enabled       bool     `json:"enabled"`
	Mandatory     bool     `json:"mandatory"`
	Revision      int64    `json:"revision"`
	Grants        []string `json:"grants"`
}

func (s *Store) NotificationRoutes() ([]NotificationRoute, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	rows, err := e.query(`SELECT id,label,channel_id,events,recipient_mode,enabled,mandatory,revision FROM notification_routes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := []NotificationRoute{}
	for rows.Next() {
		var r NotificationRoute
		if err := rows.Scan(&r.ID, &r.Label, &r.ChannelID, &r.Events, &r.RecipientMode, &r.Enabled, &r.Mandatory, &r.Revision); err != nil {
			rows.Close()
			return nil, err
		}
		r.Grants = []string{}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		rows, err := e.query(`SELECT principal FROM notification_route_grants WHERE route_id=? ORDER BY principal`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			out[i].Grants = append(out[i].Grants, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) SaveNotificationRoute(r NotificationRoute, expected int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780716)`); err != nil {
			return err
		}
	}
	var channel string
	if err := e.queryRow(`SELECT id FROM notification_channels WHERE id=?`, r.ChannelID).Scan(&channel); err != nil {
		return errNotificationConflict
	}
	grants := append([]string{}, r.Grants...)
	sort.Strings(grants)
	for _, p := range grants {
		var query string
		var arg any
		switch {
		case strings.HasPrefix(p, "u:"):
			query = `SELECT username FROM users WHERE username=?`
			arg = strings.TrimPrefix(p, "u:")
		case strings.HasPrefix(p, "g:"):
			id, err := strconv.ParseInt(strings.TrimPrefix(p, "g:"), 10, 64)
			if err != nil {
				return errNotificationConflict
			}
			query = `SELECT id FROM user_groups WHERE id=?`
			arg = id
		default:
			return errNotificationConflict
		}
		var value any
		if err := e.queryRow(query, arg).Scan(&value); err != nil {
			return errNotificationConflict
		}
	}
	if expected == 0 {
		var n int
		if err := e.queryRow(`SELECT COUNT(*) FROM notification_routes`).Scan(&n); err != nil {
			return err
		}
		if n >= 100 {
			return errNotificationCapacity
		}
		_, err = e.exec(`INSERT INTO notification_routes(id,label,channel_id,events,recipient_mode,enabled,mandatory,revision) VALUES(?,?,?,?,?,?,?,1)`, r.ID, r.Label, r.ChannelID, r.Events, r.RecipientMode, boolInt(r.Enabled), boolInt(r.Mandatory))
	} else {
		var res sql.Result
		res, err = e.exec(`UPDATE notification_routes SET label=?,channel_id=?,events=?,recipient_mode=?,enabled=?,mandatory=?,revision=revision+1 WHERE id=? AND revision=?`, r.Label, r.ChannelID, r.Events, r.RecipientMode, boolInt(r.Enabled), boolInt(r.Mandatory), r.ID, expected)
		if err == nil {
			n, _ := res.RowsAffected()
			if n != 1 {
				return errNotificationConflict
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err := e.exec(`DELETE FROM notification_route_grants WHERE route_id=?`, r.ID); err != nil {
		return err
	}
	for _, p := range r.Grants {
		if _, err := e.exec(`INSERT INTO notification_route_grants(route_id,principal) VALUES(?,?) ON CONFLICT(route_id,principal) DO NOTHING`, r.ID, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}
