//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func journalCandidateFixture(t *testing.T) control.CandidateCommand {
	t.Helper()
	old := runnerCommandFixture(t)
	return control.CandidateCommand{Format: control.CandidateFormat, DeploymentID: old.DeploymentID, ControllerEpoch: old.ControllerEpoch, ControlRevision: old.ControlRevision, InstallationID: old.InstallationID, OperationID: old.OperationID, TargetGeneration: old.TargetGeneration, Stage: "start", EffectNumber: "1", EffectID: old.EffectID, Kind: "start-candidate", InstallationFence: old.InstallationFence, IssuedMS: old.IssuedMS, ExpiresMS: old.ExpiresMS, Payload: control.CandidatePayload{ResourceID: fixtureID(420), DataNamespaceID: old.Payload.DataNamespaceID, ReleaseSHA256: old.Payload.ReleaseSHA256, Platform: "linux/amd64", DataFormat: "1", WriterFence: "1", SandboxProfile: control.CandidateSandboxProfile}}
}
func signJournalCandidate(t *testing.T, c control.CandidateCommand) ([]byte, []byte) {
	t.Helper()
	digest, err := control.CandidateLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.PayloadSHA256 = digest
	e := enrollmentFixture(30)
	a := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: e.ControlRevision, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	raw, sig, err := control.SignCandidate(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}
func TestCandidateAdmissionPersistsOnlyIntentAndReopens(t *testing.T) {
	s, directory := enrolledCommandStore(t)
	c := journalCandidateFixture(t)
	raw, sig := signJournalCandidate(t, c)
	first, err := s.AdmitCandidateCommand(context.Background(), raw, sig)
	if err != nil || first.Replayed || first.State != "prepared" {
		t.Fatal("candidate intent", first, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
		t.Fatal("admission fabricated a container")
	}
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); !errors.Is(err, control.ErrInvalidCommand) {
		t.Fatal("candidate crossed prepare-data admission domain", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.AdmitCandidateCommand(context.Background(), raw, sig)
	if err != nil || !replay.Replayed || replay.EffectID != first.EffectID {
		t.Fatal("reopen replay", replay, err)
	}
	c.InstallationFence = "2"
	raw, sig = signJournalCandidate(t, c)
	if _, err := reopened.AdmitCandidateCommand(context.Background(), raw, sig); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatal("unreconciled takeover accepted", err)
	}
	if scalar(t, reopened.db, `SELECT COUNT(*) FROM runner_commands`) != 1 {
		t.Fatal("duplicate candidate intent")
	}
}
func TestCandidateAdmissionRejectsChangedLogicalResourceAndPendingWork(t *testing.T) {
	for _, name := range []string{"resource", "release", "namespace", "effect_id", "pending", "signature", "stale"} {
		t.Run(name, func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			c := journalCandidateFixture(t)
			raw, sig := signJournalCandidate(t, c)
			if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "resource":
				c.Payload.ResourceID = fixtureID(421)
			case "release":
				c.Payload.ReleaseSHA256 = string(bytes.Repeat([]byte{'b'}, 64))
			case "namespace":
				c.Payload.DataNamespaceID = fixtureID(422)
			case "effect_id":
				c.EffectID = fixtureID(423)
			case "pending":
				c.OperationID = fixtureID(424)
				c.EffectID = fixtureID(425)
			case "stale":
				c.InstallationFence = "1"
				if _, err := s.db.Exec(`UPDATE runner_commands SET admitted_fence=2`); err != nil {
					t.Fatal(err)
				}
			}
			raw, sig = signJournalCandidate(t, c)
			if name == "signature" {
				sig[0] ^= 1
			}
			if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err == nil {
				t.Fatal("changed candidate admitted", name)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
				t.Fatal("refused intent changed journal")
			}
		})
	}
}
func TestCandidateAdmissionCanceledContext(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := journalCandidateFixture(t)
	c.IssuedMS = strconv.FormatInt(time.Now().UnixMilli()-100, 10)
	c.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+10000, 10)
	raw, sig := signJournalCandidate(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.AdmitCandidateCommand(ctx, raw, sig); err == nil {
		t.Fatal("canceled candidate admitted")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 0 {
		t.Fatal("canceled intent persisted")
	}
}

func TestCandidateAdmissionEnforcesResourceFloors(t *testing.T) {
	for _, name := range []string{"fence", "generation", "writer", "foreign_owner", "foreign_epoch"} {
		t.Run(name, func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			c := journalCandidateFixture(t)
			old := runnerCommandFixture(t)
			owner, epoch := c.InstallationID, c.ControllerEpoch
			fence, generation, writer := int64(1), int64(1), int64(1)
			switch name {
			case "fence":
				fence = 2
			case "generation":
				generation = 2
			case "writer":
				writer = 2
			case "foreign_owner":
				owner = fixtureID(451)
			case "foreign_epoch":
				epoch = fixtureID(452)
			}
			insertCommandResourceFloor(t, s, old, fence, generation, writer, owner, epoch)
			raw, sig := signJournalCandidate(t, c)
			if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err == nil {
				t.Fatal("candidate escaped durable resource floor", name)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 0 {
				t.Fatal("refused candidate persisted")
			}
		})
	}
}
func TestCandidateResourceCannotBeReusedOrRebound(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(strconv.FormatBool(replay), func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			c := journalCandidateFixture(t)
			raw, sig := signJournalCandidate(t, c)
			if replay {
				if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container','fixture-owned-container',?,1,?,1,1,'present','unknown',?,1,NULL,1,1)`, c.Payload.ResourceID, c.InstallationID, c.Payload.DataNamespaceID, c.Payload.ReleaseSHA256, c.ControllerEpoch, string(bytes.Repeat([]byte{'b'}, 64)))
			if err != nil {
				t.Fatal(err)
			}
			admitted, err := s.AdmitCandidateCommand(context.Background(), raw, sig)
			if replay {
				if err != nil || !admitted.Replayed || admitted.State != "prepared" {
					t.Fatal("historical resource replay", admitted, err)
				}
			} else if !errors.Is(err, ErrCommandConflict) {
				t.Fatal("new effect reused an existing resource", err)
			}
			if _, err := s.db.Exec(`UPDATE runner_resources SET installation_id=? WHERE id=?`, fixtureID(455), c.Payload.ResourceID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); !errors.Is(err, ErrCommandConflict) {
				t.Fatal("resource substitution accepted", err)
			}
		})
	}
}
