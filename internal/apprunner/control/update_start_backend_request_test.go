package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func updateStartRequestHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func updateStartBackendRequestFixture(t *testing.T) (UpdateStartBackendRequestCommand, ed25519.PrivateKey, Authority, int64, []byte, string) {
	t.Helper()
	registration, key, authority, now, _, document :=
		updateStartRegistrationFixture(t)
	defer clear(document)
	raw, signature, err := SignUpdateStartRegistration(
		registration, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"value":1}`)
	material, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
	signer, err := material.Signer("boot-update-1")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	assertion, err := signer.Sign(
		"POST", "/v1/echo", body,
		protocol.Viewer{Subject: strings.Repeat("a", 64), Role: "user"},
		time.UnixMilli(now))
	if err != nil {
		t.Fatal(err)
	}
	command := UpdateStartBackendRequestCommand{
		Format:          UpdateStartBackendRequestFormat,
		RequestID:       "90000000-0000-4000-8000-000000000401",
		RegistrationRaw: raw, RegistrationSignature: signature,
		BootID: "boot-update-1", Method: "POST", Path: "/v1/echo",
		BodySHA256:      updateStartRequestHash(body),
		AssertionSHA256: updateStartRequestHash([]byte(assertion)),
	}
	return command, key, authority, now, body, assertion
}

func TestUpdateStartBackendRequestRoundTrip(t *testing.T) {
	command, key, authority, now, body, assertion :=
		updateStartBackendRequestFixture(t)
	raw, signature, err := SignUpdateStartBackendRequest(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyUpdateStartBackendRequest(
		raw, signature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Registration().Start().Command().TargetGeneration != "2" ||
		ValidateUpdateStartBackendRequestData(
			verified, body, assertion) != nil {
		t.Fatal("update-start request lost its registration binding")
	}
	wire := verified.Command()
	wire.RegistrationRaw[0] ^= 1
	wire.RegistrationSignature[0] ^= 1
	if _, err := VerifyUpdateStartBackendRequest(
		raw, signature, authority, now); err != nil {
		t.Fatal("getter aliased registration bytes", err)
	}
}

func TestUpdateStartBackendRequestRefusesChangedBindings(t *testing.T) {
	command, key, authority, now, _, _ :=
		updateStartBackendRequestFixture(t)
	raw, signature, err := SignUpdateStartBackendRequest(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*UpdateStartBackendRequestCommand){
		"request": func(c *UpdateStartBackendRequestCommand) {
			c.RequestID = "90000000-0000-4000-8000-000000000402"
		},
		"method": func(c *UpdateStartBackendRequestCommand) {
			c.Method = "DELETE"
		},
		"path": func(c *UpdateStartBackendRequestCommand) {
			c.Path = "/v1/other"
		},
		"body": func(c *UpdateStartBackendRequestCommand) {
			c.BodySHA256 = strings.Repeat("a", 64)
		},
		"assertion": func(c *UpdateStartBackendRequestCommand) {
			c.AssertionSHA256 = strings.Repeat("b", 64)
		},
		"registration": func(c *UpdateStartBackendRequestCommand) {
			c.RegistrationRaw = bytes.Clone(c.RegistrationRaw)
			c.RegistrationRaw[0] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			changed.RegistrationRaw = bytes.Clone(command.RegistrationRaw)
			changed.RegistrationSignature =
				bytes.Clone(command.RegistrationSignature)
			mutate(&changed)
			changedRaw, _ := json.Marshal(changed)
			if _, err := VerifyUpdateStartBackendRequest(
				changedRaw, signature, authority, now); err == nil {
				t.Fatal("changed update-start request accepted")
			}
		})
	}
	if _, err := VerifyUpdateStartBackendRequest(
		raw, signature, authority, now+40000); err == nil {
		t.Fatal("expired update-start request accepted")
	}
}
