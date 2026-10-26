package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var errAppPlatformActorUnavailable = errors.New("current application administrator authority is unavailable")

// A future management handler constructs this from authenticated session state,
// never from a request body or app bridge message. HTTP/session/enrollment and
// CSRF gates remain required before invoking these internal repositories.
type platformActorSession struct {
	username, created, sessionID string
	sessionRevision, generation  int64
	expires                      time.Time
}

type platformActorFacts struct {
	username, created, scope, authorityFingerprint string
	sessionRevision, generation, ouID              int64
}

type platformActorControl struct {
	epoch string
	nowMS int64
}

type platformActorAccount struct {
	username, created, role, zone, sessionID string
	sessionExpires                           int64
	sessionRevision, generation              int64
	groupID                                  int64
	restricted                               int
}

func platformDatabaseNow(e migExec) (int64, error) {
	q := `SELECT CAST((julianday('now')-2440587.5)*86400000 AS INTEGER)`
	if e.s.driver == "postgres" {
		q = `SELECT floor(extract(epoch FROM clock_timestamp())*1000)::bigint`
	}
	var now int64
	if err := e.queryRow(q).Scan(&now); err != nil {
		return 0, err
	}
	if now < 0 {
		return 0, errAppPlatformControlPaused
	}
	return now, nil
}

func platformReadActorAccount(e migExec, session platformActorSession, now int64, locked bool) (platformActorAccount, error) {
	return platformReadSessionAccount(e, session, now, locked, true)
}

// Launch readers use the same current account/session lifecycle checks without
// inheriting management authority. Only the management wrapper requires admin.
func platformReadSessionAccount(e migExec, session platformActorSession, now int64, locked, requireAdmin bool) (platformActorAccount, error) {
	var account platformActorAccount
	if session.username == "" || len(session.username) > 320 || len(session.created) > 64 || session.sessionID == "" || len(session.sessionID) > 128 || session.sessionRevision < 0 || session.generation < 0 || session.expires.IsZero() || session.expires.UnixMilli() <= now {
		return account, errAppPlatformActorUnavailable
	}
	var active int
	var expiry string
	var group sql.NullInt64
	lock := ""
	if locked && e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	if err := e.queryRow(`SELECT username,COALESCE(created_at,''),COALESCE(role,'user'),COALESCE(active,1),
        COALESCE(session_rev,0),group_id,COALESCE(expires_at,''),COALESCE(restricted,0) FROM users WHERE username=?`+lock, session.username).
		Scan(&account.username, &account.created, &account.role, &active, &account.sessionRevision, &group, &expiry, &account.restricted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return account, errAppPlatformActorUnavailable
		}
		return account, err
	}
	if (requireAdmin && account.role != "admin") || active != 1 || account.created != session.created || account.sessionRevision != session.sessionRevision {
		return account, errAppPlatformActorUnavailable
	}
	generation, err := notificationAccountGeneration(e, session.username)
	if err != nil {
		return account, err
	}
	if generation != session.generation {
		return account, errAppPlatformActorUnavailable
	}
	var sessionRow string
	if err := e.queryRow(`SELECT id FROM user_sessions WHERE id=? AND username=? AND revision=? AND expires_at=? AND expires_at>? AND revoked=0`+lock,
		session.sessionID, session.username, session.sessionRevision, session.expires.Unix(), now/1000).Scan(&sessionRow); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return account, errAppPlatformActorUnavailable
		}
		return account, err
	}
	account.sessionID, account.sessionExpires = sessionRow, session.expires.Unix()
	var zone sql.NullString
	err = e.queryRow(`SELECT v FROM meta WHERE k='timezone'`).Scan(&zone)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return account, err
	}
	loc := time.Local
	if zone.Valid && zone.String != "" {
		loc, err = time.LoadLocation(zone.String)
		if err != nil {
			return account, errAppPlatformActorUnavailable
		}
	}
	if expiry != "" {
		if _, err := time.Parse("2006-01-02", expiry); err != nil || time.UnixMilli(now).In(loc).Format("2006-01-02") > expiry {
			return account, errAppPlatformActorUnavailable
		}
	}
	account.zone, account.groupID, account.generation = loc.String(), group.Int64, generation
	return account, nil
}

func (s *Server) initializeAppPlatformControl(session platformActorSession) error {
	return s.withAppPlatformActor(session, true, nil)
}

func (s *Server) withAppPlatformActor(session platformActorSession, initialize bool, fn func(migExec, platformActorFacts, platformActorControl) error) error {
	return s.withAppPlatformActorContext(context.Background(), session, initialize, fn)
}

func (s *Server) withAppPlatformActorContext(ctx context.Context, session platformActorSession, initialize bool, fn func(migExec, platformActorFacts, platformActorControl) error) error {
	// Filter stale/non-admin proofs before lazy keyring creation. This unlocked
	// read is only a precheck; the authoritative check is repeated under the
	// shared principal-lifecycle lock below.
	pool := migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}}
	now, err := platformDatabaseNow(pool)
	if err != nil {
		return err
	}
	if _, err := platformReadActorAccount(pool, session, now, false); err != nil {
		return err
	}
	dek, err := s.dek()
	if err != nil {
		return err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	now, err = platformDatabaseNow(e)
	if err != nil {
		return err
	}
	account, err := platformReadActorAccount(e, session, now, true)
	if err != nil {
		return err
	}
	seed, epoch, err := platformControlIdentity(e, dek, initialize)
	if err != nil {
		return err
	}
	defer clear(seed)
	facts, err := platformActorAuthority(e, account, seed)
	if err != nil {
		return err
	}
	if fn != nil {
		if err := fn(e, facts, platformActorControl{epoch: epoch, nowMS: now}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func platformControlIdentity(e migExec, dek []byte, initialize bool) ([]byte, string, error) {
	rows, err := e.query(`SELECT k,v FROM meta WHERE k IN ('app_platform_actor_seed','app_platform_controller_epoch','app_platform_restore_paused')`)
	if err != nil {
		return nil, "", err
	}
	values := map[string]string{}
	for rows.Next() {
		var key string
		var value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return nil, "", err
		}
		if !value.Valid {
			rows.Close()
			return nil, "", errAppPlatformControlPaused
		}
		values[key] = value.String
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	if len(values) == 0 && initialize {
		var records int
		if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_installations)+(SELECT COUNT(*) FROM app_platform_review_plans)
            +(SELECT COUNT(*) FROM app_platform_operations)+(SELECT COUNT(*) FROM app_platform_runtime_bindings)`).Scan(&records); err != nil {
			return nil, "", err
		}
		if records != 0 {
			return nil, "", errAppPlatformControlPaused
		}
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return nil, "", err
		}
		sealed, err := aesSeal(dek, seed, aad("app-platform", "actor-seed"))
		if err != nil {
			return nil, "", err
		}
		epoch, err := platformNewUUID()
		if err != nil {
			return nil, "", err
		}
		values = map[string]string{"app_platform_actor_seed": sealPrefix + sealed, "app_platform_controller_epoch": epoch, "app_platform_restore_paused": "0"}
		for _, key := range []string{"app_platform_actor_seed", "app_platform_controller_epoch", "app_platform_restore_paused"} {
			if err := e.setSetting(key, values[key]); err != nil {
				return nil, "", err
			}
		}
	}
	sealed, epoch := values["app_platform_actor_seed"], values["app_platform_controller_epoch"]
	if len(values) != 3 || values["app_platform_restore_paused"] != "0" || !platformValidUUID(epoch) || !strings.HasPrefix(sealed, sealPrefix) {
		return nil, "", errAppPlatformControlPaused
	}
	seed, err := aesOpen(dek, strings.TrimPrefix(sealed, sealPrefix), aad("app-platform", "actor-seed"))
	if err != nil || len(seed) != 32 {
		return nil, "", errAppPlatformControlPaused
	}
	return seed, epoch, nil
}

func platformActorAuthority(e migExec, account platformActorAccount, seed []byte) (platformActorFacts, error) {
	identity, err := json.Marshal([]any{"portal-app-actor/v1", account.username, account.created, account.generation})
	if err != nil {
		return platformActorFacts{}, err
	}
	mac := hmac.New(sha256.New, seed)
	mac.Write(identity)
	return platformActorAuthorityForScope(e, account, hex.EncodeToString(mac.Sum(nil)))
}

func platformActorAuthorityForScope(e migExec, account platformActorAccount, scope string) (platformActorFacts, error) {
	var zero platformActorFacts
	snapshot, err := e.s.loadOUPolicySnapshotFrom(e.ex)
	if err != nil {
		return zero, err
	}
	group := account.groupID
	if group == 0 {
		group = snapshot.graph.defaultID
	}
	chain, err := snapshot.graph.policyChain(group)
	if err != nil {
		return zero, errAppPlatformActorUnavailable
	}
	type groupIdentity struct {
		ID, Parent int64
		Created    string
	}
	groups := make([]groupIdentity, 0, len(chain))
	for _, id := range chain {
		node := snapshot.graph.nodes[id]
		groups = append(groups, groupIdentity{ID: id, Parent: node.parent, Created: node.created})
	}
	policies, err := snapshot.resolve(group)
	if err != nil {
		return zero, errAppPlatformActorUnavailable
	}
	projection := struct {
		Scope, Role, Zone, SessionID string
		SessionExpires               int64
		SessionRevision              int64
		Restricted                   int
		Groups                       []groupIdentity
		Policies                     ouResolvedPolicies
	}{scope, account.role, account.zone, account.sessionID, account.sessionExpires, account.sessionRevision, account.restricted, groups, policies}
	raw, err := json.Marshal(projection)
	if err != nil {
		return zero, err
	}
	digest := sha256.Sum256(raw)
	return platformActorFacts{username: account.username, created: account.created, scope: scope, authorityFingerprint: hex.EncodeToString(digest[:]),
		sessionRevision: account.sessionRevision, generation: account.generation, ouID: group}, nil
}

// A resource/plan lock may outlive the initial authorization sample. Recheck
// expiry and authority after acquiring those locks, before any admission write.
func platformRefreshActor(e migExec, session platformActorSession, actor platformActorFacts, control platformActorControl) (platformActorControl, error) {
	now, err := platformDatabaseNow(e)
	if err != nil {
		return control, err
	}
	account, err := platformReadActorAccount(e, session, now, true)
	if err != nil {
		return control, err
	}
	current, err := platformActorAuthorityForScope(e, account, actor.scope)
	if err != nil {
		return control, err
	}
	if current != actor {
		return control, errAppPlatformPlanChanged
	}
	var markers int
	if err := e.queryRow(`SELECT COUNT(*) FROM meta WHERE (k='app_platform_controller_epoch' AND v=?) OR (k='app_platform_restore_paused' AND v='0')`, control.epoch).Scan(&markers); err != nil {
		return control, err
	}
	if markers != 2 {
		return control, errAppPlatformControlPaused
	}
	control.nowMS = now
	return control, nil
}
