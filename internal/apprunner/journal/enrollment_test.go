//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func enrollmentFixture(n byte) ControllerEnrollment {
	// Public development fixture material only; never enroll test keys in a deployment.
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{n}, 32)).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(key)
	return ControllerEnrollment{DeploymentID: fixtureID(400), ControllerEpoch: fixtureID(401), ControlRevision: 3, PublicKey: key, Fingerprint: hex.EncodeToString(sum[:])}
}
func TestExplicitControllerEnrollmentReopens(t *testing.T) {
	ctx := context.Background()
	directory := privateDirectory(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ControllerDeployment(ctx); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("empty journal gained trust: %v", err)
	}
	expected := enrollmentFixture(17)
	first, created, err := store.EnrollController(ctx, expected)
	if err != nil || !created {
		t.Fatalf("explicit enrollment: %v", err)
	}
	if first.Revision != 1 || first.ControlRevision != 3 || first.CreatedMS <= 0 || first.UpdatedMS != first.CreatedMS {
		t.Fatal("invalid initial revision/time")
	}
	second, created, err := store.EnrollController(ctx, expected)
	if err != nil || created || !reflect.DeepEqual(first, second) {
		t.Fatalf("exact retry changed enrollment: %v", err)
	}
	second.PublicKey[0] ^= 1
	unchanged, err := store.ControllerDeployment(ctx)
	if err != nil || !reflect.DeepEqual(first, unchanged) {
		t.Fatal("caller mutation changed persisted controller")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.ControllerDeployment(ctx)
	if err != nil || !reflect.DeepEqual(first, after) {
		t.Fatalf("reopen replaced trust: %v", err)
	}
}
func TestControllerEnrollmentCannotReplaceIdentity(t *testing.T) {
	for _, kind := range []string{"key", "epoch", "deployment", "revision", "bad_pin", "algorithm"} {
		t.Run(kind, func(t *testing.T) {
			store, err := Open(privateDirectory(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			request := enrollmentFixture(18)
			before, _, err := store.EnrollController(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "key":
				request = enrollmentFixture(19)
			case "epoch":
				request.ControllerEpoch = fixtureID(402)
			case "deployment":
				request.DeploymentID = fixtureID(403)
			case "revision":
				request.ControlRevision++
			case "bad_pin":
				request.Fingerprint = strings.Repeat("0", 64)
			case "algorithm":
				if _, err := store.db.Exec(`UPDATE runner_deployment SET verification_algorithm='RSA'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := store.EnrollController(ctx, request); err == nil {
				t.Fatal("existing trust overwritten")
			}
			if kind != "algorithm" {
				after, err := store.ControllerDeployment(ctx)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("refusal changed stored identity")
				}
			}
		})
	}
}
func TestEnrollmentRefusesUnownedWorkAndInvalidInput(t *testing.T) {
	for _, kind := range []string{"orphan", "short_key", "uppercase_id", "zero_revision", "bad_pin", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			store, err := Open(privateDirectory(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			request := enrollmentFixture(20)
			ctx := context.Background()
			switch kind {
			case "orphan":
				if err := insertCommand(store.db); err != nil {
					t.Fatal(err)
				}
			case "short_key":
				request.PublicKey = request.PublicKey[:31]
			case "uppercase_id":
				request.ControllerEpoch = "00000000-0000-4000-8000-00000000ABCD"
			case "zero_revision":
				request.ControlRevision = 0
			case "bad_pin":
				request.Fingerprint = strings.Repeat("a", 64)
			case "canceled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			if _, _, err := store.EnrollController(ctx, request); err == nil {
				t.Fatal("invalid first enrollment accepted")
			}
			if scalar(t, store.db, `SELECT COUNT(*) FROM runner_deployment`) != 0 {
				t.Fatal("refusal added controller trust")
			}
		})
	}
}
func TestConcurrentEnrollmentHasOneCreator(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := enrollmentFixture(21)
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, created, err := store.EnrollController(context.Background(), request)
			results <- created
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	created := 0
	for value := range results {
		if value {
			created++
		}
	}
	if created != 1 {
		t.Fatal("concurrent enrollment created multiple identities")
	}
}

func TestControllerEnrollmentInsertRollback(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Runtime enrollment has no Portal session/audit row. Inject failure at its
	// only durable write; no partial deployment or application work may remain.
	if _, err := store.db.Exec(`CREATE TRIGGER fixture_enrollment_failure BEFORE INSERT ON runner_deployment BEGIN SELECT RAISE(ABORT,'fixture enrollment failure');END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnrollController(context.Background(), enrollmentFixture(22)); err == nil {
		t.Fatal("injected enrollment failure ignored")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_deployment`) != 0 {
		t.Fatal("failed enrollment left trust")
	}
	if _, err := store.db.Exec(`DROP TRIGGER fixture_enrollment_failure`); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.EnrollController(context.Background(), enrollmentFixture(22)); err != nil || !created {
		t.Fatalf("retry after rollback: %v", err)
	}
}
func TestConflictingConcurrentEnrollmentHasOneIdentity(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, seed := range []byte{23, 24} {
		wg.Go(func() {
			_, _, err := store.EnrollController(context.Background(), enrollmentFixture(seed))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrEnrollmentConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("conflicting enrollments gained multiple authorities")
	}
	if _, err := store.ControllerDeployment(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestControllerEnrollmentChildExit(t *testing.T) {
	if os.Getenv("PORTAL_RUNNER_ENROLLMENT_TEST_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	store, err := Open(os.Getenv("PORTAL_RUNNER_ENROLLMENT_TEST_DIRECTORY"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnrollController(context.Background(), enrollmentFixture(25)); err != nil {
		t.Fatal(err)
	}
	// Bypass Close/checkpoint/Go defers to exercise committed WAL recovery.
	os.Exit(0)
}
func TestControllerEnrollmentSurvivesUncleanProcessExit(t *testing.T) {
	directory := privateDirectory(t)
	child := exec.Command(os.Args[0], "-test.run=^TestControllerEnrollmentChildExit$")
	child.Env = append(os.Environ(), "PORTAL_RUNNER_ENROLLMENT_TEST_CHILD=1", "PORTAL_RUNNER_ENROLLMENT_TEST_DIRECTORY="+directory)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("enrollment child: %v %s", err, output)
	}
	if info, err := os.Stat(filepath.Join(directory, journalName+"-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("no committed WAL evidence: %v", err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current, err := store.ControllerDeployment(context.Background())
	if err != nil || !reflect.DeepEqual(current.ControllerEnrollment, enrollmentFixture(25)) {
		t.Fatalf("unclean exit replaced enrollment: %v", err)
	}
}
