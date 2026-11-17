package control

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func removeImageFixture(t *testing.T) (RemoveImageCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	load, key, authority, now := loadImageFixture(t)
	command := RemoveImageCommand{CommandHeader: load.CommandHeader, Payload: RemoveImagePayload{ReleaseSHA256: load.Payload.ReleaseSHA256, Platform: load.Payload.Platform, ManifestDigest: load.Payload.ManifestDigest, ConfigDigest: load.Payload.ConfigDigest, Layers: load.Payload.Layers, KnownLoads: "c0000000-0000-4000-8000-0000000000a1"}}
	command.Format, command.Kind, command.EffectNumber = RemoveImageFormat, RemoveImageKind, "2"
	command.EffectID = "c0000000-0000-4000-8000-0000000000b1"
	digest, err := RemoveImageLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func TestRemoveImageRoundTripsAndRefusesNonCanonicalBytes(t *testing.T) {
	command, key, authority, now := removeImageFixture(t)
	raw, signature, err := SignRemoveImage(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyRemoveImage(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("round trip", err)
	}
	for name, variant := range map[string][]byte{
		"trailing":  append(append([]byte(nil), raw...), '\n'),
		"duplicate": []byte(strings.Replace(string(raw), `"stage":"stage"`, `"stage":"stage","stage":"stage"`, 1)),
		"escaped":   []byte(strings.Replace(string(raw), `"remove-image"`, "\"remove\\u002dimage\"", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			sig := ed25519.Sign(key, frame(removeImageSignaturePurpose, variant))
			if _, err := VerifyRemoveImage(variant, sig, authority, now); err == nil {
				t.Fatal("non-canonical command accepted")
			}
		})
	}
}

// A load-image command must never verify as a remove-image command or the reverse, whichever purpose signed it:
// a signed load can never be replayed as a removal.
func TestRemoveImagePurposesAreDisjointFromLoadImage(t *testing.T) {
	command, key, authority, now := removeImageFixture(t)
	raw, execute, err := SignRemoveImage(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	read, err := SignRemoveImageRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(read) == string(execute) {
		t.Fatal("read signature equals execute signature")
	}
	if _, err := VerifyRemoveImageRead(raw, read, authority, 55000); err != nil {
		t.Fatal("a read after the signed window was refused:", err)
	}
	for name, check := range map[string]func() error{
		"execute signature as read":    func() error { _, err := VerifyRemoveImageRead(raw, execute, authority, now); return err },
		"read signature as execute":    func() error { _, err := VerifyRemoveImage(raw, read, authority, now); return err },
		"read signature as settlement": func() error { _, err := VerifyRemoveImageSettlement(raw, read, authority, now); return err },
		"load purpose as execute": func() error {
			_, err := VerifyRemoveImage(raw, ed25519.Sign(key, frame(loadImageSignaturePurpose, raw)), authority, now)
			return err
		},
		"load read purpose as read": func() error {
			_, err := VerifyRemoveImageRead(raw, ed25519.Sign(key, frame(loadImageReadPurpose, raw)), authority, now)
			return err
		},
		"as a load command": func() error {
			_, err := VerifyLoadImage(raw, ed25519.Sign(key, frame(loadImageSignaturePurpose, raw)), authority, now)
			return err
		},
	} {
		if check() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := VerifyRemoveImage(raw, execute, authority, 54000); err == nil {
		t.Fatal("expired execute accepted")
	}
	if _, err := VerifyRemoveImageSettlement(raw, execute, authority, 54000); err != nil {
		t.Fatal("settlement is not bound by the window:", err)
	}
}

func TestRemoveImageRefusesMalformedHeaderAndPayload(t *testing.T) {
	cases := map[string]func(*RemoveImageCommand){
		"load format":           func(c *RemoveImageCommand) { c.Format = LoadImageFormat },
		"load kind":             func(c *RemoveImageCommand) { c.Kind = LoadImageKind },
		"wrong stage":           func(c *RemoveImageCommand) { c.Stage = "migrate" },
		"slot one is the load":  func(c *RemoveImageCommand) { c.EffectNumber = "1" },
		"zero slot":             func(c *RemoveImageCommand) { c.EffectNumber = "0" },
		"slot above thirty two": func(c *RemoveImageCommand) { c.EffectNumber = "33" },
		"bad release":           func(c *RemoveImageCommand) { c.Payload.ReleaseSHA256 = "abc" },
		"unknown platform":      func(c *RemoveImageCommand) { c.Payload.Platform = "linux/riscv64" },
		"prefixed config":       func(c *RemoveImageCommand) { c.Payload.ConfigDigest = "sha256:" + strings.Repeat("a", 64) },
		"manifest is config":    func(c *RemoveImageCommand) { c.Payload.ManifestDigest = c.Payload.ConfigDigest },
		"empty layers":          func(c *RemoveImageCommand) { c.Payload.Layers = "" },
		"no known loads":        func(c *RemoveImageCommand) { c.Payload.KnownLoads = "" },
		"unsorted known loads": func(c *RemoveImageCommand) {
			c.Payload.KnownLoads = "c0000000-0000-4000-8000-0000000000a2,c0000000-0000-4000-8000-0000000000a1"
		},
		"duplicate known loads": func(c *RemoveImageCommand) {
			c.Payload.KnownLoads = "c0000000-0000-4000-8000-0000000000a1,c0000000-0000-4000-8000-0000000000a1"
		},
		"bad known load": func(c *RemoveImageCommand) { c.Payload.KnownLoads = "not-a-uuid" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			command, _, _, _ := removeImageFixture(t)
			mutate(&command)
			if _, err := RemoveImageLogicalDigest(command); err == nil {
				t.Fatal("malformed remove-image command produced a digest")
			}
		})
	}
	for _, slot := range []string{"2", "17", "32"} {
		command, _, _, _ := removeImageFixture(t)
		command.EffectNumber = slot
		if _, err := RemoveImageLogicalDigest(command); err != nil {
			t.Fatalf("slot %s refused: %v", slot, err)
		}
	}
}

// The logical digest binds every payload field and the effect identity, so a signed removal of one image cannot be
// carried over to another.
func TestRemoveImageDigestBindsThePayload(t *testing.T) {
	command, _, _, _ := removeImageFixture(t)
	base, _ := RemoveImageLogicalDigest(command)
	other := command
	other.Payload.ConfigDigest = strings.Repeat("7", 64)
	changed, _ := RemoveImageLogicalDigest(other)
	if base == changed {
		t.Fatal("a different image has the same logical digest")
	}
	load, _, _, _ := loadImageFixture(t)
	loadDigest, _ := LoadImageLogicalDigest(load)
	if loadDigest == base {
		t.Fatal("a load and a removal share a logical digest")
	}
}

func TestKnownLoadsAreCanonicalAndBounded(t *testing.T) {
	ids := []string{"c0000000-0000-4000-8000-0000000000a2", "c0000000-0000-4000-8000-0000000000a1"}
	encoded := FormatKnownLoads(ids)
	parsed, ok := ParseKnownLoads(encoded)
	if !ok || len(parsed) != 2 || parsed[0] > parsed[1] {
		t.Fatalf("round trip: %v %v", parsed, ok)
	}
	var many []string
	for i := 0; i <= MaxKnownLoads; i++ {
		many = append(many, "c0000000-0000-4000-8000-"+strings.Repeat("0", 8)+string(rune('a'+i/26))+string(rune('a'+i%26))+"00")
	}
	if _, ok := ParseKnownLoads(FormatKnownLoads(many)); ok {
		t.Fatal("more than MaxKnownLoads accepted")
	}
}
