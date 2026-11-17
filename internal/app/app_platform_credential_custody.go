package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var errAppPlatformCredentialUnavailable = errors.New("application credential custody unavailable or changed")

const platformCredentialLimit = 256
const platformCredentialValueLimit = 16384

type platformCredentialMetadata struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	State         string `json:"state"`
	ValueRevision int64  `json:"value_revision"`
	Revision      int64  `json:"revision"`
	CreatedMS     int64  `json:"created_ms"`
	UpdatedMS     int64  `json:"updated_ms"`
}

// Internal administration input, never a bridge/wire/diagnostic DTO. Explicit
// HTTP parsing and an exact affected-binding review must precede any public API.
type platformCredentialChange struct {
	kind, id, label         string
	revision, valueRevision int64
	value                   []byte
}

func (platformCredentialChange) MarshalJSON() ([]byte, error) {
	return nil, errAppPlatformCredentialUnavailable
}
func (*platformCredentialChange) UnmarshalJSON([]byte) error {
	return errAppPlatformCredentialUnavailable
}

func platformCredentialAAD(id string, revision int64) []byte {
	return aad("app-platform", "credential:"+id+":"+strconv.FormatInt(revision, 10))
}
func (s *Server) sealPlatformCredentialValue(id string, revision int64, value []byte) (string, error) {
	if !platformValidUUID(id) || revision < 1 || len(value) < 1 || len(value) > platformCredentialValueLimit {
		return "", errAppPlatformCredentialUnavailable
	}
	key, err := s.dek()
	if err != nil {
		return "", errAppPlatformCredentialUnavailable
	}
	sealed, err := aesSeal(key, value, platformCredentialAAD(id, revision))
	if err != nil || len(sealed)+len(sealPrefix) > 32768 {
		return "", errAppPlatformCredentialUnavailable
	}
	return sealPrefix + sealed, nil
}

// Custody primitive only. A future use adapter must prove current installation,
// principal, reviewed binding and network policy before calling this function.
// There is deliberately no HTTP/SDK/raw-read endpoint to this primitive.
func (s *Server) openPlatformCredentialValue(id string, revision int64, sealed string) ([]byte, error) {
	if !platformValidUUID(id) || revision < 1 || len(sealed) > 32768 || !strings.HasPrefix(sealed, sealPrefix) {
		return nil, errAppPlatformCredentialUnavailable
	}
	key, err := s.dek()
	if err != nil {
		return nil, errAppPlatformCredentialUnavailable
	}
	value, err := aesOpen(key, strings.TrimPrefix(sealed, sealPrefix), platformCredentialAAD(id, revision))
	if err != nil || len(value) < 1 || len(value) > platformCredentialValueLimit {
		clear(value)
		return nil, errAppPlatformCredentialUnavailable
	}
	return value, nil
}
func validPlatformCredentialLabel(label string) bool {
	if label == "" || len(label) > 160 || !utf8.ValidString(label) || strings.TrimSpace(label) != label {
		return false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Server) changePlatformCredential(session platformActorSession, change platformCredentialChange) (platformCredentialMetadata, error) {
	var result platformCredentialMetadata
	if len(change.value) > platformCredentialValueLimit {
		return result, errAppPlatformCredentialUnavailable
	}
	value := append([]byte(nil), change.value...)
	defer clear(value)
	if change.kind == "create" {
		if change.id != "" || change.revision != 0 || change.valueRevision != 0 || !validPlatformCredentialLabel(change.label) || len(value) == 0 {
			return result, errAppPlatformCredentialUnavailable
		}
	} else if (change.kind != "replace" && change.kind != "revoke") || !platformValidUUID(change.id) || change.revision < 1 || change.revision == math.MaxInt64 || change.valueRevision < 1 || change.valueRevision == math.MaxInt64 || change.label != "" || (change.kind == "replace" && len(value) == 0) || (change.kind == "revoke" && len(value) != 0) {
		return result, errAppPlatformCredentialUnavailable
	}
	err := s.withPlatformTrustTransactionContext(context.Background(), session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		result = platformCredentialMetadata{}
		// Same principal/trust lock order as installation admission. Custody writes
		// are serialized on both drivers, including creation capacity checks.
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		if change.kind == "create" {
			var count int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_credentials`).Scan(&count); err != nil {
				return err
			}
			if count >= platformCredentialLimit {
				return errAppPlatformCredentialUnavailable
			}
			id, err := platformNewUUID()
			if err != nil {
				return err
			}
			result = platformCredentialMetadata{ID: id, Label: change.label, State: "active", ValueRevision: 1, Revision: 1, CreatedMS: control.nowMS, UpdatedMS: control.nowMS}
		} else {
			// Fail closed while the separate affected-installation review/consume path
			// is unfinished. No bound credential can be rotated or revoked through an
			// unreviewed shortcut, and no current caller can activate a binding.
			var count int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_secret_bindings WHERE credential_id=?`, change.id).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return errAppPlatformCredentialUnavailable
			}
			lock := ""
			if e.s.driver == "postgres" {
				lock = " FOR UPDATE"
			}
			if err := e.queryRow(`SELECT id,label,state,value_revision,revision,created_ms,updated_ms FROM app_platform_credentials WHERE id=?`+lock, change.id).Scan(&result.ID, &result.Label, &result.State, &result.ValueRevision, &result.Revision, &result.CreatedMS, &result.UpdatedMS); err != nil {
				return errAppPlatformCredentialUnavailable
			}
			if result.State != "active" || result.Revision != change.revision || result.ValueRevision != change.valueRevision {
				return errAppPlatformCredentialUnavailable
			}
			result.ValueRevision++
			result.Revision++
		}
		var err error
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		result.UpdatedMS = control.nowMS
		if change.kind == "revoke" {
			result.State = "revoked"
			if _, err := e.exec(`UPDATE app_platform_credentials SET state='revoked',sealed_value=NULL,value_revision=?,revision=?,updated_ms=?,revoked_ms=? WHERE id=?`, result.ValueRevision, result.Revision, result.UpdatedMS, result.UpdatedMS, result.ID); err != nil {
				return err
			}
		} else {
			sealed, err := s.sealPlatformCredentialValue(result.ID, result.ValueRevision, value)
			if err != nil {
				return err
			}
			if change.kind == "create" {
				result.CreatedMS = control.nowMS
				_, err = e.exec(`INSERT INTO app_platform_credentials(id,label,state,sealed_value,value_revision,revision,created_ms,updated_ms,revoked_ms) VALUES(?,?,'active',?,1,1,?,?,NULL)`, result.ID, result.Label, sealed, result.CreatedMS, result.UpdatedMS)
			} else {
				_, err = e.exec(`UPDATE app_platform_credentials SET sealed_value=?,value_revision=?,revision=?,updated_ms=? WHERE id=?`, sealed, result.ValueRevision, result.Revision, result.UpdatedMS, result.ID)
			}
			if err != nil {
				return err
			}
		}
		detail, _ := json.Marshal(struct {
			Kind          string `json:"kind"`
			Revision      int64  `json:"revision"`
			ValueRevision int64  `json:"value_revision"`
		}{change.kind, result.Revision, result.ValueRevision})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.credential_changed','app_platform_credential',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, result.ID, string(detail)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformCredentialMetadata{}, err
	}
	return result, nil
}

func (s *Server) listPlatformCredentials(session platformActorSession, after string, limit int) ([]platformCredentialMetadata, error) {
	if limit < 1 || limit > 100 || (after != "" && !platformValidUUID(after)) {
		return nil, errAppPlatformCredentialUnavailable
	}
	var result []platformCredentialMetadata
	err := s.withAppPlatformActor(session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		rows, err := e.query(`SELECT id,label,state,value_revision,revision,created_ms,updated_ms FROM app_platform_credentials WHERE id>? ORDER BY id LIMIT ?`, after, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m platformCredentialMetadata
			if err := rows.Scan(&m.ID, &m.Label, &m.State, &m.ValueRevision, &m.Revision, &m.CreatedMS, &m.UpdatedMS); err != nil {
				rows.Close()
				return err
			}
			result = append(result, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = []platformCredentialMetadata{}
	}
	return result, nil
}
