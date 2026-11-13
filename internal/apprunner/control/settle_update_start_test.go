package control

import (
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"testing"
)

func settleUpdateStartFixture(t *testing.T) (SettleUpdateStartCommand, ed25519.PrivateKey, Authority, int64, []byte, []byte) {
	t.Helper()
	start, key, authority, now := updateStartFixture(t)
	startRaw, startSignature, err := SignUpdateStart(start, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	startRead, err := SignUpdateStartRead(startRaw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	command := SettleUpdateStartCommand{
		Format: SettleUpdateStartFormat, Mode: SettleModeSettle, EffectID: "c1000000-0000-4000-8000-0000000000c1", OperationID: "c2000000-0000-4000-8000-0000000000c2",
		InstallationFence: start.InstallationFence, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
		StartRaw: startRaw, StartReadSignature: startRead,
	}
	if command.PayloadSHA256, err = SettleUpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	return command, key, authority, now, startRaw, startSignature
}

func TestSettleUpdateStartRoundTripAndPurposes(t *testing.T) {
	command, key, authority, now, _, _ := settleUpdateStartFixture(t)
	raw, signature, err := SignSettleUpdateStart(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifySettleUpdateStart(raw, signature, authority, now)
	if err != nil || verified.Start().Command().EffectID == "" || verified.Command().OperationID == verified.Start().Command().OperationID {
		t.Fatalf("round trip: %v", err)
	}
	read, err := SignSettleUpdateStartRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	// The execute and read signatures are not interchangeable.
	if _, err := VerifySettleUpdateStart(raw, read, authority, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := VerifySettleUpdateStartRead(raw, signature, authority, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	// Only the execute is bound by its window; a read-back after it is fine, a settlement already admitted too.
	after := now + 26000
	if _, err := VerifySettleUpdateStart(raw, signature, authority, after); err == nil {
		t.Fatal("an expired settle command was admitted")
	}
	if _, err := VerifySettleUpdateStartSettlement(raw, signature, authority, now); err != nil {
		t.Fatalf("an admitted settlement was refused: %v", err)
	}
}

// The start reference inside a settlement is signed for a read. A start signed to be executed is not accepted in its
// place, so no execute-capable start command is ever made for a settlement.
func TestSettleUpdateStartRefusesAnExecuteSignedStart(t *testing.T) {
	command, key, authority, now, _, startSignature := settleUpdateStartFixture(t)
	command.StartReadSignature = startSignature
	var err error
	if command.PayloadSHA256, err = SettleUpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SignSettleUpdateStart(command, key, authority, now); err == nil {
		t.Fatal("a settlement over an execute-signed start was signed")
	}
}

func TestSettleUpdateStartBinding(t *testing.T) {
	command, key, authority, now, _, _ := settleUpdateStartFixture(t)
	base := command.PayloadSHA256
	for name, mutate := range map[string]func(*SettleUpdateStartCommand){
		"operation": func(c *SettleUpdateStartCommand) { c.OperationID = "c3000000-0000-4000-8000-0000000000c3" },
		"fence":     func(c *SettleUpdateStartCommand) { c.InstallationFence = "9" },
		"effect":    func(c *SettleUpdateStartCommand) { c.EffectID = "c4000000-0000-4000-8000-0000000000c4" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			digest, err := SettleUpdateStartLogicalDigest(changed)
			if name != "effect" && (err != nil || digest == base) {
				t.Fatal("the field is not bound by the digest")
			}
		})
	}
	same := command
	same.OperationID = "ignored"
	if _, err := SettleUpdateStartLogicalDigest(same); err == nil {
		t.Fatal("a malformed operation was accepted")
	}
	raw, signature, err := SignSettleUpdateStart(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)/2] ^= 1
	if _, err := VerifySettleUpdateStart(tampered, signature, authority, now); err == nil {
		t.Fatal("a tampered command verified")
	}
}

// settleFixtureForKind builds a signed settlement over a start of the given kind.
func settleFixtureForKind(t *testing.T, kind string) (SettleUpdateStartCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	start, key, authority, now := updateStartFixture(t)
	start.Kind = kind
	switch kind {
	case "restore-start":
		start.OperationID = "d0000000-0000-4000-8000-000000000000"
		start.Payload.TargetReleaseSHA256 = start.Payload.SourceReleaseSHA256
	case "enable-start":
		start.Payload.TargetReleaseSHA256 = start.Payload.SourceReleaseSHA256
	}
	var err error
	if start.PayloadSHA256, err = UpdateStartLogicalDigest(start); err != nil {
		t.Fatal(err)
	}
	startRaw, _, err := SignUpdateStart(start, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	startRead, err := SignUpdateStartRead(startRaw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	command := SettleUpdateStartCommand{
		Format: SettleUpdateStartFormat, Mode: SettleModeSettle, EffectID: "c1000000-0000-4000-8000-0000000000c1", OperationID: "c2000000-0000-4000-8000-0000000000c2",
		InstallationFence: start.InstallationFence, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
		StartRaw: startRaw, StartReadSignature: startRead,
	}
	if command.PayloadSHA256, err = SettleUpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	return command, key, authority, now
}

// An orphaned enable-start or restore-start is settled exactly as an update-start is.
func TestSettleUpdateStartAcceptsEveryStartKind(t *testing.T) {
	for _, kind := range []string{"update-start", "enable-start", "restore-start"} {
		t.Run(kind, func(t *testing.T) {
			command, key, authority, now := settleFixtureForKind(t, kind)
			raw, signature, err := SignSettleUpdateStart(command, key, authority, now)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := VerifySettleUpdateStart(raw, signature, authority, now)
			if err != nil || verified.Start().Command().Kind != kind {
				t.Fatalf("verify: %v", err)
			}
			read, err := SignSettleUpdateStartRead(raw, key, authority, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifySettleUpdateStartRead(raw, read, authority, now); err != nil {
				t.Fatalf("read: %v", err)
			}
			if _, err := VerifySettleUpdateStartSettlement(raw, signature, authority, now); err != nil {
				t.Fatalf("settlement: %v", err)
			}
		})
	}
}

// A start of any other kind is refused, whatever else about it is well formed.
func TestSettleUpdateStartRefusesOtherKinds(t *testing.T) {
	command, _, _, _ := settleFixtureForKind(t, "update-start")
	start, err := parseUpdateStart(command.StartRaw)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"stop", "stage", "load-image", "remove-image", "start-candidate", ""} {
		changed := start
		changed.Kind = kind
		raw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		c := command
		c.StartRaw = raw
		if _, err := SettleUpdateStartLogicalDigest(c); err == nil {
			t.Fatalf("a settlement over a %q start was accepted", kind)
		}
	}
}

// The start's own digest names its kind, so a settlement signed for one kind never verifies for another.
func TestSettleUpdateStartIsBoundToTheStartKind(t *testing.T) {
	update, key, authority, now := settleFixtureForKind(t, "update-start")
	enable, _, _, _ := settleFixtureForKind(t, "enable-start")
	restore, _, _, _ := settleFixtureForKind(t, "restore-start")
	if update.PayloadSHA256 == enable.PayloadSHA256 || enable.PayloadSHA256 == restore.PayloadSHA256 || update.PayloadSHA256 == restore.PayloadSHA256 {
		t.Fatal("settlement digests of different start kinds coincide")
	}
	raw, signature, err := SignSettleUpdateStart(update, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]SettleUpdateStartCommand{"enable": enable, "restore": restore} {
		swapped := update
		swapped.StartRaw, swapped.StartReadSignature = other.StartRaw, other.StartReadSignature
		// The signed payload digest of the update settlement does not match the other start.
		rawSwapped, err := json.Marshal(swapped)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifySettleUpdateStart(rawSwapped, signature, authority, now); err == nil {
			t.Fatalf("a settlement signed for an update start verified over a %s start", name)
		}
	}
	_ = raw
}
