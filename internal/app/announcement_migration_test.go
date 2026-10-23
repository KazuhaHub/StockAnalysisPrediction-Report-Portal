package app

import (
	"bytes"
	"strings"
	"testing"
)

// Migration 0010 (ADR 0042): announcements.dismissible becomes strip_dismissible, and
// popup_dismissible is added beside it.
//
// The rename is the first step that changes a BASELINE column rather than adding one, so these tests
// pin the three places the old name is load-bearing: the startup shape check, the step's own
// verification, and a restore of a dump written before the step. Each must accept exactly one name at
// each point in the sequence and refuse the rest — a database carrying both names, or neither, is one
// nobody produced, and opening it would mean guessing which column is the real one.

// backBeforeStep0010 winds a migrated database back to the shape the release before 0010 left.
func backBeforeStep0010(t *testing.T, path string) {
	t.Helper()
	mutateFile(t, path,
		`DELETE FROM meta WHERE k='mig:0010'`,
		`ALTER TABLE announcements DROP COLUMN popup_dismissible`,
		`ALTER TABLE announcements RENAME COLUMN strip_dismissible TO dismissible`)
}

// The upgrade path, and the one production takes: values move with the column, nothing is
// back-filled, and an existing popup keeps behaving as it did (NULL reads as dismissible).
func TestStep0010RenamesTheBandFlagAndAddsThePopupFlag(t *testing.T) {
	path := fileStore(t, "pre-0010.db")
	backBeforeStep0010(t, path)
	mutateFile(t, path,
		`INSERT INTO announcements(id,title,dismissible,display,popup) VALUES(1,'条带可关',1,'both',1)`,
		`INSERT INTO announcements(id,title,dismissible,display,popup) VALUES(2,'条带不可关',0,'strip',0)`)

	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("a pre-0010 database must be accepted and migrated: %v", err)
	}
	if st.columnExists("announcements", "dismissible") {
		t.Error("the old column name survived the step")
	}
	for _, c := range []string{"strip_dismissible", "popup_dismissible"} {
		if !st.columnExists("announcements", c) {
			t.Errorf("announcements.%s is missing after the step", c)
		}
	}
	if !recorded(st, "0010") {
		t.Error("step 0010 ran but was not recorded")
	}
	if a := st.GetAnnouncement(1); a == nil || !a.StripDismissible || !a.PopupDismissible {
		t.Errorf("row 1 after the step = %+v, want strip and popup dismissible", a)
	}
	if a := st.GetAnnouncement(2); a == nil || a.StripDismissible || !a.PopupDismissible {
		t.Errorf("row 2 after the step = %+v, want strip locked, popup dismissible", a)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM announcements WHERE popup_dismissible IS NULL`); n != 2 {
		t.Errorf("%d rows have a NULL popup flag, want 2: the step must not back-fill", n)
	}
	st.Close()

	// The next start checks the baseline column under its new name, and writes nothing.
	again, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("reopening a migrated database: %v", err)
	}
	defer again.Close()
	if a := again.GetAnnouncement(1); a == nil || !a.StripDismissible {
		t.Errorf("row 1 after a restart = %+v", a)
	}
}

// Once the step is recorded the old name is wrong, not merely redundant. Both directions fail closed.
func TestARenamedBaseColumnIsCheckedUnderItsNewName(t *testing.T) {
	readded := fileStore(t, "readded.db")
	mutateFile(t, readded, `ALTER TABLE announcements ADD COLUMN dismissible INTEGER`)
	if st, err := OpenStore("sqlite", readded); err == nil {
		st.Close()
		t.Error("a migrated database that grew the old column back was accepted")
	}

	dropped := fileStore(t, "dropped.db")
	mutateFile(t, dropped, `ALTER TABLE announcements DROP COLUMN strip_dismissible`)
	if st, err := OpenStore("sqlite", dropped); err == nil {
		st.Close()
		t.Error("a migrated database missing the renamed column was accepted")
	} else if !strings.Contains(err.Error(), "strip_dismissible") {
		t.Errorf("refusal does not name the missing column: %v", err)
	}
}

// Before the step, a database carrying BOTH names is not one any release wrote, and neither name can
// be trusted to hold the band flag.
func TestAPreRenameDatabaseCarryingBothNamesIsRefused(t *testing.T) {
	path := fileStore(t, "both.db")
	backBeforeStep0010(t, path)
	mutateFile(t, path, `ALTER TABLE announcements ADD COLUMN strip_dismissible INTEGER`)
	if st, err := OpenStore("sqlite", path); err == nil {
		st.Close()
		t.Error("a database with both dismissible and strip_dismissible was accepted")
	}
}

// The runbook's pre-upgrade backup was written with the old name. It must load, with the value
// landing under the new one — the rename changes no data, so an older dump loses nothing.
func TestADumpFromBeforeTheRenameLoadsUnderTheNewName(t *testing.T) {
	dump := preMigrationDump(t,
		`INSERT INTO announcements(id,title,content,dismissible) VALUES(5,'旧公告','正文',1)`)
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("a dump from before 0010 must load: %v", err)
	}
	a := dst.GetAnnouncement(5)
	if a == nil || !a.StripDismissible || !a.PopupDismissible {
		t.Fatalf("restored row = %+v, want strip and popup dismissible", a)
	}
	if !recorded(dst, "0010") {
		t.Error("the restore did not record step 0010")
	}
}

// A dump that claims the step has to carry its result. Arriving with the old name instead is a
// damaged or hand-edited backup, and loading it as an old one would be a guess.
func TestADumpClaimingTheRenameMustCarryTheNewName(t *testing.T) {
	src := newTestStore(t)
	if _, err := src.exec(`INSERT INTO announcements(id,title,strip_dismissible,popup_dismissible) VALUES(1,'x',1,0)`); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, _, err := src.dumpTo(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"strip_dismissible"`) {
		t.Fatal("a current dump does not carry strip_dismissible")
	}
	forged := strings.Replace(buf.String(), `"strip_dismissible"`, `"dismissible"`, 1)

	dst := newTestStore(t)
	if _, err := dst.restoreFrom(strings.NewReader(forged), true); err == nil {
		t.Error("a dump claiming 0010 but carrying the old column name was loaded")
	}
}

// Both flags survive a backup and restore within the new line.
func TestBackupRoundTripKeepsBothDismissalFlags(t *testing.T) {
	src := newTestStore(t)
	if _, err := src.exec(`INSERT INTO announcements(id,title,strip_dismissible,popup_dismissible) VALUES(3,'y',1,0)`); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, _, err := src.dumpTo(&buf); err != nil {
		t.Fatal(err)
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(&buf, true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if a := dst.GetAnnouncement(3); a == nil || !a.StripDismissible || a.PopupDismissible {
		t.Errorf("restored row = %+v, want strip dismissible, popup locked", a)
	}
}

// The two guards above back each other up — the startup shape check and the step's own verification
// both refuse a wrong shape — so an end-to-end test cannot tell when one of them is broken. These
// drive each layer on its own.

func TestTheShapeCheckEnforcesTheRenameOnItsOwn(t *testing.T) {
	st := newTestStore(t)
	if err := st.checkBaseShape(true); err != nil {
		t.Fatalf("a migrated database fails its own shape check: %v", err)
	}
	if _, err := st.exec(`ALTER TABLE announcements ADD COLUMN dismissible INTEGER`); err != nil {
		t.Fatal(err)
	}
	if err := st.checkBaseShape(true); err == nil {
		t.Error("the shape check accepted the old name beside a recorded rename")
	}
	if _, err := st.exec(`DELETE FROM meta WHERE k='mig:0010'`); err != nil {
		t.Fatal(err)
	}
	if err := st.checkBaseShape(true); err == nil {
		t.Error("the shape check accepted both names before the step")
	}
}

func TestTheStepVerificationEnforcesTheRenameOnItsOwn(t *testing.T) {
	st := newTestStore(t)
	steps := []migration{migration0010()}
	if err := st.verifyMigrationProducts(st.poolExec(), steps); err != nil {
		t.Fatalf("a migrated database fails the step's verification: %v", err)
	}
	if _, err := st.exec(`ALTER TABLE announcements ADD COLUMN dismissible INTEGER`); err != nil {
		t.Fatal(err)
	}
	if err := st.verifyMigrationProducts(st.poolExec(), steps); err == nil {
		t.Error("the step's verification accepted the old name left beside the new one")
	}
}

// A dump whose header predates the rename must carry the OLD name. Carrying the new one — alone, or
// beside the old one — is a header and content that disagree, and on SQLite a duplicated column in
// the insert would silently keep whichever came first.
func TestAPreRenameDumpCarryingTheNewNameIsRefused(t *testing.T) {
	both := preMigrationDump(t,
		`ALTER TABLE announcements ADD COLUMN strip_dismissible INTEGER`,
		`INSERT INTO announcements(id,title,dismissible,strip_dismissible) VALUES(7,'两个名字',1,0)`)
	if _, err := newTestStore(t).restoreFrom(bytes.NewReader(both), true); err == nil {
		t.Error("a pre-0010 dump carrying both names was loaded")
	}

	renamedOnly := preMigrationDump(t,
		`ALTER TABLE announcements RENAME COLUMN dismissible TO strip_dismissible`)
	if _, err := newTestStore(t).restoreFrom(bytes.NewReader(renamedOnly), true); err == nil {
		t.Error("a dump whose header predates 0010 but whose content has the new name was loaded")
	}
}
