//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func signBackendRegistrationFixture(t *testing.T, reference, referenceSignature []byte, effectID, documentSHA string) ([]byte, []byte) {
	t.Helper()
	c := control.BackendRegistrationCommand{Format: control.BackendRegistrationFormat, EffectID: effectID, CandidateRaw: reference, CandidateSignature: referenceSignature, DocumentSHA256: documentSHA}
	var err error
	c.PayloadSHA256, err = control.BackendRegistrationLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	e := enrollmentFixture(30)
	a := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: e.ControlRevision, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	raw, sig, err := control.SignBackendRegistration(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func TestBackendRegistrationAdmissionBindsExistingCandidate(t *testing.T) {
	// Synthetic applied candidate/data rows establish repository invariants only;
	// this fixture is not proof of a native process, key delivery or registration.
	s, reference, referenceSignature, _ := appliedCandidateReceiptFixture(t)
	defer s.Close()
	beforeResources := scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`)
	raw, sig := signBackendRegistrationFixture(t, reference, referenceSignature, fixtureID(950), strings.Repeat("c", 64))
	admitted, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, sig)
	if err != nil || admitted.Replayed || admitted.State != "prepared" {
		t.Fatal("registration intent", admitted, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != beforeResources || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate' AND kind='activate' AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL`) != 1 {
		t.Fatal("registration admission fabricated physical state")
	}
	replayed, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, sig)
	if err != nil || !replayed.Replayed || replayed.EffectID != admitted.EffectID {
		t.Fatal("registration replay", err)
	}
	if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("registration crossed candidate admission")
	}
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("registration crossed data admission")
	}
	changed, sigChanged := signBackendRegistrationFixture(t, reference, referenceSignature, fixtureID(950), strings.Repeat("d", 64))
	if _, err := s.AdmitBackendRegistrationCommand(context.Background(), changed, sigChanged); err == nil {
		t.Fatal("effect identity rebound to another document")
	}
	duplicate, sigDuplicate := signBackendRegistrationFixture(t, reference, referenceSignature, fixtureID(951), strings.Repeat("c", 64))
	if _, err := s.AdmitBackendRegistrationCommand(context.Background(), duplicate, sigDuplicate); err == nil {
		t.Fatal("duplicate registration stage admitted")
	}
}

func TestBackendRegistrationAdmissionRefusesChangedSource(t *testing.T) {
	for _, mode := range []string{"unknown", "ack", "native", "stopped", "fence", "missing", "partial_receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, reference, referenceSignature, _ := appliedCandidateReceiptFixture(t)
			defer s.Close()
			var ref control.CandidateCommand
			if err := json.Unmarshal(reference, &ref); err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := s.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "unknown":
				exec(`UPDATE runner_commands SET state='unknown' WHERE effect_id=?`, ref.EffectID)
			case "ack":
				exec(`UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE effect_id=?`, ref.EffectID)
			case "native":
				exec(`UPDATE runner_resources SET native_id=? WHERE id=?`, strings.Repeat("f", 64), ref.Payload.ResourceID)
			case "stopped":
				exec(`UPDATE runner_resources SET observed_state='stopped' WHERE id=?`, ref.Payload.ResourceID)
			case "fence":
				exec(`UPDATE runner_resources SET control_fence=control_fence+1 WHERE id=?`, ref.Payload.ResourceID)
			case "missing":
				exec(`DELETE FROM runner_resources WHERE id=?`, ref.Payload.ResourceID)
			case "partial_receipt":
				exec(`UPDATE runner_commands SET receipt_json='{}' WHERE effect_id=?`, ref.EffectID)
			}
			raw, sig := signBackendRegistrationFixture(t, reference, referenceSignature, fixtureID(950), strings.Repeat("c", 64))
			if _, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, sig); err == nil {
				t.Fatal("changed candidate authorized delivery", mode)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate'`) != 0 {
				t.Fatal("failed registration admission wrote intent")
			}
		})
	}
}
