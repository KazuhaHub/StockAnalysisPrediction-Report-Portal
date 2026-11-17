package control

import "testing"

// A read-receipt command is signed under its own purpose. A captured read can therefore never be
// submitted as an execute, and an execute signature is not a read.
func TestImportCheckpointReadHasItsOwnSignaturePurpose(t *testing.T) {
	command, key, authority, now := importCheckpointFixture(t)
	raw, executeSignature, err := SignImportCheckpoint(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	readSignature, err := SignImportCheckpointRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(readSignature) == string(executeSignature) {
		t.Fatal("the read signature equals the execute signature")
	}
	if _, err := VerifyImportCheckpointRead(raw, readSignature, authority, now); err != nil {
		t.Fatal("a read signature was not accepted as a read:", err)
	}
	// Settlement of the original read window: a read outlives the execute window like a settlement.
	if _, err := VerifyImportCheckpointRead(raw, readSignature, authority, 55000); err != nil {
		t.Fatal("a read after the signed window was refused:", err)
	}
	if _, err := VerifyImportCheckpointRead(raw, executeSignature, authority, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	for name, verify := range map[string]func() error{
		"execute": func() error { _, err := VerifyImportCheckpoint(raw, readSignature, authority, now); return err },
		"settle": func() error {
			_, err := VerifyImportCheckpointSettlement(raw, readSignature, authority, now)
			return err
		},
	} {
		if verify() == nil {
			t.Fatalf("a read signature was accepted as an import %s", name)
		}
	}
}

func TestUpdateStartReadHasItsOwnSignaturePurpose(t *testing.T) {
	command, key, authority, now := updateStartFixture(t)
	raw, executeSignature, err := SignUpdateStart(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	readSignature, err := SignUpdateStartRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyUpdateStartRead(raw, readSignature, authority, now); err != nil {
		t.Fatal("a read signature was not accepted as a read:", err)
	}
	if _, err := VerifyUpdateStartRead(raw, readSignature, authority, 55000); err == nil {
		t.Fatal("an expired read was accepted")
	}
	if _, err := VerifyUpdateStartRead(raw, executeSignature, authority, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	if _, err := VerifyUpdateStart(raw, readSignature, authority, now); err == nil {
		t.Fatal("a read signature was accepted as an update-start execute")
	}
}
