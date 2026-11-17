//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func runnerCommandFixture(t *testing.T) control.Command {
	t.Helper()
	authority := enrollmentFixture(30)
	now := time.Now().UTC().UnixMilli()
	return control.Command{Format: control.Format, DeploymentID: authority.DeploymentID, ControllerEpoch: authority.ControllerEpoch, ControlRevision: strconv.FormatInt(authority.ControlRevision, 10), InstallationID: fixtureID(410), OperationID: fixtureID(411), TargetGeneration: "1", Stage: "prepare_data", EffectNumber: "1", EffectID: fixtureID(412), Kind: "prepare-data", InstallationFence: "1", IssuedMS: strconv.FormatInt(now-1000, 10), ExpiresMS: strconv.FormatInt(now+29000, 10), Payload: control.PrepareDataPayload{DataNamespaceID: fixtureID(413), ReleaseSHA256: strings.Repeat("a", 64), DataFormat: "1", QuotaBytes: "268435456", QuotaInodes: "65536", WriterFence: "1"}}
}
func signRunnerCommand(t *testing.T, c control.Command) ([]byte, []byte) {
	t.Helper()
	digest, err := control.LogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.PayloadSHA256 = digest
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	prefix := []byte("portal.app/runner-command/v1\x00")
	frame := append([]byte{}, prefix...)
	length := make([]byte, 8)
	binary.BigEndian.PutUint64(length, uint64(len(raw)))
	frame = append(frame, length...)
	frame = append(frame, raw...)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	return raw, ed25519.Sign(key, frame)
}
func enrolledCommandStore(t *testing.T) (*Store, string) {
	t.Helper()
	directory := privateDirectory(t)
	s, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, _, err := s.EnrollController(context.Background(), enrollmentFixture(30)); err != nil {
		t.Fatal(err)
	}
	return s, directory
}
func TestSignedCommandAdmissionAndReplay(t *testing.T) {
	s, directory := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	raw, sig := signRunnerCommand(t, c)
	first, err := s.AdmitControlCommand(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.State != "prepared" || first.AdmittedFence != 1 || first.CurrentFence != 1 {
		t.Fatal("admission manufactured a physical outcome")
	}
	replay, err := s.AdmitControlCommand(context.Background(), raw, sig)
	if err != nil || !replay.Replayed || replay.EffectID != first.EffectID {
		t.Fatalf("duplicate identity: %v", err)
	}
	c.IssuedMS = strconv.FormatInt(time.Now().UnixMilli(), 10)
	c.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+20000, 10)
	raw, sig = signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	c.InstallationFence = "2"
	raw, sig = signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatalf("rewrote admitted fence on takeover: %v", err)
	}
	if scalar(t, s.db, `SELECT admitted_fence FROM runner_commands`) != 1 {
		t.Fatal("original execution fence changed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c.InstallationFence = "1"
	raw, sig = signRunnerCommand(t, c)
	if result, err := reopened.AdmitControlCommand(context.Background(), raw, sig); err != nil || !result.Replayed {
		t.Fatalf("reopen forgot admitted intent: %v", err)
	}
	if scalar(t, reopened.db, `SELECT COUNT(*) FROM runner_commands`) != 1 || scalar(t, reopened.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
		t.Fatal("replay duplicated work or fabricated resource")
	}
}
func TestCommandIdentityAndPendingWorkRefusal(t *testing.T) {
	for _, name := range []string{"payload", "slot", "new_pending", "signature", "stale"} {
		t.Run(name, func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			c := runnerCommandFixture(t)
			c.InstallationFence = "2"
			raw, sig := signRunnerCommand(t, c)
			if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "payload":
				c.Payload.QuotaBytes = "536870912"
			case "slot":
				c.EffectID = fixtureID(414)
			case "new_pending":
				c.EffectID = fixtureID(414)
				c.OperationID = fixtureID(415)
				c.InstallationFence = "3"
			case "stale":
				c.InstallationFence = "1"
			}
			raw, sig = signRunnerCommand(t, c)
			if name == "signature" {
				sig[0] ^= 1
			}
			if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err == nil {
				t.Fatal("conflicting or stale command admitted")
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 1 || scalar(t, s.db, `SELECT admitted_fence FROM runner_commands`) != 2 {
				t.Fatal("refusal changed journal identity")
			}
		})
	}
}
func TestConcurrentSignedCommandHasOneIntent(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	raw, sig := signRunnerCommand(t, runnerCommandFixture(t))
	var wg sync.WaitGroup
	results := make(chan CommandAdmission, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { r, e := s.AdmitControlCommand(context.Background(), raw, sig); results <- r; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	fresh := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for r := range results {
		if !r.Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatal("multiple command intents")
	}
}
func TestSignedCommandChecksTimeAfterConnectionWait(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	expiry := time.Now().UnixMilli() + 300
	c.ExpiresMS = strconv.FormatInt(expiry, 10)
	raw, sig := signRunnerCommand(t, c)
	holder, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	prior := s.db.Stats().WaitCount
	result := make(chan error, 1)
	go func() { _, err := s.AdmitControlCommand(context.Background(), raw, sig); result <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for s.db.Stats().WaitCount == prior && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.db.Stats().WaitCount == prior {
		holder.Rollback()
		t.Fatalf("command did not wait for owned connection: %v", <-result)
	}
	for time.Now().UnixMilli() < expiry {
		time.Sleep(time.Millisecond)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, control.ErrInvalidCommand) {
		t.Fatalf("expired envelope after wait: %v", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 0 {
		t.Fatal("expired command persisted")
	}
}
func TestMissingControllerCannotAdmitSignedCommand(t *testing.T) {
	directory := privateDirectory(t)
	s, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, sig := signRunnerCommand(t, runnerCommandFixture(t))
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("implicit enrollment: %v", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 0 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_deployment`) != 0 {
		t.Fatal("untrusted request created authority")
	}
}

func insertCommandResourceFloor(t *testing.T, s *Store, c control.Command, fence, generation, writer int64, owner, epoch string) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'volume','fixture-owned-data',?,?,?,?,?,'present','unknown',?,1,NULL,1,1)`, fixtureID(450), owner, c.Payload.DataNamespaceID, c.Payload.ReleaseSHA256, generation, epoch, fence, writer, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
}
func TestDurableResourceFloorKeepsOriginalFence(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	// This is a journal fence fixture, explicitly not native physical evidence.
	insertCommandResourceFloor(t, s, c, 2, 1, 1, c.InstallationID, c.ControllerEpoch)
	c.InstallationFence = "2"
	raw, sig = signRunnerCommand(t, c)
	result, err := s.AdmitControlCommand(context.Background(), raw, sig)
	if err != nil || !result.Replayed || result.AdmittedFence != 1 || result.CurrentFence != 2 {
		t.Fatalf("reconciled observation rewrote original fence: %v", err)
	}
	c.InstallationFence = "1"
	raw, sig = signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("old worker survived observed floor")
	}
	if scalar(t, s.db, `SELECT admitted_fence FROM runner_commands`) != 1 {
		t.Fatal("immutable admitted fence overwritten")
	}
}
func TestResourceIdentityAndGenerationRefusal(t *testing.T) {
	for _, kind := range []string{"fence", "generation", "writer", "foreign_owner", "foreign_epoch"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			c := runnerCommandFixture(t)
			owner, epoch := c.InstallationID, c.ControllerEpoch
			c.InstallationFence = "5"
			c.TargetGeneration = "3"
			c.Payload.WriterFence = "7"
			switch kind {
			case "fence":
				c.InstallationFence = "4"
			case "generation":
				c.TargetGeneration = "2"
			case "writer":
				c.Payload.WriterFence = "6"
			case "foreign_owner":
				owner = fixtureID(451)
			case "foreign_epoch":
				epoch = fixtureID(452)
			}
			insertCommandResourceFloor(t, s, c, 5, 3, 7, owner, epoch)
			raw, sig := signRunnerCommand(t, c)
			if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err == nil {
				t.Fatal("stale or foreign resource identity admitted")
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 0 {
				t.Fatal("refusal persisted command")
			}
		})
	}
}
func TestCommandAdmissionInsertRollback(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	raw, sig := signRunnerCommand(t, runnerCommandFixture(t))
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_command_failure BEFORE INSERT ON runner_commands BEGIN SELECT RAISE(ABORT,'fixture command failure');END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("insert failure ignored")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 0 {
		t.Fatal("failed command left partial intent")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_command_failure`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil || result.Replayed {
		t.Fatalf("retry after rollback: %v", err)
	}
}
