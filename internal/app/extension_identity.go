package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Extension identity is prepared independently of connection storage. No endpoint
// exposes these facts until the app-bound backend registration is implemented.
type extensionIdentityBinding struct {
	Issuer     string
	AppID      string
	InstanceID string
	// Key is the stable identity seed, independent of the rotating assertion key.
	Key []byte
}

type extensionViewerFacts struct {
	Subject         string
	SessionRevision int64
	Role            string
	OUs             []string
}

var errExtensionIdentity = errors.New("extension viewer identity unavailable")

func extensionOpaqueIdentity(b extensionIdentityBinding, purpose string, fields ...string) string {
	input := append([]string{"rp-extension-identity-v1", b.Issuer, b.AppID, b.InstanceID, purpose}, fields...)
	raw, _ := json.Marshal(input)
	mac := hmac.New(sha256.New, b.Key)
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

// extensionViewerIdentity reads authority from one database snapshot, rather
// than reusing role/OU facts captured when an app was first opened. The caller
// must also verify current installation, token scopes and backend registration.
func (s *Store) extensionViewerIdentity(ctx context.Context, entry appTokenEntry, binding extensionIdentityBinding, now time.Time, loc *time.Location) (extensionViewerFacts, error) {
	var empty extensionViewerFacts
	if entry.username == "" || entry.appID != binding.AppID || !now.Before(entry.expires) || binding.Issuer == "" || binding.AppID == "" || binding.InstanceID == "" || len(binding.Key) < 32 || loc == nil {
		return empty, errExtensionIdentity
	}
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	var active int
	var revision int64
	var created, role, expiry string
	var primary sql.NullInt64
	err = e.queryRow(`SELECT COALESCE(active,1),COALESCE(session_rev,0),COALESCE(created_at,''),COALESCE(NULLIF(role,''),'user'),COALESCE(expires_at,''),group_id FROM users WHERE username=?`, entry.username).Scan(&active, &revision, &created, &role, &expiry, &primary)
	if err != nil {
		return empty, errExtensionIdentity
	}
	if expiry != "" {
		if _, err := time.Parse("2006-01-02", expiry); err != nil {
			return empty, errExtensionIdentity
		}
	}
	if active != 1 || revision != entry.sessionRev || created != entry.accountCreated || (expiry != "" && now.In(loc).Format("2006-01-02") > expiry) {
		return empty, errExtensionIdentity
	}
	generation, err := notificationAccountGeneration(e, entry.username)
	if err != nil {
		return empty, err
	}
	if generation != entry.accountGeneration {
		return empty, errExtensionIdentity
	}
	groupID := primary.Int64
	if !primary.Valid || groupID == 0 {
		rows, err := e.query(`SELECT id FROM user_groups WHERE is_default=1 ORDER BY id LIMIT 2`)
		if err != nil {
			return empty, err
		}
		defaults := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return empty, err
			}
			defaults = append(defaults, id)
		}
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			return empty, rowErr
		}
		if len(defaults) != 1 {
			return empty, errExtensionIdentity
		}
		groupID = defaults[0]
	}
	facts := extensionViewerFacts{Subject: extensionOpaqueIdentity(binding, "account", entry.username, created, strconv.FormatInt(generation, 10)), SessionRevision: revision, Role: role, OUs: []string{}}
	seen := map[int64]bool{}
	for groupID != 0 {
		if groupID < 0 || seen[groupID] || len(seen) >= 64 {
			return empty, errExtensionIdentity
		}
		seen[groupID] = true
		var parent sql.NullInt64
		var groupCreated string
		if err := e.queryRow(`SELECT COALESCE(created_at,''),parent_id FROM user_groups WHERE id=?`, groupID).Scan(&groupCreated, &parent); err != nil || groupCreated == "" {
			return empty, errExtensionIdentity
		}
		facts.OUs = append(facts.OUs, extensionOpaqueIdentity(binding, "ou", strconv.FormatInt(groupID, 10), groupCreated))
		groupID = parent.Int64
	}
	if len(facts.OUs) == 0 {
		return empty, errExtensionIdentity
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return facts, nil
}
