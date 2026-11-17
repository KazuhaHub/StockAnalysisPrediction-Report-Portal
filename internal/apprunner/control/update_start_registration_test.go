package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func updateStartRegistrationFixture(t *testing.T) (UpdateStartRegistrationCommand, ed25519.PrivateKey, Authority, int64, []byte, []byte) {
	t.Helper()
	command, key, authority, now := updateStartFixture(t)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	command.Payload.TargetReleaseSHA256 = parsed.SHA256
	command.PayloadSHA256, err = UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	startRaw, startSignature, err := SignUpdateStart(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := appplatform.NewBackendRegistration(
		appplatform.BackendIdentity{
			AppID: parsed.Manifest.ID, DeploymentID: command.DeploymentID,
			ControllerEpoch: command.ControllerEpoch,
			InstallationID:  command.InstallationID,
			Generation:      command.TargetGeneration,
			PolicyRevision:  command.Payload.TargetPolicyRevision,
			ReleaseSHA256:   parsed.SHA256,
		}, bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	document, err := registration.Document()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	value := UpdateStartRegistrationCommand{
		Format:   UpdateStartRegistrationFormat,
		EffectID: "b1000000-0000-4000-8000-000000000000",
		StartRaw: startRaw, StartSignature: startSignature,
		RunningReceiptSHA256: strings.Repeat("d", 64),
		DocumentSHA256:       hex.EncodeToString(digest[:]),
	}
	value.PayloadSHA256, err = UpdateStartRegistrationLogicalDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	return value, key, authority, now, descriptor, document
}

func TestUpdateStartRegistrationRoundTripAndDocument(t *testing.T) {
	command, key, authority, now, descriptor, document :=
		updateStartRegistrationFixture(t)
	defer clear(document)
	raw, signature, err := SignUpdateStartRegistration(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyUpdateStartRegistration(
		raw, signature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Start().Command().TargetGeneration != "2" ||
		verified.Start().Command().Payload.TargetPolicyRevision != "2" {
		t.Fatal("registration lost start reference")
	}
	registration, err := ValidateUpdateStartRegistrationDocument(
		verified, descriptor, document)
	if err != nil {
		t.Fatal(err)
	}
	registration.Close()
	wire := verified.Command()
	wire.StartRaw[0] ^= 1
	wire.StartSignature[0] ^= 1
	if _, err := VerifyUpdateStartRegistration(
		raw, signature, authority, now); err != nil {
		t.Fatal("getter aliased start reference", err)
	}
	if bytes.Contains(raw, []byte(`"signing_key"`)) {
		t.Fatal("private registration bytes entered signed command")
	}
}

func TestUpdateStartRegistrationRefusesChangedBindings(t *testing.T) {
	command, key, authority, now, _, _ := updateStartRegistrationFixture(t)
	raw, signature, err := SignUpdateStartRegistration(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*UpdateStartRegistrationCommand){
		"effect":      func(c *UpdateStartRegistrationCommand) { c.EffectID = "b2000000-0000-4000-8000-000000000000" },
		"running":     func(c *UpdateStartRegistrationCommand) { c.RunningReceiptSHA256 = strings.Repeat("e", 64) },
		"document":    func(c *UpdateStartRegistrationCommand) { c.DocumentSHA256 = strings.Repeat("f", 64) },
		"start bytes": func(c *UpdateStartRegistrationCommand) { c.StartRaw[0] ^= 1 },
		"start sig":   func(c *UpdateStartRegistrationCommand) { c.StartSignature[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			changed.StartRaw = bytes.Clone(command.StartRaw)
			changed.StartSignature = bytes.Clone(command.StartSignature)
			mutate(&changed)
			changedRaw, _ := json.Marshal(changed)
			if _, err := VerifyUpdateStartRegistration(
				changedRaw, signature, authority, now); err == nil {
				t.Fatal("changed registration binding accepted")
			}
		})
	}
	if _, err := VerifyUpdateStartRegistration(
		raw, signature, authority, now+40000); err == nil {
		t.Fatal("expired start reference accepted")
	}
}
