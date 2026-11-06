package appplatform

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

func backendIdentityFixture() BackendIdentity {
	return BackendIdentity{AppID: "notes-sample", DeploymentID: "00000000-0000-4000-8000-000000000001", ControllerEpoch: "00000000-0000-4000-8000-000000000002", InstallationID: "00000000-0000-4000-8000-000000000003", Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64)}
}
func TestBackendRegistrationProtocolBinding(t *testing.T) {
	seed := bytes.Repeat([]byte{17}, 32)
	r, err := NewBackendRegistration(backendIdentityFixture(), seed)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	document, err := r.Document()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(document)
	parsed, err := ParseBackendRegistration(document)
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.Close()
	host, err := r.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(host.Key)
	app, err := parsed.Signer("boot-1")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(app.Key)
	if bytes.Equal(host.Key, seed) || !bytes.Equal(host.Key, app.Key) {
		t.Fatal("key derivation/reader mismatch")
	}
	now := time.Now()
	challenge, err := host.NewChallenge(now)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := app.AnswerChallenge(challenge, "boot-1", now)
	if err != nil {
		t.Fatal(err)
	}
	host.BootID, err = host.VerifyAnswer(challenge, answer, now)
	if err != nil || host.BootID != "boot-1" {
		t.Fatal("managed boot proof", err)
	}
	verifier := protocol.NewVerifier(protocol.VerifyConfig{Issuer: app.Issuer, Audience: app.Audience, AppID: app.AppID, InstanceID: app.InstanceID, BootID: app.BootID, Keys: map[string][]byte{app.KeyID: app.Key}, ReplayCapacity: 16})
	body := []byte(`{"value":"fixture"}`)
	viewer := protocol.Viewer{Subject: strings.Repeat("b", 64), Role: "user", OUs: []string{strings.Repeat("c", 64)}}
	token, err := host.Sign("POST", "/v1/notes", body, viewer, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(token, "POST", "/v1/notes", body, now); err != nil {
		t.Fatal("shared verifier rejected managed binding", err)
	}
	if _, err := verifier.Verify(token, "POST", "/v1/notes", body, now); err == nil {
		t.Fatal("replayed managed request accepted")
	}
	for _, mode := range []string{"generation", "policy", "release", "epoch", "installation", "deployment", "app"} {
		i := backendIdentityFixture()
		switch mode {
		case "generation":
			i.Generation = "2"
		case "policy":
			i.PolicyRevision = "2"
		case "release":
			i.ReleaseSHA256 = strings.Repeat("d", 64)
		case "epoch":
			i.ControllerEpoch = "00000000-0000-4000-8000-000000000009"
		case "installation":
			i.InstallationID = "00000000-0000-4000-8000-000000000009"
		case "deployment":
			i.DeploymentID = "00000000-0000-4000-8000-000000000009"
		case "app":
			i.AppID = "other-app"
		}
		other, err := NewBackendRegistration(i, seed)
		if err != nil {
			t.Fatal(err)
		}
		otherSigner, err := other.Signer("boot-1")
		if err != nil {
			t.Fatal(err)
		}
		if otherSigner.InstanceID == host.InstanceID || bytes.Equal(otherSigner.Key, host.Key) {
			t.Fatal("managed tuple not separated", mode)
		}
		otherToken, err := otherSigner.Sign("POST", "/v1/notes", body, viewer, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(otherToken, "POST", "/v1/notes", body, now); err == nil {
			t.Fatal("old verifier accepted other managed tuple", mode)
		}
		clear(otherSigner.Key)
		other.Close()
	}
	for _, mode := range []string{"method", "path", "body", "boot"} {
		method, path, requestBody := "POST", "/v1/notes", body
		signer := host
		if mode == "boot" {
			signer.BootID = "boot-2"
		}
		token, err := signer.Sign("POST", "/v1/notes", body, viewer, now)
		if err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "method":
			method = "GET"
		case "path":
			path = "/v1/other"
		case "body":
			requestBody = []byte(`{}`)
		}
		if _, err := verifier.Verify(token, method, path, requestBody, now); err == nil {
			t.Fatal("binding change accepted", mode)
		}
	}
}

func TestBackendRegistrationRedactionAndClose(t *testing.T) {
	r, err := NewBackendRegistration(backendIdentityFixture(), bytes.Repeat([]byte{21}, 32))
	if err != nil {
		t.Fatal(err)
	}
	copy := r
	signer, err := r.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	for _, format := range []string{"%v", "%+v", "%#v"} {
		out := fmt.Sprintf(format, r)
		if !strings.Contains(out, "redacted") || strings.Contains(out, base64.StdEncoding.EncodeToString(signer.Key)) {
			t.Fatal("registration logging leaked key")
		}
	}
	if _, err := json.Marshal(r); err == nil {
		t.Fatal("accidental JSON key export")
	}
	if err := json.Unmarshal([]byte(`{}`), new(BackendRegistration)); err == nil {
		t.Fatal("JSON minted live registration")
	}
	r.Close()
	if _, err := copy.Document(); err == nil {
		t.Fatal("copy survived registration close")
	}
	if _, err := copy.Signer("boot"); err == nil {
		t.Fatal("closed registration signed")
	}
	if _, err := copy.Metadata(); err == nil {
		t.Fatal("closed registration remained live")
	}
}

func TestBackendRegistrationRejectsInvalidDocuments(t *testing.T) {
	r, err := NewBackendRegistration(backendIdentityFixture(), bytes.Repeat([]byte{18}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	raw, err := r.Document()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	for _, invalid := range [][]byte{nil, []byte(`{}`), append(bytes.Clone(raw), ' '), []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`), []byte(strings.Replace(string(raw), `"generation":"1"`, `"generation":"2"`, 1)), []byte(strings.Replace(string(raw), `"policy_revision":"1"`, `"policy_revision":"01"`, 1)), []byte(strings.Replace(string(raw), `"format":"portal.managed-backend-registration/1-dev.1"`, `"format":"other"`, 1)), bytes.Repeat([]byte{'a'}, 4097)} {
		if accepted, err := ParseBackendRegistration(invalid); err == nil {
			accepted.Close()
			t.Fatal("invalid bootstrap accepted")
		}
		clear(invalid)
	}
	for _, kind := range []string{"app", "epoch", "generation", "policy", "release"} {
		i := backendIdentityFixture()
		switch kind {
		case "app":
			i.AppID = "../escape"
		case "epoch":
			i.ControllerEpoch = "00000000-0000-0000-0000-000000000000"
		case "generation":
			i.Generation = "0"
		case "policy":
			i.PolicyRevision = "9223372036854775808"
		case "release":
			i.ReleaseSHA256 = "bad"
		}
		if accepted, err := NewBackendRegistration(i, bytes.Repeat([]byte{18}, 32)); err == nil {
			accepted.Close()
			t.Fatal("invalid tuple accepted", kind)
		}
	}
	if accepted, err := NewBackendRegistration(backendIdentityFixture(), nil); err == nil {
		accepted.Close()
		t.Fatal("missing seed accepted")
	}
}
