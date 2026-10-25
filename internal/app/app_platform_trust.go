package app

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errAppPlatformTrustConflict = errors.New("application publisher trust changed or conflicts")

type platformPublisherChange struct {
	kind, publisherID, keyID              string
	expectedRevision, expectedKeyRevision int64
	enabled                               bool
	namespaces                            []appplatform.PublisherNamespace
	key                                   *appplatform.PublisherKey
}
type platformPublisherTrustRevision struct {
	publisherID, keyID    string
	revision, keyRevision int64
}

// Private administration repository. No request body, app bridge or startup
// path can call this without a future authenticated management integration.
func (s *Server) changePlatformPublisherTrust(session platformActorSession, change platformPublisherChange) (platformPublisherTrustRevision, error) {
	var result platformPublisherTrustRevision
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		index := -1
		for i, p := range trust.Publishers {
			if p.ID == change.publisherID {
				index = i
				break
			}
		}
		registering := change.kind == "register"
		if registering {
			if index != -1 || change.expectedRevision != 0 || change.key == nil || change.keyID != "" || change.expectedKeyRevision != 0 {
				return errAppPlatformTrustConflict
			}
			trust.Publishers = append(trust.Publishers, appplatform.TrustedPublisher{ID: change.publisherID, Enabled: change.enabled, Revision: "1", Namespaces: change.namespaces})
			index = len(trust.Publishers) - 1
		} else if index < 0 || change.expectedRevision <= 0 || change.expectedRevision == math.MaxInt64 || trust.Publishers[index].Revision != strconv.FormatInt(change.expectedRevision, 10) {
			return errAppPlatformTrustConflict
		}
		publisher := &trust.Publishers[index]
		var changedKey *appplatform.PublisherKey
		switch change.kind {
		case "register", "add_key":
			if change.key == nil || change.key.Revision != "1" || change.key.RevokedMS != nil || change.keyID != "" || change.expectedKeyRevision != 0 {
				return errAppPlatformTrustConflict
			}
			if !registering && (change.enabled || change.namespaces != nil) {
				return errAppPlatformTrustConflict
			}
			for _, key := range publisher.Keys {
				if key.ID == change.key.ID {
					return errAppPlatformTrustConflict
				}
			}
			publisher.Keys = append(publisher.Keys, *change.key)
			changedKey = &publisher.Keys[len(publisher.Keys)-1]
		case "policy":
			if change.key != nil || change.keyID != "" || change.expectedKeyRevision != 0 {
				return errAppPlatformTrustConflict
			}
			publisher.Enabled, publisher.Namespaces = change.enabled, change.namespaces
		case "revoke_key":
			if change.key != nil || change.enabled || change.namespaces != nil || change.expectedKeyRevision <= 0 || change.expectedKeyRevision == math.MaxInt64 {
				return errAppPlatformTrustConflict
			}
			for i := range publisher.Keys {
				if publisher.Keys[i].ID == change.keyID {
					changedKey = &publisher.Keys[i]
					break
				}
			}
			if changedKey == nil || changedKey.Revision != strconv.FormatInt(change.expectedKeyRevision, 10) {
				return errAppPlatformTrustConflict
			}
			if changedKey.RevokedMS != nil {
				result = platformPublisherTrustRevision{publisherID: publisher.ID, keyID: changedKey.ID, revision: change.expectedRevision, keyRevision: change.expectedKeyRevision}
				return nil
			}
			revoked := strconv.FormatInt(control.nowMS, 10)
			changedKey.RevokedMS = &revoked
			changedKey.Revision = strconv.FormatInt(change.expectedKeyRevision+1, 10)
		default:
			return errAppPlatformTrustConflict
		}
		if !registering {
			publisher.Revision = strconv.FormatInt(change.expectedRevision+1, 10)
		}
		if err := appplatform.ValidatePublisherTrust(trust); err != nil {
			return errAppPlatformTrustConflict
		}
		namespaces := publisher.Namespaces
		if namespaces == nil {
			namespaces = []appplatform.PublisherNamespace{}
		}
		namespaceJSON, err := json.Marshal(namespaces)
		if err != nil {
			return err
		}
		if len(namespaceJSON) > 8192 {
			return errAppPlatformTrustConflict
		}
		revision, _ := strconv.ParseInt(publisher.Revision, 10, 64)
		if registering {
			_, err = e.exec(`INSERT INTO app_platform_publishers(id,namespaces_json,enabled,revision,created_ms,updated_ms) VALUES(?,?,?,?,?,?)`, publisher.ID, string(namespaceJSON), boolInt(publisher.Enabled), revision, control.nowMS, control.nowMS)
		} else {
			var updated sql.Result
			updated, err = e.exec(`UPDATE app_platform_publishers SET namespaces_json=?,enabled=?,revision=?,updated_ms=? WHERE id=? AND revision=?`, string(namespaceJSON), boolInt(publisher.Enabled), revision, control.nowMS, publisher.ID, change.expectedRevision)
			if err == nil {
				n, countErr := updated.RowsAffected()
				if countErr != nil {
					return countErr
				}
				if n != 1 {
					return errAppPlatformTrustConflict
				}
			}
		}
		if err != nil {
			return err
		}
		result = platformPublisherTrustRevision{publisherID: publisher.ID, revision: revision}
		if changedKey != nil {
			keyRevision, _ := strconv.ParseInt(changedKey.Revision, 10, 64)
			result.keyID, result.keyRevision = changedKey.ID, keyRevision
			if change.kind == "revoke_key" {
				var updated sql.Result
				updated, err = e.exec(`UPDATE app_platform_publisher_keys SET revoked_ms=?,revision=?,updated_ms=? WHERE publisher_id=? AND key_id=? AND revision=? AND revoked_ms IS NULL`, control.nowMS, keyRevision, control.nowMS, publisher.ID, changedKey.ID, change.expectedKeyRevision)
				if err == nil {
					n, countErr := updated.RowsAffected()
					if countErr != nil {
						return countErr
					}
					if n != 1 {
						return errAppPlatformTrustConflict
					}
				}
			} else {
				public, _ := base64.RawURLEncoding.Strict().DecodeString(changedKey.PublicKey)
				before, _ := strconv.ParseInt(changedKey.NotBeforeMS, 10, 64)
				var after any
				if changedKey.NotAfterMS != nil {
					n, _ := strconv.ParseInt(*changedKey.NotAfterMS, 10, 64)
					after = n
				}
				_, err = e.exec(`INSERT INTO app_platform_publisher_keys(publisher_id,key_id,algorithm,public_key,not_before_ms,not_after_ms,revoked_ms,revision,created_ms,updated_ms) VALUES(?,?,?,?,?,?,NULL,1,?,?)`, publisher.ID, changedKey.ID, changedKey.Algorithm, public, before, after, control.nowMS, control.nowMS)
			}
			if err != nil {
				return err
			}
		}
		if _, err := e.exec(`UPDATE app_platform_review_plans SET expires_ms=CASE WHEN expires_ms>? THEN ? ELSE expires_ms END,updated_ms=? WHERE publisher_id=? AND consumed_ms IS NULL`, control.nowMS, control.nowMS, control.nowMS, publisher.ID); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Kind        string `json:"kind"`
			Revision    int64  `json:"revision"`
			KeyID       string `json:"key_id,omitempty"`
			KeyRevision int64  `json:"key_revision,omitempty"`
		}{change.kind, revision, result.keyID, result.keyRevision})
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.publisher_trust_changed','app_platform_publisher',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, publisher.ID, string(detail))
		if err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformPublisherTrustRevision{}, err
	}
	return result, nil
}

func lockPlatformPublisherTrust(e migExec) error {
	if e.s.driver == "postgres" {
		_, err := e.exec(`LOCK TABLE app_platform_publishers,app_platform_publisher_keys IN SHARE ROW EXCLUSIVE MODE`)
		return err
	}
	_, err := e.exec(`UPDATE app_platform_publishers SET revision=revision WHERE 0`)
	return err
}

// The caller holds the trust transaction guard, so the publisher/key snapshot
// cannot mix policy revisions or miss a concurrent namespace insertion.
func loadPlatformPublisherTrust(e migExec) (appplatform.PublisherTrust, error) {
	var trust appplatform.PublisherTrust
	rows, err := e.query(`SELECT id,namespaces_json,enabled,revision FROM app_platform_publishers ORDER BY id LIMIT 129`)
	if err != nil {
		return trust, err
	}
	for rows.Next() {
		var p appplatform.TrustedPublisher
		var namespaces string
		var enabled int
		var revision int64
		if err := rows.Scan(&p.ID, &namespaces, &enabled, &revision); err != nil {
			rows.Close()
			return trust, err
		}
		p.Namespaces, err = appplatform.ParsePublisherNamespaces([]byte(namespaces))
		if err != nil {
			rows.Close()
			return trust, errAppPlatformTrustConflict
		}
		if enabled != 0 && enabled != 1 {
			rows.Close()
			return trust, errAppPlatformTrustConflict
		}
		p.Enabled, p.Revision = enabled == 1, strconv.FormatInt(revision, 10)
		trust.Publishers = append(trust.Publishers, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return trust, err
	}
	rows, err = e.query(`SELECT publisher_id,key_id,algorithm,public_key,not_before_ms,not_after_ms,revoked_ms,revision FROM app_platform_publisher_keys ORDER BY publisher_id,key_id LIMIT 257`)
	if err != nil {
		return trust, err
	}
	for rows.Next() {
		var publisher string
		var key appplatform.PublisherKey
		var public []byte
		var before, revision int64
		var after, revoked sql.NullInt64
		if err := rows.Scan(&publisher, &key.ID, &key.Algorithm, &public, &before, &after, &revoked, &revision); err != nil {
			rows.Close()
			return trust, err
		}
		key.PublicKey, key.NotBeforeMS, key.Revision = base64.RawURLEncoding.EncodeToString(public), strconv.FormatInt(before, 10), strconv.FormatInt(revision, 10)
		if after.Valid {
			v := strconv.FormatInt(after.Int64, 10)
			key.NotAfterMS = &v
		}
		if revoked.Valid {
			v := strconv.FormatInt(revoked.Int64, 10)
			key.RevokedMS = &v
		}
		found := false
		for i := range trust.Publishers {
			if trust.Publishers[i].ID == publisher {
				trust.Publishers[i].Keys = append(trust.Publishers[i].Keys, key)
				found = true
				break
			}
		}
		if !found {
			rows.Close()
			return trust, errAppPlatformTrustConflict
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return trust, err
	}
	if err := appplatform.ValidatePublisherTrust(trust); err != nil {
		return trust, errAppPlatformTrustConflict
	}
	return trust, nil
}

// These callbacks perform database-only work. Retrying a rolled-back contention
// failure must never repeat downloads, physical effects or external notifications.
func (s *Server) withPlatformTrustTransaction(session platformActorSession, fn func(migExec, platformActorFacts, platformActorControl) error) error {
	for attempt := 0; ; attempt++ {
		err := s.withAppPlatformActor(session, false, fn)
		if err == nil || !isContentionErr(err) || attempt == 5 {
			return err
		}
		time.Sleep(time.Duration(10<<attempt) * time.Millisecond)
	}
}
