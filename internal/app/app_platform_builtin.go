package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The built-in official catalogue and publisher (doc 189). The publisher is ordinary trust rows, because many
// authority paths join the trust tables; Portal writes them itself, at initialization and at startup, and never
// rewrites what an administrator changed afterwards.

type platformBuiltinKey struct{ id, publicKey string }

type platformBuiltinPublisher struct {
	id, displayName string
	namespaces      []appplatform.PublisherNamespace
	keys            []platformBuiltinKey
}

const (
	platformOfficialCatalogueURL  = "https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/catalogue/catalogue.json"
	platformOfficialCatalogueName = "Kazuha Hub"
	platformOfficialCatalogueKey  = "app_platform_official_catalogue"
)

// Rotation: a release appends the new key (ID <publisher>-YYYY-MM); the old one stays until an administrator
// revokes it.
var platformOfficialPublishers = []platformBuiltinPublisher{{
	id:          "kazuha-hub",
	displayName: "Kazuha Hub",
	namespaces:  []appplatform.PublisherNamespace{{Kind: "prefix", Value: "kazuha-hub-"}},
	keys:        []platformBuiltinKey{{id: "kazuha-hub-2026-11", publicKey: "Z9uDuPmR0UIyQX3JIqgpKdFhQzqwJKMqHMBsNWvdIOk"}},
}}

func (s *Server) platformBuiltin(id string) *platformBuiltinPublisher {
	for i := range s.platformBuiltinPublishers {
		if s.platformBuiltinPublishers[i].id == id {
			return &s.platformBuiltinPublishers[i]
		}
	}
	return nil
}

func platformSameNamespaces(a, b []appplatform.PublisherNamespace) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// platformBuiltinMatches reports whether a trust row is the built-in publisher: exactly its namespaces, and every
// key the row holds is a built-in key with the same public key (an administrator may have revoked some). Built-in
// keys the row lacks are a rotation ensure adds. An unrevoked key of any other ID makes the row someone else's;
// revoking it is how an administrator hands a hand-registered row over to the built-in one.
func platformBuiltinMatches(b platformBuiltinPublisher, p appplatform.TrustedPublisher) bool {
	if !platformSameNamespaces(b.namespaces, p.Namespaces) {
		return false
	}
	for _, have := range p.Keys {
		known := false
		for _, want := range b.keys {
			known = known || (have.ID == want.id && have.Algorithm == "Ed25519" && have.PublicKey == want.publicKey)
		}
		if !known && (have.RevokedMS == nil || platformBuiltinKeyID(b, have.ID)) {
			return false
		}
	}
	return true
}

func platformBuiltinKeyID(b platformBuiltinPublisher, id string) bool {
	for _, k := range b.keys {
		if k.id == id {
			return true
		}
	}
	return false
}

func platformControlActive(e migExec) (bool, error) {
	var epoch, paused, seed sql.NullString
	if err := e.queryRow(`SELECT (SELECT v FROM meta WHERE k='app_platform_controller_epoch'),(SELECT v FROM meta WHERE k='app_platform_restore_paused'),(SELECT v FROM meta WHERE k='app_platform_actor_seed')`).Scan(&epoch, &paused, &seed); err != nil {
		return false, err
	}
	return epoch.Valid && platformValidUUID(epoch.String) && paused.Valid && paused.String == "0" && seed.Valid && strings.HasPrefix(seed.String, sealPrefix), nil
}

// ensurePlatformBuiltinTrust is the startup step: on an initialized, unpaused deployment it seeds or completes
// the built-in publishers. It does nothing before initialization, whose own transaction seeds them.
func (s *Server) ensurePlatformBuiltinTrust(ctx context.Context) error {
	if len(s.platformBuiltinPublishers) == 0 {
		return nil
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return err
	}
	active, err := platformControlActive(e)
	if err != nil || !active {
		return err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return err
	}
	if err := s.ensurePlatformBuiltinTrustTx(e, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ensurePlatformBuiltinTrustLogged runs ensure after a commit that may have unblocked it; a failure leaves trust
// as it was and is only logged, since the caller's own change has already succeeded.
func (s *Server) ensurePlatformBuiltinTrustLogged(ctx context.Context) {
	if err := s.ensurePlatformBuiltinTrust(context.WithoutCancel(ctx)); err != nil {
		log.Printf("app platform: built-in publisher check failed: %v", err)
	}
}

// ensurePlatformBuiltinTrustTx needs the trust lock held. A conflict writes nothing and is reported by the
// publisher directory; the enabled flag, revocations and namespaces of an existing row are never rewritten.
func (s *Server) ensurePlatformBuiltinTrustTx(e migExec, now int64) error {
	for _, b := range s.platformBuiltinPublishers {
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			return err
		}
		var row *appplatform.TrustedPublisher
		for i := range trust.Publishers {
			if trust.Publishers[i].ID == b.id {
				row = &trust.Publishers[i]
			}
		}
		if row == nil {
			candidate := appplatform.TrustedPublisher{ID: b.id, Enabled: true, Revision: "1", Namespaces: b.namespaces}
			for _, k := range b.keys {
				candidate.Keys = append(candidate.Keys, appplatform.PublisherKey{ID: k.id, Algorithm: "Ed25519", PublicKey: k.publicKey, NotBeforeMS: "0", Revision: "1"})
			}
			trust.Publishers = append(trust.Publishers, candidate)
			if appplatform.ValidatePublisherTrust(trust) != nil {
				log.Printf("app platform: built-in publisher %s not trusted: adding it would make publisher trust invalid (a namespace overlap or a trust limit)", b.id)
				continue
			}
			namespaces, _ := json.Marshal(b.namespaces)
			if _, err := e.exec(`INSERT INTO app_platform_publishers(id,namespaces_json,enabled,revision,created_ms,updated_ms) VALUES(?,?,1,1,?,?)`, b.id, string(namespaces), now, now); err != nil {
				return err
			}
			for _, k := range b.keys {
				if err := platformInsertBuiltinKey(e, b.id, k, now); err != nil {
					return err
				}
			}
			if err := platformAuditBuiltin(e, b.id, 1, "", now); err != nil {
				return err
			}
			continue
		}
		if !platformBuiltinMatches(b, *row) {
			log.Printf("app platform: publisher %s is registered with another namespace or key than the built-in one; left unchanged", b.id)
			continue
		}
		revision, _ := strconv.ParseInt(row.Revision, 10, 64)
		for _, k := range b.keys {
			held := false
			for _, have := range row.Keys {
				held = held || have.ID == k.id
			}
			if held {
				continue
			}
			candidate := trust
			candidate.Publishers = append([]appplatform.TrustedPublisher(nil), trust.Publishers...)
			for i := range candidate.Publishers {
				if candidate.Publishers[i].ID == b.id {
					candidate.Publishers[i].Keys = append(append([]appplatform.PublisherKey(nil), candidate.Publishers[i].Keys...), appplatform.PublisherKey{ID: k.id, Algorithm: "Ed25519", PublicKey: k.publicKey, NotBeforeMS: "0", Revision: "1"})
				}
			}
			if appplatform.ValidatePublisherTrust(candidate) != nil {
				log.Printf("app platform: built-in key %s of %s not added: trust would be invalid", k.id, b.id)
				continue
			}
			updated, err := e.exec(`UPDATE app_platform_publishers SET revision=?,updated_ms=? WHERE id=? AND revision=?`, revision+1, now, b.id, revision)
			if err != nil {
				return err
			}
			if n, err := updated.RowsAffected(); err != nil || n != 1 {
				return errAppPlatformTrustConflict
			}
			revision++
			if err := platformInsertBuiltinKey(e, b.id, k, now); err != nil {
				return err
			}
			// As for a manual key addition, open reviews were made against the previous trust.
			if _, err := e.exec(`UPDATE app_platform_review_plans SET expires_ms=CASE WHEN expires_ms>? THEN ? ELSE expires_ms END,updated_ms=? WHERE publisher_id=? AND consumed_ms IS NULL`, now, now, now, b.id); err != nil {
				return err
			}
			if err := platformAuditBuiltin(e, b.id, revision, k.id, now); err != nil {
				return err
			}
			trust = candidate
		}
	}
	return nil
}

func platformInsertBuiltinKey(e migExec, publisher string, k platformBuiltinKey, now int64) error {
	public, err := base64.RawURLEncoding.Strict().DecodeString(k.publicKey)
	if err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO app_platform_publisher_keys(publisher_id,key_id,algorithm,public_key,not_before_ms,not_after_ms,revoked_ms,revision,created_ms,updated_ms) VALUES(?,?,'Ed25519',?,0,NULL,NULL,1,?,?)`, publisher, k.id, public, now, now)
	return err
}

func platformAuditBuiltin(e migExec, publisher string, revision int64, keyID string, now int64) error {
	detail, _ := json.Marshal(struct {
		Kind     string `json:"kind"`
		Revision int64  `json:"revision"`
		KeyID    string `json:"key_id,omitempty"`
	}{"builtin", revision, keyID})
	_, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.publisher_trust_changed','app_platform_publisher',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), publisher, string(detail))
	return err
}

// platformBuiltinChangeAllowed refuses administrator changes that would take over or re-scope the built-in
// publisher: its ID is reserved while it has no row, its keys come from releases and its namespaces are fixed
// while the row is the built-in one. Disable, enable and revoke stay available. A conflicting row with the
// built-in ID is the administrator's own, and every change to it stays open, so it can be fixed or disabled.
// row is the current trust row of change.publisherID, or nil.
func (s *Server) platformBuiltinChangeAllowed(change platformPublisherChange, row *appplatform.TrustedPublisher) bool {
	b := s.platformBuiltin(change.publisherID)
	if b == nil {
		return true
	}
	if row == nil {
		return change.kind != "register"
	}
	if !platformBuiltinMatches(*b, *row) {
		return true
	}
	switch change.kind {
	case "policy":
		return platformSameNamespaces(b.namespaces, change.namespaces)
	case "revoke_key":
		return true
	}
	return false
}

type platformPublisherView struct {
	appplatform.TrustedPublisher
	Builtin     bool   `json:"builtin,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// platformPublisherViews marks the rows that are the built-in publishers and reports whether any built-in
// publisher is missing or conflicting on an active deployment.
func (s *Server) platformPublisherViews(trust appplatform.PublisherTrust, active bool) ([]platformPublisherView, bool) {
	views := make([]platformPublisherView, 0, len(trust.Publishers))
	conflict := false
	for _, b := range s.platformBuiltinPublishers {
		found := false
		for _, p := range trust.Publishers {
			if p.ID == b.id {
				found = true
				conflict = conflict || !platformBuiltinMatches(b, p)
			}
		}
		conflict = conflict || (active && !found)
	}
	for _, p := range trust.Publishers {
		view := platformPublisherView{TrustedPublisher: p}
		if b := s.platformBuiltin(p.ID); b != nil && platformBuiltinMatches(*b, p) {
			view.Builtin, view.DisplayName = true, b.displayName
		}
		views = append(views, view)
	}
	return views, conflict
}

func (s *Server) platformOfficialCatalogueEnabled(e migExec) (bool, error) {
	var value sql.NullString
	if err := e.queryRow(`SELECT (SELECT v FROM meta WHERE k=?)`, platformOfficialCatalogueKey).Scan(&value); err != nil {
		return false, err
	}
	return !value.Valid || value.String != "disabled", nil
}

func (s *Server) apiPlatformMarketSources(w http.ResponseWriter, r *http.Request, user string) {
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var official any
	err = s.withAppPlatformActorContextMode(r.Context(), actor, false, true, func(e migExec, facts platformActorFacts, control platformActorControl) error {
		if _, err := platformRefreshActor(e, actor, facts, control); err != nil {
			return err
		}
		if s.platformOfficialCatalogue == "" {
			return nil
		}
		enabled, err := s.platformOfficialCatalogueEnabled(e)
		official = map[string]any{"name": platformOfficialCatalogueName, "url": s.platformOfficialCatalogue, "enabled": enabled}
		return err
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"official": official})
}

func (s *Server) apiPlatformMarketSourcesChange(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "official_enabled")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	raw := strings.TrimSpace(string(fields["official_enabled"]))
	enabled := raw == "true"
	if (raw != "true" && raw != "false") || s.platformOfficialCatalogue == "" {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	err = s.withAppPlatformActorContext(r.Context(), actor, false, func(e migExec, facts platformActorFacts, control platformActorControl) error {
		if _, err := platformRefreshActor(e, actor, facts, control); err != nil {
			return err
		}
		current, err := s.platformOfficialCatalogueEnabled(e)
		if err != nil || current == enabled {
			return err
		}
		if enabled {
			_, err = e.exec(`DELETE FROM meta WHERE k=?`, platformOfficialCatalogueKey)
		} else {
			_, err = e.exec(`INSERT INTO meta(k,v) VALUES(?,'disabled') ON CONFLICT(k) DO UPDATE SET v=excluded.v`, platformOfficialCatalogueKey)
		}
		if err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"source": "official", "url": s.platformOfficialCatalogue, "enabled": enabled})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.market_source_changed','app_platform_market_source','official',?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), facts.username, facts.ouID, string(detail)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, actor, facts, control)
		return err
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"official_enabled": enabled})
}

// seedPlatformBuiltinTrust runs inside the initialization transaction, after its no-history check.
func (s *Server) seedPlatformBuiltinTrust(e migExec, now int64) error {
	if len(s.platformBuiltinPublishers) == 0 {
		return nil
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return err
	}
	return s.ensurePlatformBuiltinTrustTx(e, now)
}
