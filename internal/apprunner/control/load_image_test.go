package control

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func loadImageFixture(t *testing.T) (LoadImageCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	checkpoint, key, authority, now := checkpointFixture(t)
	command := LoadImageCommand{CommandHeader: CommandHeader{
		Format: LoadImageFormat, DeploymentID: authority.DeploymentID,
		ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID: checkpoint.InstallationID,
		OperationID:    checkpoint.OperationID, TargetGeneration: "2",
		Stage: "stage", EffectNumber: "1",
		EffectID: "c0000000-0000-4000-8000-0000000000a1", Kind: "load-image",
		InstallationFence: "2",
	}, Payload: LoadImagePayload{
		ReleaseSHA256: strings.Repeat("9", 64), Platform: "linux/amd64",
		ManifestDigest: strings.Repeat("1", 64), ConfigDigest: strings.Repeat("2", 64),
		Layers: strings.Repeat("3", 64) + ":1024," + strings.Repeat("4", 64) + ":2048",
	}}
	digest, err := LoadImageLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func TestLoadImageRoundTripsAndRefusesNonCanonicalBytes(t *testing.T) {
	command, key, authority, now := loadImageFixture(t)
	raw, signature, err := SignLoadImage(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyLoadImage(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("round trip", err)
	}
	for name, variant := range map[string][]byte{
		"trailing":  append(append([]byte(nil), raw...), '\n'),
		"duplicate": []byte(strings.Replace(string(raw), `"stage":"stage"`, `"stage":"stage","stage":"stage"`, 1)),
		"escaped":   []byte(strings.Replace(string(raw), `"load-image"`, "\"load\\u002dimage\"", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			sig := ed25519.Sign(key, frame(loadImageSignaturePurpose, variant))
			if _, err := VerifyLoadImage(variant, sig, authority, now); err == nil {
				t.Fatal("non-canonical command accepted")
			}
		})
	}
}

// A signature made for another purpose must never verify here, and the
// load-image purposes must never verify elsewhere: each failure is the safe
// direction (the foreign command is refused).
func TestLoadImagePurposesAreDisjoint(t *testing.T) {
	command, key, authority, now := loadImageFixture(t)
	raw, execute, err := SignLoadImage(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	read, err := SignLoadImageRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(read) == string(execute) {
		t.Fatal("read signature equals execute signature")
	}
	if _, err := VerifyLoadImageRead(raw, read, authority, 55000); err != nil {
		t.Fatal("a read after the signed window was refused:", err)
	}
	for name, check := range map[string]func() error{
		"execute signature as read":    func() error { _, err := VerifyLoadImageRead(raw, execute, authority, now); return err },
		"read signature as execute":    func() error { _, err := VerifyLoadImage(raw, read, authority, now); return err },
		"read signature as settlement": func() error { _, err := VerifyLoadImageSettlement(raw, read, authority, now); return err },
		"migrate purpose as execute": func() error {
			_, err := VerifyLoadImage(raw, ed25519.Sign(key, frame(migrateDataSignaturePurpose, raw)), authority, now)
			return err
		},
		"image blob purpose as execute": func() error {
			_, err := VerifyLoadImage(raw, ed25519.Sign(key, frame(imageBlobSignaturePurpose, raw)), authority, now)
			return err
		},
		"prepare data purpose as execute": func() error {
			_, err := VerifyLoadImage(raw, ed25519.Sign(key, frame("portal.app/runner-command/v1", raw)), authority, now)
			return err
		},
	} {
		if check() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := VerifyLoadImage(raw, execute, authority, 54000); err == nil {
		t.Fatal("expired execute accepted")
	}
	if _, err := VerifyLoadImageSettlement(raw, execute, authority, 54000); err != nil {
		t.Fatal("settlement is not bound by the window:", err)
	}
}

func TestLoadImageRefusesMalformedHeaderAndPayload(t *testing.T) {
	cases := map[string]func(*LoadImageCommand){
		"wrong stage":             func(c *LoadImageCommand) { c.Stage = "migrate" },
		"wrong kind":              func(c *LoadImageCommand) { c.Kind = "migrate-data" },
		"zero slot":               func(c *LoadImageCommand) { c.EffectNumber = "0" },
		"slot above thirty two":   func(c *LoadImageCommand) { c.EffectNumber = "33" },
		"leading zero fence":      func(c *LoadImageCommand) { c.InstallationFence = "02" },
		"bad release":             func(c *LoadImageCommand) { c.Payload.ReleaseSHA256 = "abc" },
		"unknown platform":        func(c *LoadImageCommand) { c.Payload.Platform = "linux/riscv64" },
		"upper manifest":          func(c *LoadImageCommand) { c.Payload.ManifestDigest = strings.ToUpper(strings.Repeat("a", 64)) },
		"prefixed config":         func(c *LoadImageCommand) { c.Payload.ConfigDigest = "sha256:" + strings.Repeat("a", 64) },
		"empty layers":            func(c *LoadImageCommand) { c.Payload.Layers = "" },
		"layer without size":      func(c *LoadImageCommand) { c.Payload.Layers = strings.Repeat("3", 64) },
		"layer zero size":         func(c *LoadImageCommand) { c.Payload.Layers = strings.Repeat("3", 64) + ":0" },
		"layer size leading zero": func(c *LoadImageCommand) { c.Payload.Layers = strings.Repeat("3", 64) + ":01" },
		"layer oversize":          func(c *LoadImageCommand) { c.Payload.Layers = strings.Repeat("3", 64) + ":1073741825" },
		"trailing comma":          func(c *LoadImageCommand) { c.Payload.Layers += "," },
		"too many layers": func(c *LoadImageCommand) {
			c.Payload.Layers = strings.TrimSuffix(strings.Repeat(strings.Repeat("3", 64)+":1,", 33), ",")
		},
		"config equals manifest": func(c *LoadImageCommand) { c.Payload.ConfigDigest = c.Payload.ManifestDigest },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			command, _, _, _ := loadImageFixture(t)
			mutate(&command)
			if _, err := LoadImageLogicalDigest(command); err == nil {
				t.Fatal("malformed command has a digest")
			}
		})
	}
}

func TestLoadImageLayersCodecRoundTrips(t *testing.T) {
	layers, ok := ParseLoadImageLayers(strings.Repeat("3", 64) + ":1024," + strings.Repeat("4", 64) + ":2048")
	if !ok || len(layers) != 2 || layers[1].DiffID != strings.Repeat("4", 64) || layers[1].UncompressedBytes != 2048 {
		t.Fatalf("parse: %+v %v", layers, ok)
	}
	if FormatLoadImageLayers(layers) != strings.Repeat("3", 64)+":1024,"+strings.Repeat("4", 64)+":2048" {
		t.Fatal("format is not the inverse of parse")
	}
}

func TestLoadImageLogicalDigestBindsEveryPayloadField(t *testing.T) {
	base, _, _, _ := loadImageFixture(t)
	want, _ := LoadImageLogicalDigest(base)
	for name, mutate := range map[string]func(*LoadImageCommand){
		"release":  func(c *LoadImageCommand) { c.Payload.ReleaseSHA256 = strings.Repeat("8", 64) },
		"platform": func(c *LoadImageCommand) { c.Payload.Platform = "linux/arm64" },
		"manifest": func(c *LoadImageCommand) { c.Payload.ManifestDigest = strings.Repeat("7", 64) },
		"config":   func(c *LoadImageCommand) { c.Payload.ConfigDigest = strings.Repeat("7", 64) },
		"layers": func(c *LoadImageCommand) {
			c.Payload.Layers = strings.Repeat("3", 64) + ":1025," + strings.Repeat("4", 64) + ":2048"
		},
		"slot":      func(c *LoadImageCommand) { c.EffectNumber = "2" },
		"operation": func(c *LoadImageCommand) { c.OperationID = "c0000000-0000-4000-8000-0000000000ff" },
	} {
		changed := base
		mutate(&changed)
		got, err := LoadImageLogicalDigest(changed)
		if err != nil || got == want {
			t.Fatalf("%s is not bound by the logical digest: %v", name, err)
		}
	}
	// Transport timing and the installation fence are excluded so a renewed envelope observes the same effect.
	renewed := base
	renewed.IssuedMS, renewed.ExpiresMS, renewed.InstallationFence = "30000", "55000", "3"
	if got, _ := LoadImageLogicalDigest(renewed); got != want {
		t.Fatal("a renewed envelope changed the logical digest")
	}
	raw, _ := json.Marshal(base)
	if len(raw) > MaxCommandBytes {
		t.Fatal("fixture exceeds the command bound")
	}
}
