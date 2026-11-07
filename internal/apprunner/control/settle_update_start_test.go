package control

import (
	"crypto/ed25519"
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
		Format: SettleUpdateStartFormat, EffectID: "c1000000-0000-4000-8000-0000000000c1", OperationID: "c2000000-0000-4000-8000-0000000000c2",
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
