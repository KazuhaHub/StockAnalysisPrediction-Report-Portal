//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDataReservationNeedsAdmittedCommand(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	raw, sig := signRunnerCommand(t, runnerCommandFixture(t))
	if result, err := s.ReservePrepareData(context.Background(), raw, sig); err == nil || result != (DataReservation{}) {
		t.Fatal("signature alone created data reservation")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 0 {
		t.Fatal("reservation manufactured command admission")
	}
}
func TestDataReservationPersistsAndReplays(t *testing.T) {
	s, directory := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	first, err := s.ReservePrepareData(context.Background(), raw, sig)
	if err != nil || first.ProjectID != 1 || first.ResourceID != c.Payload.DataNamespaceID || first.NamespaceID != c.Payload.DataNamespaceID || first.Replayed {
		t.Fatalf("fresh reservation: %+v %v", first, err)
	}
	replay, err := s.ReservePrepareData(context.Background(), raw, sig)
	if err != nil || !replay.Replayed || replay.ProjectID != first.ProjectID || replay.OwnershipSHA256 != first.OwnershipSHA256 {
		t.Fatalf("reservation replay: %+v %v", replay, err)
	}
	c.IssuedMS = strconv.FormatInt(time.Now().UnixMilli(), 10)
	c.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+20000, 10)
	raw, sig = signRunnerCommand(t, c)
	renewed, err := s.ReservePrepareData(context.Background(), raw, sig)
	if err != nil || !renewed.Replayed || renewed.ProjectID != first.ProjectID || renewed.OwnershipSHA256 != first.OwnershipSHA256 {
		t.Fatalf("renewed envelope changed logical ownership: %+v %v", renewed, err)
	}
	var state, receipt string
	if err := s.db.QueryRow(`SELECT state,receipt_json FROM runner_commands`).Scan(&state, &receipt); err != nil {
		t.Fatal(err)
	}
	if state != "prepared" || receipt != "{}" || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE observed_state='unknown' AND observed_ms IS NULL`) != 1 {
		t.Fatal("reservation fabricated execution or physical observation")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.ReservePrepareData(context.Background(), raw, sig)
	if err != nil || !again.Replayed || again.ProjectID != first.ProjectID || again.OwnershipSHA256 != first.OwnershipSHA256 {
		t.Fatalf("restart lost reservation: %+v %v", again, err)
	}
	if _, err := reopened.db.Exec(`UPDATE runner_commands SET state='unknown'`); err != nil {
		t.Fatal(err)
	}
	if result, err := reopened.ReservePrepareData(context.Background(), raw, sig); !errors.Is(err, ErrFenceReconciliation) || result != (DataReservation{}) {
		t.Fatal("uncertain work received fresh reservation", err)
	}
}

func TestDataReservationMonotonicAllocationAndConcurrentReplay(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	ctx := context.Background()
	c := runnerCommandFixture(t)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		reservation DataReservation
		err         error
	}
	done := make(chan outcome, 8)
	for range 8 {
		go func() { r, err := s.ReservePrepareData(ctx, raw, sig); done <- outcome{r, err} }()
	}
	fresh := 0
	for range 8 {
		got := <-done
		if got.err != nil || got.reservation.ProjectID != 1 {
			t.Fatalf("concurrent reservation: %+v", got)
		}
		if !got.reservation.Replayed {
			fresh++
		}
	}
	if fresh != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 1 {
		t.Fatal("concurrent requests duplicated reservation")
	}
	if _, err := s.db.Exec(`UPDATE runner_resources SET desired_state='absent',observed_state='absent'`); err != nil {
		t.Fatal(err)
	}
	c.InstallationID = fixtureID(500)
	c.OperationID = fixtureID(501)
	c.EffectID = fixtureID(502)
	c.Payload.DataNamespaceID = fixtureID(503)
	raw, sig = signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	next, err := s.ReservePrepareData(ctx, raw, sig)
	if err != nil || next.ProjectID != 2 || next.Replayed {
		t.Fatalf("absent resource ID was recycled: %+v %v", next, err)
	}
}

func TestDataReservationRejectsChangedRecordOrCommand(t *testing.T) {
	for _, kind := range []string{"ownership", "native", "owner", "release", "namespace", "resource_id", "kind", "desired", "generation", "writer", "epoch", "fence", "observed", "observed_time", "revision", "state", "payload", "renewed_fence", "receipt", "future_time", "same_install_new_namespace"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			c := runnerCommandFixture(t)
			raw, sig := signRunnerCommand(t, c)
			if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			var statement string
			var args []any
			switch kind {
			case "ownership":
				statement = `UPDATE runner_resources SET ownership_sha256=?`
				args = []any{strings.Repeat("b", 64)}
			case "native":
				statement = `UPDATE runner_resources SET native_id='xfs-project/v1/2'`
			case "owner":
				statement = `UPDATE runner_resources SET installation_id=?`
				args = []any{fixtureID(550)}
			case "release":
				statement = `UPDATE runner_resources SET release_sha256=?`
				args = []any{strings.Repeat("b", 64)}
			case "namespace":
				statement = `UPDATE runner_resources SET data_namespace_id=?`
				args = []any{fixtureID(553)}
			case "resource_id":
				statement = `UPDATE runner_resources SET id=?`
				args = []any{fixtureID(554)}
			case "kind":
				statement = `UPDATE runner_resources SET kind='artifact'`
			case "desired":
				statement = `UPDATE runner_resources SET desired_state='absent'`
			case "generation":
				statement = `UPDATE runner_resources SET generation=2`
			case "writer":
				statement = `UPDATE runner_resources SET writer_fence=2`
			case "epoch":
				statement = `UPDATE runner_resources SET controller_epoch=?`
				args = []any{fixtureID(551)}
			case "fence":
				statement = `UPDATE runner_resources SET control_fence=2`
			case "observed":
				statement = `UPDATE runner_resources SET observed_state='present'`
			case "observed_time":
				statement = `UPDATE runner_resources SET observed_ms=1`
			case "revision":
				statement = `UPDATE runner_resources SET revision=2`
			case "state":
				statement = `UPDATE runner_commands SET state='applied'`
			case "payload":
				c.Payload.QuotaBytes = "134217728"
			case "renewed_fence":
				c.InstallationFence = "2"
			case "receipt":
				statement = `UPDATE runner_commands SET receipt_json='{"format":"unexpected"}'`
			case "future_time":
				statement = `UPDATE runner_resources SET created_ms=9223372036854775807,updated_ms=9223372036854775807`
			case "same_install_new_namespace":
				c.Payload.DataNamespaceID = fixtureID(552)
			}
			if statement != "" {
				if _, err := s.db.Exec(statement, args...); err != nil {
					t.Fatal(err)
				}
			} else {
				raw, sig = signRunnerCommand(t, c)
			}
			if result, err := s.ReservePrepareData(context.Background(), raw, sig); err == nil || result != (DataReservation{}) {
				t.Fatal("changed resource/command inherited reservation")
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 1 {
				t.Fatal("refusal allocated a replacement")
			}
		})
	}
}

func TestDataReservationRejectsUnknownAllocatorIdentity(t *testing.T) {
	for _, native := range []string{"external-volume", "xfs-project/v1/0", "xfs-project/v1/01", "xfs-project/v1/+1", "xfs-project/v1/4294967294", "xfs-project/v1/4294967295"} {
		t.Run(native, func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			c := runnerCommandFixture(t)
			insertCommandResourceFloor(t, s, c, 1, 1, 1, fixtureID(560), c.ControllerEpoch)
			if _, err := s.db.Exec(`UPDATE runner_resources SET native_id=?,data_namespace_id=?`, native, fixtureID(561)); err != nil {
				t.Fatal(err)
			}
			raw, sig := signRunnerCommand(t, c)
			if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			if result, err := s.ReservePrepareData(context.Background(), raw, sig); !errors.Is(err, ErrFenceReconciliation) || result != (DataReservation{}) {
				t.Fatalf("unsafe allocator input %q: %+v %v", native, result, err)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 1 {
				t.Fatal("unsafe allocator created mapping")
			}
		})
	}
}

func TestDataReservationRollbackAndPayloadAdmission(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_reservation_failure BEFORE INSERT ON runner_resources BEGIN SELECT RAISE(ABORT,'fixture reservation failure');END`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ReservePrepareData(context.Background(), raw, sig); err == nil || result != (DataReservation{}) {
		t.Fatal("resource insert failure ignored")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
		t.Fatal("failed reservation persisted")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_reservation_failure`); err != nil {
		t.Fatal(err)
	}
	result, err := s.ReservePrepareData(context.Background(), raw, sig)
	if err != nil || result.ProjectID != 1 || result.Replayed {
		t.Fatalf("rollback consumed a project: %+v %v", result, err)
	}
}

func TestDataReservationExpiryAfterConnectionWait(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	expiry := time.Now().UnixMilli() + 400
	c.ExpiresMS = strconv.FormatInt(expiry, 10)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	holder, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	prior := s.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { _, err := s.ReservePrepareData(context.Background(), raw, sig); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for s.db.Stats().WaitCount == prior && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.db.Stats().WaitCount == prior {
		holder.Rollback()
		t.Fatal("reservation did not wait")
	}
	for time.Now().UnixMilli() < expiry {
		time.Sleep(time.Millisecond)
	}
	holder.Rollback()
	if err := <-done; !errors.Is(err, control.ErrInvalidCommand) {
		t.Fatal("expired command allocated data", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
		t.Fatal("expired reservation persisted")
	}
}

func TestDataReservationConcurrentInstallations(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	type envelope struct{ raw, signature []byte }
	commands := []envelope{}
	for i := 0; i < 8; i++ {
		c := runnerCommandFixture(t)
		c.InstallationID = fixtureID(600 + i*4)
		c.OperationID = fixtureID(601 + i*4)
		c.EffectID = fixtureID(602 + i*4)
		c.Payload.DataNamespaceID = fixtureID(603 + i*4)
		raw, sig := signRunnerCommand(t, c)
		if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, envelope{raw, sig})
	}
	type outcome struct {
		reservation DataReservation
		err         error
	}
	done := make(chan outcome, 8)
	for _, command := range commands {
		go func(command envelope) {
			r, err := s.ReservePrepareData(context.Background(), command.raw, command.signature)
			done <- outcome{r, err}
		}(command)
	}
	seen := map[uint32]bool{}
	for range 8 {
		got := <-done
		if got.err != nil || got.reservation.Replayed || seen[got.reservation.ProjectID] || got.reservation.ProjectID < 1 || got.reservation.ProjectID > 8 {
			t.Fatalf("allocation collision: %+v", got)
		}
		seen[got.reservation.ProjectID] = true
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 8 {
		t.Fatal("concurrent installation mapping lost")
	}
}

func TestDataReservationEnvelopeAndCancellationRefusal(t *testing.T) {
	for _, kind := range []string{"unaligned", "signature", "cancel", "revision", "epoch"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			c := runnerCommandFixture(t)
			if kind == "unaligned" {
				c.Payload.QuotaBytes = "513"
			}
			raw, sig := signRunnerCommand(t, c)
			if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "signature":
				sig[0] ^= 1
			case "cancel":
				cancel()
			case "revision":
				c.ControlRevision = "2"
				raw, sig = signRunnerCommand(t, c)
			case "epoch":
				c.ControllerEpoch = fixtureID(680)
				raw, sig = signRunnerCommand(t, c)
			}
			if result, err := s.ReservePrepareData(ctx, raw, sig); err == nil || result != (DataReservation{}) {
				t.Fatal("invalid envelope created reservation")
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
				t.Fatal("refused envelope persisted resource")
			}
		})
	}
}

func TestDataReservationDoesNotReplaceExistingRoot(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	// Historical terminal metadata is not native completion evidence. This fixture
	// only exercises a separate new command against an already reserved root.
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='applied'`); err != nil {
		t.Fatal(err)
	}
	c.OperationID = fixtureID(690)
	c.EffectID = fixtureID(691)
	c.Payload.DataNamespaceID = fixtureID(692)
	raw, sig = signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ReservePrepareData(context.Background(), raw, sig); !errors.Is(err, ErrDataReservationConflict) || result != (DataReservation{}) {
		t.Fatalf("replacement root admitted: %+v %v", result, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 1 {
		t.Fatal("second root created")
	}
}

func TestDataReservationCapacityBoundary(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	// These are retained ledger fixtures, not native resources or readiness proof.
	_, err := s.db.Exec(`WITH RECURSIVE sequence(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM sequence WHERE n<65535)
 INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
 SELECT printf('11111111-1111-4111-8111-%012x',n),?,printf('11111111-1111-4111-8111-%012x',n),'volume','xfs-project/v1/'||n,?,1,?,1,1,'absent','absent',?,1,NULL,1,1 FROM sequence`, fixtureID(750), c.Payload.ReleaseSHA256, c.ControllerEpoch, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	c.IssuedMS = strconv.FormatInt(time.Now().UnixMilli(), 10)
	c.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+29000, 10)
	raw, sig = signRunnerCommand(t, c)
	last, err := s.ReservePrepareData(context.Background(), raw, sig)
	if err != nil || last.ProjectID != 65536 || last.Replayed {
		t.Fatalf("last reservation refused: %+v %v", last, err)
	}
	c.InstallationID = fixtureID(760)
	c.OperationID = fixtureID(761)
	c.EffectID = fixtureID(762)
	c.Payload.DataNamespaceID = fixtureID(763)
	raw, sig = signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ReservePrepareData(context.Background(), raw, sig); !errors.Is(err, ErrFenceReconciliation) || result != (DataReservation{}) {
		t.Fatalf("reservation ceiling exceeded: %+v %v", result, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 65536 {
		t.Fatal("capacity refusal mutated reservations")
	}
}
