//go:build linux && (amd64 || arm64)

package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func testNativePlatformNetworkHTTP(t *testing.T, s *Server, actor platformActorSession, installation string, binding platformBackendRequestBinding, endpoint platformRuntimeEndpoint) {
	t.Helper()
	// A fresh HTTP host shares the real current SQL/configuration, not a copied
	// Server mutex or an injected executor. Keep the original fixture host's
	// immutable own-backend bootstrap available for its independent later tests.
	host := &Server{st: s.st, cfg: s.cfg, loginThr: newLoginThrottle(), v1Rate: newRateLimiter()}
	mux := http.NewServeMux()
	host.wireRoutes(mux)
	cookie := host.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	path := "/api/app-platform/installations/" + installation + "/network"
	payload := func(target string) string {
		raw, err := json.Marshal(map[string]any{"binding": binding, "method": "GET", "url": "https://api.example.com" + target, "accept": "application/json", "contentType": "", "bodyBase64": ""})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	body := payload("/v1/success")
	baseline := platformNetworkFixtureCount(t)
	if w := platformBackendHTTPCall(mux, path, cookie, body, nil); w.Code != 503 || platformNetworkFixtureCount(t) != baseline {
		t.Fatal("authorized request with no trusted endpoint performed IO", w.Code)
	}
	if err := host.bindPlatformBackendHTTP(endpoint); err != nil {
		t.Fatal("network HTTP trusted bootstrap", err)
	}
	for _, bad := range []string{strings.Replace(body, `"generation":"1"`, `"generation":"2"`, 1), strings.Replace(body, `"policyRevision":"1"`, `"policyRevision":"2"`, 1)} {
		if w := platformBackendHTTPCall(mux, path, cookie, bad, nil); w.Code != 404 || platformNetworkFixtureCount(t) != baseline {
			t.Fatal("stale page reached HTTPS", w.Code)
		}
	}
	w := platformBackendHTTPCall(mux, path, cookie, body, map[string]string{"Sec-Fetch-Site": "same-origin"})
	var response struct {
		Status      int    `json:"status"`
		ContentType string `json:"contentType"`
		Body        string `json:"bodyBase64"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Status != 200 || response.ContentType != "application/json" || response.Body != base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`)) || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" || w.Header().Get("Cache-Control") != "no-store" || platformNetworkFixtureCount(t) != baseline+1 {
		t.Fatal("session HTTP did not reach current-viewer HTTPS", w.Code, w.Body.String())
	}
	t.Log("PORTAL_NETWORK_SESSION_HTTP")
	cancel, done := beginPlatformNetworkFixtureWithdrawal(t, s, installation)
	defer cancel()
	w = platformBackendHTTPCall(mux, path, cookie, payload("/v1/revoke"), nil)
	cancel()
	withdrawalErr := <-done
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
	var failure struct {
		Code      string `json:"code"`
		Retryable *bool  `json:"retryable"`
	}
	if withdrawalErr != nil || w.Code != 502 || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Code != "app_platform_request_uncertain" || failure.Retryable == nil || *failure.Retryable || strings.Contains(w.Body.String(), "bodyBase64") || w.Header().Get("Retry-After") != "" || platformNetworkFixtureCount(t) != baseline+2 {
		t.Fatal("session HTTP leaked/retried uncertain network result", withdrawalErr, w.Code, w.Body.String())
	}
	t.Log("PORTAL_NETWORK_SESSION_HTTP_UNCERTAIN")
	streamRaw, err := json.Marshal(map[string]any{
		"binding": binding, "method": "GET",
		"url":    "https://api.example.com/v1/stream",
		"accept": "application/x-ndjson", "contentType": "", "bodyBase64": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := platformBackendHTTPCall(mux, path+"/stream", cookie, string(streamRaw), map[string]string{"Sec-Fetch-Site": "same-origin"})
	var streamed []byte
	started, ended := false, false
	sequence := 0
	for _, line := range strings.Split(strings.TrimSpace(stream.Body.String()), "\n") {
		var frame struct {
			Type        string `json:"type"`
			Status      int    `json:"status"`
			ContentType string `json:"contentType"`
			Sequence    int    `json:"sequence"`
			Body        string `json:"bodyBase64"`
			Bytes       int64  `json:"bytes"`
			SHA         string `json:"sha256"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil {
			t.Fatal("invalid stream frame", line)
		}
		switch frame.Type {
		case "start":
			started = frame.Status == 200 && frame.ContentType == "application/x-ndjson"
		case "chunk":
			sequence++
			if frame.Sequence != sequence {
				t.Fatal("stream frame sequence changed")
			}
			chunk, err := base64.StdEncoding.DecodeString(frame.Body)
			if err != nil {
				t.Fatal(err)
			}
			streamed = append(streamed, chunk...)
		case "end":
			sum := sha256.Sum256(streamed)
			ended = frame.Sequence == sequence && frame.Bytes == int64(len(streamed)) && frame.SHA == hex.EncodeToString(sum[:])
		default:
			t.Fatal("unexpected stream frame", line)
		}
	}
	if stream.Code != 200 || stream.Header().Get("Content-Type") != "application/x-ndjson; charset=utf-8" || !started || !ended || string(streamed) != "one\ntwo\n" || platformNetworkFixtureCount(t) != 8 {
		t.Fatal("session stream did not reach current-viewer HTTPS", stream.Code, stream.Body.String())
	}
	t.Log("PORTAL_NETWORK_STREAM_SESSION_NDJSON")
}
