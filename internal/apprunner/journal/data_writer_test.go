//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func writerCommandFixture(t *testing.T, source, signature []byte, proof DataPreparationReceipt) control.DataWriterCommand {
	t.Helper()
	encoded, _ := json.Marshal(proof)
	return control.DataWriterCommand{Format: control.DataWriterFormat, EffectID: fixtureID(1100), DataRaw: bytes.Clone(source), DataSignature: bytes.Clone(signature), DataReceiptSHA256: candidateHash(encoded), ResourceID: fixtureID(1101), Platform: "linux/amd64", SandboxProfile: control.DataWriterSandboxProfile}
}
func signWriterFixture(t *testing.T, c control.DataWriterCommand) ([]byte, []byte) {
	t.Helper()
	e := enrollmentFixture(30)
	return signWriterWithAuthority(t, c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: e.ControlRevision, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint})
}
func signWriterWithAuthority(t *testing.T, c control.DataWriterCommand, key ed25519.PrivateKey, a control.Authority) ([]byte, []byte) {
	t.Helper()
	var err error
	c.PayloadSHA256, err = control.DataWriterLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignDataWriter(c, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}
func admittedWriterFixture(t *testing.T) (*Store, []byte, []byte, control.DataWriterCommand) {
	t.Helper()
	// Synthetic applied rows exercise real SQLite invariants/crypto only. They
	// are not native ownership, process exclusion or a writer permission receipt.
	s, source, signature, proof := appliedReceiptFixture(t)
	c := writerCommandFixture(t, source, signature, proof)
	raw, sig := signWriterFixture(t, c)
	if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, c
}
func writerStoreDirectory(t *testing.T, s *Store) string {
	t.Helper()
	var file string
	if err := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&file); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(file)
}

func TestDataWriterAdmissionClaimsNoPhysicalSuccess(t *testing.T) {
	s, raw, sig, c := admittedWriterFixture(t)
	defer s.Close()
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='prepare_data' AND effect_number=2 AND kind='activate' AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL`) != 1 {
		t.Fatal("admission fabricated resources/completion")
	}
	replay, err := s.AdmitDataWriterCommand(context.Background(), raw, sig)
	if err != nil || !replay.Replayed || replay.State != "prepared" {
		t.Fatal("writer replay", replay, err)
	}
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("writer crossed data admission")
	}
	if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("writer crossed candidate admission")
	}
	if _, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("writer crossed registration admission")
	}
	c.EffectID = fixtureID(1102)
	other, otherSig := signWriterFixture(t, c)
	if _, err := s.AdmitDataWriterCommand(context.Background(), other, otherSig); err == nil {
		t.Fatal("writer stage duplicated")
	}
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(claim); !errors.Is(err, ErrDataWriterClaimUnavailable) {
		t.Fatal("live claim serialized", err)
	}
	if _, err := s.ClaimDataWriter(context.Background(), raw, sig); err == nil {
		t.Fatal("unknown writer reclaimed")
	}
	attempt, err := s.RunDataWriterOnce(context.Background(), claim, func(ctx context.Context, p DataWriterPlan) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("writer callback has no deadline")
		}
		if p.Command.ResourceID != c.ResourceID || p.Source.Claim.ResourceID == c.ResourceID {
			t.Fatal("writer source/resource tuple lost")
		}
		p.Command.DataRaw[0] ^= 1
		p.Source.WriterFence = "2"
		return nil
	})
	if err != nil || !attempt.Invoked {
		t.Fatal("guarded invocation", attempt, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_number=2 AND state='unknown' AND ack_attempted_ms IS NULL`) != 1 || scalar(t, s.db, `SELECT revision FROM runner_resources`) != 2 {
		t.Fatal("callback success fabricated physical receipt")
	}
	if attempt, err := s.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error { t.Fatal("writer invoked twice"); return nil }); err == nil || attempt.Invoked {
		t.Fatal("claim copied/replayed")
	}
}

func TestDataWriterAdmissionRefusesChangedSourceAndRecordedResources(t *testing.T) {
	for _, mode := range []string{"unknown", "ack", "missing", "revision", "writer", "fence", "owner", "observed", "timestamp", "receipt", "resource", "container", "history", "receipt_hash", "takeover"} {
		t.Run(mode, func(t *testing.T) {
			s, source, signature, proof := appliedReceiptFixture(t)
			defer s.Close()
			c := writerCommandFixture(t, source, signature, proof)
			queries := map[string]string{
				"unknown":   `UPDATE runner_commands SET state='unknown'`,
				"ack":       `UPDATE runner_commands SET ack_attempted_ms=updated_ms`,
				"missing":   `DELETE FROM runner_resources`,
				"revision":  `UPDATE runner_resources SET revision=3`,
				"writer":    `UPDATE runner_resources SET writer_fence=2`,
				"fence":     `UPDATE runner_resources SET control_fence=2`,
				"owner":     `UPDATE runner_resources SET ownership_sha256='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'`,
				"observed":  `UPDATE runner_resources SET observed_state='stopped'`,
				"timestamp": `UPDATE runner_commands SET updated_ms=updated_ms+1`,
				"receipt":   `UPDATE runner_commands SET receipt_json='{}'`,
			}
			if query := queries[mode]; query != "" {
				if _, err := s.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "resource" || mode == "container" {
				id := fixtureID(1103)
				if mode == "resource" {
					id = c.ResourceID
				}
				_, err := s.db.Exec(`INSERT INTO runner_resources SELECT ?,installation_id,data_namespace_id,'container',?,release_sha256,generation,controller_epoch,control_fence,writer_fence,'stopped','stopped',ownership_sha256,revision,observed_ms,created_ms,updated_ms FROM runner_resources WHERE kind='volume'`, id, strings.Repeat("d", 64))
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "history" {
				_, err := s.db.Exec(`INSERT INTO runner_commands SELECT ?,operation_id,installation_id,'stop',1,'stop',payload_sha256,target_generation,controller_epoch,admitted_fence,'applied','{}',NULL,created_ms,updated_ms FROM runner_commands LIMIT 1`, fixtureID(1104))
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "receipt_hash" {
				c.DataReceiptSHA256 = strings.Repeat("e", 64)
			}
			if mode == "takeover" {
				var ref control.Command
				json.Unmarshal(source, &ref)
				ref.InstallationFence = "2"
				c.DataRaw, c.DataSignature = signRunnerCommand(t, ref)
			}
			raw, sig := signWriterFixture(t, c)
			if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err == nil {
				t.Fatal("unsafe writer intent admitted", mode)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='prepare_data' AND effect_number=2`) != 0 {
				t.Fatal("failed admission wrote writer intent")
			}
		})
	}
}

func TestDataWriterOneShotConcurrentCopiesAndRestart(t *testing.T) {
	s, raw, sig, _ := admittedWriterFixture(t)
	directory := writerStoreDirectory(t, s)
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := enrolledCommandStore(t)
	if attempt, err := other.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error { t.Fatal("foreign journal executed claim"); return nil }); err == nil || attempt.Invoked {
		t.Fatal("foreign owner accepted")
	}
	var calls, attempts atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			attempt, _ := s.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error {
				calls.Add(1)
				return errors.New("synthetic native failure")
			})
			if attempt.Invoked {
				attempts.Add(1)
			}
		})
	}
	group.Wait()
	if calls.Load() != 1 || attempts.Load() != 1 {
		t.Fatal("copied writer claim ran more than once", calls.Load(), attempts.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if admission, err := reopened.AdmitDataWriterCommand(context.Background(), raw, sig); err != nil || !admission.Replayed || admission.State != "unknown" {
		t.Fatal("restart forgot unknown intent", admission, err)
	}
	if _, err := reopened.ClaimDataWriter(context.Background(), raw, sig); err == nil {
		t.Fatal("restart reconstructed executable claim")
	}
	if attempt, err := reopened.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error { t.Fatal("old process handle executed"); return nil }); err == nil || attempt.Invoked {
		t.Fatal("old owner claim adopted")
	}
}

func TestDataWriterRefusesPreflightAndPostflightChanges(t *testing.T) {
	for _, phase := range []string{"before", "during"} {
		for _, mutation := range []string{"source", "fence", "authority", "marker", "ack"} {
			t.Run(phase+"/"+mutation, func(t *testing.T) {
				s, raw, sig, _ := admittedWriterFixture(t)
				defer s.Close()
				claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
				if err != nil {
					t.Fatal(err)
				}
				change := func() {
					queries := map[string]string{
						"source":    `UPDATE runner_commands SET receipt_json='{}' WHERE kind='prepare-data'`,
						"fence":     `UPDATE runner_resources SET control_fence=2`,
						"authority": `UPDATE runner_deployment SET control_revision=control_revision+1`,
						"marker":    `UPDATE runner_commands SET receipt_json='{}' WHERE kind='activate'`,
						"ack":       `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='activate'`,
					}
					if _, err := s.db.Exec(queries[mutation]); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "before" {
					change()
				}
				calls := 0
				attempt, err := s.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error {
					calls++
					if phase == "during" {
						change()
					}
					return nil
				})
				if err == nil || attempt.Invoked != (phase == "during") || (calls == 1) != (phase == "during") {
					t.Fatal("changed durable source/authority escaped checks", attempt, err, calls)
				}
				if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='activate' AND state='applied'`) != 0 {
					t.Fatal("failure fabricated completion")
				}
			})
		}
	}
}

func TestDataWriterCancellationDoesNotRetryInvokedWork(t *testing.T) {
	s, raw, sig, _ := admittedWriterFixture(t)
	defer s.Close()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ClaimDataWriter(canceled, raw, sig); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled claim", err)
	}
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if attempt, err := s.RunDataWriterOnce(canceled, claim, func(context.Context, DataWriterPlan) error { t.Fatal("pre-canceled callback invoked"); return nil }); !errors.Is(err, context.Canceled) || attempt.Invoked {
		t.Fatal("pre-cancel consumed execution", attempt, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempt, err := s.RunDataWriterOnce(ctx, claim, func(context.Context, DataWriterPlan) error { cancel(); return nil })
	if !attempt.Invoked || !errors.Is(err, context.Canceled) {
		t.Fatal("invoked cancellation lost uncertainty", attempt, err)
	}
	if attempt, err := s.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error { t.Fatal("canceled work retried"); return nil }); err == nil || attempt.Invoked {
		t.Fatal("invoked work retried")
	}
}

func TestDataWriterCurrentKeyRenewalPreservesOriginalSource(t *testing.T) {
	s, raw, sig, c := admittedWriterFixture(t)
	defer s.Close()
	e := enrollmentFixture(31)
	e.ControlRevision = 4
	if _, err := s.db.Exec(`UPDATE runner_deployment SET control_revision=4,verification_key=?,key_fingerprint=?,revision=revision+1`, e.PublicKey, e.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("retired key accepted")
	}
	var ref control.Command
	json.Unmarshal(c.DataRaw, &ref)
	ref.ControlRevision = "4"
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
	a := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: 4, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	var err error
	c.DataRaw, c.DataSignature, err = control.Sign(ref, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	raw, sig = signWriterWithAuthority(t, c, key, a)
	if admission, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err != nil || !admission.Replayed {
		t.Fatal("current key could not reference original source", admission, err)
	}
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if attempt, err := s.RunDataWriterOnce(context.Background(), claim, func(_ context.Context, p DataWriterPlan) error {
		if p.Source.ControlRevision != "3" {
			t.Fatal("source ownership revision rewritten")
		}
		return nil
	}); err != nil || !attempt.Invoked {
		t.Fatal(attempt, err)
	}
}

func TestDataWriterClaimExpiresBeforeInvocation(t *testing.T) {
	s, source, _, proof := appliedReceiptFixture(t)
	defer s.Close()
	var ref control.Command
	json.Unmarshal(source, &ref)
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now-10, 10), strconv.FormatInt(now+1500, 10)
	source, signature := signRunnerCommand(t, ref)
	c := writerCommandFixture(t, source, signature, proof)
	raw, sig := signWriterFixture(t, c)
	if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(now + 1501)))
	if attempt, err := s.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error { t.Fatal("expired claim invoked"); return nil }); err == nil || attempt.Invoked {
		t.Fatal("expired authority executed")
	}
}

func TestDataWriterInvocationStopsAtSignedDeadline(t *testing.T) {
	s, source, _, proof := appliedReceiptFixture(t)
	defer s.Close()
	var ref control.Command
	json.Unmarshal(source, &ref)
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now-10, 10), strconv.FormatInt(now+2000, 10)
	source, signature := signRunnerCommand(t, ref)
	raw, sig := signWriterFixture(t, writerCommandFixture(t, source, signature, proof))
	if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := s.RunDataWriterOnce(context.Background(), claim, func(ctx context.Context, _ DataWriterPlan) error {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(time.UnixMilli(now+2000)) {
			t.Fatal("execution not bounded to signed expiry")
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !attempt.Invoked || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("signed expiry did not end invocation", attempt, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='activate' AND state='unknown'`) != 1 {
		t.Fatal("expiry fabricated success/retry")
	}
}

func TestDataWriterCloseWaitsForLiveInvocation(t *testing.T) {
	s, raw, sig, _ := admittedWriterFixture(t)
	directory := writerStoreDirectory(t, s)
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	type outcome struct {
		attempt DataWriterAttempt
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		attempt, err := s.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error { close(started); <-release; return nil })
		done <- outcome{attempt, err}
	}()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	deadline := time.Now().Add(time.Second)
	for !s.closing.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.closing.Load() {
		close(release)
		t.Fatal("close did not begin")
	}
	select {
	case err := <-closed:
		close(release)
		t.Fatal("journal ownership released before invocation settled", err)
	default:
	}
	close(release)
	result := <-done
	if !result.attempt.Invoked || result.err == nil {
		t.Fatal("closing invocation lost uncertainty", result)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if scalar(t, reopened.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='activate' AND state='unknown'`) != 1 {
		t.Fatal("close lost durable uncertainty")
	}
}
