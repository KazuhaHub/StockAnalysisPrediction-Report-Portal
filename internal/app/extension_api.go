package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

type extensionPolicy struct {
	Version int `json:"version"`
	protocol.Policy
}
type extensionKey struct {
	KeyID        string `json:"key_id"`
	SigningKey   []byte `json:"signing_key"`
	IdentitySeed []byte `json:"identity_seed"`
}
type extensionBackend interface {
	Handshake(context.Context, protocol.Signer, time.Time) (string, error)
	Do(context.Context, string, string, []byte, string) (protocol.BackendResponse, error)
	Close()
}
type extensionCachedClient struct {
	revision int64
	instance string
	backend  extensionBackend
	used     time.Time
}
type extensionRuntime struct {
	mu            sync.Mutex
	clients       map[string]extensionCachedClient
	slots         chan struct{}
	clientForTest func(extensionConnection, extensionPolicy) (extensionBackend, error)
}

func (s *Store) extensionIssuer() (string, error) {
	_, err := s.exec(`INSERT INTO meta(k,v) VALUES('extension_issuer',?) ON CONFLICT(k) DO NOTHING`, randToken())
	if err != nil {
		return "", err
	}
	var value string
	err = s.queryRow(`SELECT v FROM meta WHERE k='extension_issuer'`).Scan(&value)
	if value == "" && err == nil {
		return "", errExtensionIdentity
	}
	return value, err
}
func (s *Server) extensionSecret(c extensionConnection) (extensionKey, error) {
	var key extensionKey
	plain, err := s.openSecret("app-backend:"+c.AppID, c.InstanceID, c.Secret)
	if err != nil {
		return key, err
	}
	if json.Unmarshal([]byte(plain), &key) != nil || key.KeyID == "" || len(key.SigningKey) != 32 || len(key.IdentitySeed) != 32 {
		return extensionKey{}, errExtensionIdentity
	}
	return key, nil
}
func extensionDecode(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return protocol.ErrInvalid
	}
	return nil
}
func extensionAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errExtensionConflict):
		jsonError(w, 409, "extension connection changed; reload before saving")
	case errors.Is(err, errExtensionNotFound), errors.Is(err, errExtensionMissingApp):
		jsonError(w, 404, "extension connection or installation not found")
	default:
		jsonError(w, 503, "extension connection unavailable")
	}
}
func (s *Server) extensionView(c extensionConnection, issuer string) map[string]any {
	u, _ := url.Parse(c.BaseURL)
	var policy extensionPolicy
	json.Unmarshal([]byte(c.Policy), &policy)
	return map[string]any{"app_id": c.AppID, "instance_id": c.InstanceID, "issuer": issuer, "label": c.Label, "base_url": u.Scheme + "://" + u.Host, "audience": c.Audience, "policy": policy, "enabled": c.Enabled, "revision": c.Revision, "has_key": c.Secret != "", "created_at": notificationTime(c.CreatedAt), "updated_at": notificationTime(c.UpdatedAt)}
}
func (s *Server) apiExtensionBackend(w http.ResponseWriter, r *http.Request, user string) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	if _, ok := s.st.GetApp(id); !ok {
		extensionAPIError(w, errExtensionMissingApp)
		return
	}
	current, loadErr := s.st.extensionConnection(id)
	if loadErr != nil && !errors.Is(loadErr, errExtensionNotFound) {
		extensionAPIError(w, loadErr)
		return
	}
	issuer, err := s.st.extensionIssuer()
	if err != nil {
		extensionAPIError(w, err)
		return
	}
	if r.Method == "GET" {
		if loadErr != nil {
			extensionAPIError(w, loadErr)
			return
		}
		writeJSON(w, s.extensionView(current, issuer))
		return
	}
	if r.Method == "DELETE" {
		var in struct {
			Revision int64 `json:"revision"`
		}
		if extensionDecode(w, r, 1024, &in) != nil || in.Revision <= 0 {
			jsonError(w, 400, "provide the current connection revision")
			return
		}
		if err := s.st.deleteExtensionConnection(id, in.Revision); err != nil {
			extensionAPIError(w, err)
			return
		}
		s.invalidateExtension(id)
		s.recordChange(r, user, AuditAppBackendDelete, "app", id, nil)
		writeJSON(w, okJSON)
		return
	}
	var in struct {
		Revision   int64           `json:"revision"`
		Label      string          `json:"label"`
		BaseURL    string          `json:"base_url"`
		Audience   string          `json:"audience"`
		Enabled    bool            `json:"enabled"`
		KeyID      string          `json:"key_id"`
		SigningKey string          `json:"signing_key"`
		Policy     extensionPolicy `json:"policy"`
	}
	if r.Method != "PUT" || extensionDecode(w, r, 32768, &in) != nil || in.Revision < 0 || strings.TrimSpace(in.Label) == "" || len(in.Label) > 120 || in.Audience == "" || len(in.Audience) > 256 || in.Policy.Version != 1 {
		jsonError(w, 400, "invalid connection configuration")
		return
	}
	client, err := protocol.NewBackendClient(in.BaseURL, in.Policy.Policy)
	if err != nil {
		jsonError(w, 400, "invalid destination or connection policy")
		return
	}
	client.Close()
	u, _ := url.Parse(in.BaseURL)
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !in.Policy.AllowsIP(ip) {
		jsonError(w, 400, "destination address is forbidden")
		return
	}
	c := current
	if loadErr != nil {
		c = extensionConnection{AppID: id, InstanceID: randToken()}
	}
	if (loadErr != nil && in.Revision != 0) || (loadErr == nil && in.Revision != c.Revision) {
		extensionAPIError(w, errExtensionConflict)
		return
	}
	var key extensionKey
	if c.Secret != "" {
		key, err = s.extensionSecret(c)
		if err != nil {
			extensionAPIError(w, err)
			return
		}
	}
	if len(key.IdentitySeed) == 0 {
		key.IdentitySeed = make([]byte, 32)
		if _, err := rand.Read(key.IdentitySeed); err != nil {
			extensionAPIError(w, err)
			return
		}
	}
	if in.SigningKey != "" {
		decoded, err := base64.StdEncoding.Strict().DecodeString(in.SigningKey)
		if err != nil || len(decoded) != 32 || in.KeyID == "" || len(in.KeyID) > 64 {
			jsonError(w, 400, "provide a key ID and a base64-encoded 32-byte signing key")
			return
		}
		key.SigningKey, key.KeyID = decoded, in.KeyID
	} else if in.KeyID != "" && in.KeyID != key.KeyID {
		jsonError(w, 400, "key ID changes require a signing key")
		return
	}
	if len(key.SigningKey) != 32 {
		jsonError(w, 400, "pairing requires a signing key")
		return
	}
	raw, _ := json.Marshal(key)
	sealed, err := s.sealSecret("app-backend:"+id, c.InstanceID, string(raw))
	if err != nil {
		extensionAPIError(w, err)
		return
	}
	policy, _ := json.Marshal(in.Policy)
	c.Label, c.BaseURL, c.Audience, c.Secret, c.Policy, c.Enabled = in.Label, in.BaseURL, in.Audience, sealed, string(policy), in.Enabled
	if err := s.st.saveExtensionConnection(c, in.Revision); err != nil {
		extensionAPIError(w, err)
		return
	}
	s.invalidateExtension(id)
	saved, err := s.st.extensionConnection(id)
	if err != nil {
		extensionAPIError(w, err)
		return
	}
	s.recordChange(r, user, AuditAppBackendChange, "app", id, map[string]any{"revision": saved.Revision, "enabled": saved.Enabled, "key_rotated": in.SigningKey != ""})
	writeJSON(w, s.extensionView(saved, issuer))
}

func (s *Server) invalidateExtension(id string) {
	s.extensions.mu.Lock()
	defer s.extensions.mu.Unlock()
	if old, ok := s.extensions.clients[id]; ok {
		old.backend.Close()
		delete(s.extensions.clients, id)
	}
}
func (s *Server) extensionClient(c extensionConnection, p extensionPolicy) (extensionBackend, error) {
	rt := &s.extensions
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if old, ok := rt.clients[c.AppID]; ok {
		if old.revision == c.Revision && old.instance == c.InstanceID {
			old.used = time.Now()
			rt.clients[c.AppID] = old
			return old.backend, nil
		}
		old.backend.Close()
		delete(rt.clients, c.AppID)
	}
	var client extensionBackend
	var err error
	if rt.clientForTest != nil {
		client, err = rt.clientForTest(c, p)
	} else {
		client, err = protocol.NewBackendClient(c.BaseURL, p.Policy)
	}
	if err != nil {
		return nil, err
	}
	if rt.clients == nil {
		rt.clients = make(map[string]extensionCachedClient)
	}
	if len(rt.clients) >= 64 {
		var victim string
		var oldest time.Time
		for id, entry := range rt.clients {
			if victim == "" || entry.used.Before(oldest) {
				victim = id
				oldest = entry.used
			}
		}
		rt.clients[victim].backend.Close()
		delete(rt.clients, victim)
	}
	rt.clients[c.AppID] = extensionCachedClient{revision: c.Revision, instance: c.InstanceID, backend: client, used: time.Now()}
	return client, nil
}
func (s *Server) acquireExtensionSlot() (func(), bool) {
	rt := &s.extensions
	rt.mu.Lock()
	if rt.slots == nil {
		rt.slots = make(chan struct{}, 16)
	}
	slots := rt.slots
	rt.mu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return nil, false
	}
}

// The extension proxy accepts only app-bound tokens. Machine tokens, including
// all-scoped tokens, cannot supply current viewer authority.
func (s *Server) v1ExtensionRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	deny := func() { v1err(w, 403, "extension_forbidden", "the current app and viewer are not authorized") }
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || s.appTok == nil {
		deny()
		return
	}
	entry, ok := s.appTok.extensionEntry(token, time.Now())
	if !ok {
		// Unknown, expired or revoked (an app closed for the viewer revokes its tokens): the same 401
		// the bridge answers, which is the signal for the host page to mint a new token. A token that
		// is live but not allowed stays a 403.
		v1err(w, http.StatusUnauthorized, "unauthorized", "the app token is missing, expired or revoked: mint a new one")
		return
	}
	if entry.appID != r.PathValue("id") || !entry.accountGenerationKnown {
		deny()
		return
	}
	var in struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body,omitempty"`
	}
	if extensionDecode(w, r, 70000, &in) != nil {
		v1err(w, 400, "invalid_param", "provide one bounded extension request")
		return
	}
	need := "extension.read"
	if in.Method != "GET" {
		need = "extension.manage"
	}
	if open, err := s.st.appOpenForKey(entry.username, entry.appID); err != nil || !open { // ADR 0048: re-checked on every request
		if err == nil {
			v1err(w, http.StatusForbidden, "app_closed", "app_closed")
		} else {
			deny()
		}
		return
	}
	app, installed := s.st.GetApp(entry.appID)
	if !installed || !entry.scopes[need] || !slices.Contains(app.Scopes, need) {
		deny()
		return
	}
	c, err := s.st.extensionConnection(entry.appID)
	if err != nil || !c.Enabled || c.Secret == "" || entry.backendInstance != c.InstanceID {
		deny()
		return
	}
	var policy extensionPolicy
	if json.Unmarshal([]byte(c.Policy), &policy) != nil || policy.Version != 1 || !policy.Allows(in.Method, in.Path) || strings.HasPrefix(in.Path, "/v1/handshake") || (in.Method == "GET" && len(in.Body) > 0) || len(in.Body) > 65536 {
		v1err(w, 400, "invalid_param", "extension route or body is not permitted")
		return
	}
	issuer, err := s.st.extensionIssuer()
	if err != nil {
		deny()
		return
	}
	key, err := s.extensionSecret(c)
	if err != nil {
		deny()
		return
	}
	binding := extensionIdentityBinding{Issuer: issuer, AppID: c.AppID, InstanceID: c.InstanceID, Key: key.IdentitySeed}
	facts, err := s.st.extensionViewerIdentity(r.Context(), entry, binding, time.Now(), s.panelLocation())
	if err != nil || (need == "extension.manage" && facts.Role != "admin") {
		deny()
		return
	}
	holds, err := s.mustEnroll(entry.username)
	if err != nil || holds {
		deny()
		return
	}
	release, ok := s.acquireExtensionSlot()
	if !ok {
		v1err(w, 429, "extension_busy", "extension capacity unavailable")
		return
	}
	defer release()
	client, err := s.extensionClient(c, policy)
	if err != nil {
		v1err(w, 503, "extension_unavailable", "extension backend unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	signer := protocol.Signer{Issuer: issuer, Audience: c.Audience, AppID: c.AppID, InstanceID: c.InstanceID, KeyID: key.KeyID, Key: key.SigningKey}
	boot, err := client.Handshake(ctx, signer, time.Now())
	if errors.Is(err, protocol.ErrBusy) {
		v1err(w, 429, "extension_busy", "extension capacity unavailable")
		return
	}
	if err != nil {
		v1err(w, 502, "extension_unavailable", "extension backend unavailable")
		return
	}
	signer.BootID = boot
	// Repeat authority after the network handshake. A changed connection or viewer
	// must not dispatch a request that waited under an older policy.
	fresh, err := s.st.extensionConnection(c.AppID)
	if err != nil || !fresh.Enabled || fresh.Revision != c.Revision || fresh.InstanceID != c.InstanceID {
		deny()
		return
	}
	facts, err = s.st.extensionViewerIdentity(ctx, entry, binding, time.Now(), s.panelLocation())
	app, installed = s.st.GetApp(entry.appID)
	if err != nil || !installed || !slices.Contains(app.Scopes, need) || (need == "extension.manage" && facts.Role != "admin") {
		deny()
		return
	}
	holds, err = s.mustEnroll(entry.username)
	if err != nil || holds {
		deny()
		return
	}
	assertion, err := signer.Sign(in.Method, in.Path, in.Body, protocol.Viewer{Subject: facts.Subject, SessionRevision: facts.SessionRevision, Role: facts.Role, OUs: facts.OUs}, time.Now())
	if err != nil {
		deny()
		return
	}
	result, err := client.Do(ctx, in.Method, in.Path, in.Body, assertion)
	if errors.Is(err, protocol.ErrBusy) {
		v1err(w, 429, "extension_busy", "extension capacity unavailable")
		return
	}
	if err != nil || len(result.Body) > 1048576 || !json.Valid(result.Body) {
		v1err(w, 502, "extension_unavailable", "extension backend unavailable or returned an invalid response")
		return
	}
	if result.Status < 200 || result.Status >= 300 {
		switch result.Status {
		case 400, 403, 404, 409, 422, 429:
			v1err(w, result.Status, "extension_backend_rejected", "extension backend refused the request")
		default:
			v1err(w, 502, "extension_unavailable", "extension backend unavailable")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(result.Status)
	w.Write(bytes.Clone(result.Body))
}
