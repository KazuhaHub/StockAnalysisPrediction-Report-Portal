//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func unknownWriterRecoveryFixture(t *testing.T) (*Store, []byte, []byte, []byte, DataWriterClaim) {
	t.Helper()
	s, raw, _, descriptor := syntheticWriterExecutionFixture(t)
	var writer control.DataWriterCommand
	json.Unmarshal(raw, &writer)
	var ref control.Command
	json.Unmarshal(writer.DataRaw, &ref)
	ref.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+750, 10)
	writer.DataRaw, writer.DataSignature = signRunnerCommand(t, ref)
	raw, sig := signWriterFixture(t, writer)
	if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, descriptor, claim
}

func freshWriterRecoveryEnvelope(t *testing.T, raw []byte, claim DataWriterClaim) ([]byte, []byte) {
	t.Helper()
	var m dataWriterClaimMarker
	json.Unmarshal([]byte(claim.state.marker), &m)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	if remaining := time.Until(time.UnixMilli(expires + 1)); remaining > 0 {
		time.Sleep(remaining)
	}
	var c control.DataWriterCommand
	json.Unmarshal(raw, &c)
	var ref control.Command
	json.Unmarshal(c.DataRaw, &ref)
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	c.DataRaw, c.DataSignature = signRunnerCommand(t, ref)
	return signWriterFixture(t, c)
}

func TestDataWriterRecoveryRequiresExpiredClaimAndFreshAuthority(t *testing.T) {
	s, raw, sig, descriptor, claim := unknownWriterRecoveryFixture(t)
	defer s.Close()
	if _, err := s.prepareDataWriterRecovery(context.Background(), raw, sig, descriptor); err == nil {
		t.Fatal("live execution envelope authorized recovery")
	}
	raw, sig = freshWriterRecoveryEnvelope(t, raw, claim)
	p, err := s.prepareDataWriterRecovery(context.Background(), raw, sig, descriptor)
	if err != nil || p.facts.receipt != claim.state.marker || p.request.Binding.ClaimID == "" {
		t.Fatal("fresh exact observation refused", err)
	}
	if result, err := s.settleRecoveredDataWriter(context.Background(), p, descriptor, driver.DataWriterRecoveryObservation{}); err == nil || result != (DataWriterReceipt{}) {
		t.Fatal("invented recovery observation settled unknown claim")
	}
	for _, native := range []*driver.CandidateRuntime{nil, {}} {
		if result, err := s.ReconcileDataWriter(context.Background(), raw, sig, descriptor, native); err == nil || result != (DataWriterReceipt{}) {
			t.Fatal("missing actual provider restored authority")
		}
	}
	if scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='volume'`) != 2 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='activate' AND state='unknown'`) != 1 {
		t.Fatal("observation failure changed journal")
	}
	if _, err := s.ClaimDataWriter(context.Background(), raw, sig); err == nil {
		t.Fatal("observation minted another execution handle")
	}

}

func TestDataWriterRecoveryRefusesCurrentStateDrift(t *testing.T) {
	for _, mode := range []string{"authority", "ack", "fence", "marker", "source", "container"} {
		t.Run(mode, func(t *testing.T) {
			s, raw, _, descriptor, claim := unknownWriterRecoveryFixture(t)
			defer s.Close()
			raw, sig := freshWriterRecoveryEnvelope(t, raw, claim)
			queries := map[string]string{
				"authority": `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"ack":       `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='activate'`,
				"fence":     `UPDATE runner_resources SET writer_fence=writer_fence+1`,
				"marker":    `UPDATE runner_commands SET receipt_json='{}' WHERE kind='activate'`,
				"source":    `UPDATE runner_commands SET receipt_json='{}' WHERE kind='prepare-data'`,
				"container": `UPDATE runner_resources SET kind='container'`,
			}
			if _, err := s.db.Exec(queries[mode]); err != nil {
				t.Fatal(err)
			}
			if _, err := s.prepareDataWriterRecovery(context.Background(), raw, sig, descriptor); err == nil {
				t.Fatal("drift authorized recovery", mode)
			}
		})
	}
}

func TestDataWriterRecoveredReceiptSeparatesObservationWindow(t *testing.T) {
	s, _, _, original := syntheticAppliedWriterFixture(t)
	defer s.Close()
	r := original
	expires, _ := strconv.ParseInt(r.Claim.ExpiresMS, 10, 64)
	issued := expires + 1
	r.Format = dataWriterRecoveredReceiptFormat
	r.RecoveryEnvelopeSHA256 = strings.Repeat("d", 64)
	r.RecoveryIssuedMS = strconv.FormatInt(issued, 10)
	r.RecoveryExpiresMS = strconv.FormatInt(issued+25000, 10)
	r.Native.ObservedMS = strconv.FormatInt(issued+1, 10)
	r.SettledMS = r.Native.ObservedMS
	r.BindingSHA256 = dataWriterReceiptBinding(r)
	raw, _ := json.Marshal(r)
	if _, err := ParseDataWriterReceipt(raw); err != nil {
		t.Fatal("valid recovered evidence shape", err)
	}
	for _, mode := range []string{"original_format", "old_window", "late", "binding", "extra_original_field"} {
		t.Run(mode, func(t *testing.T) {
			changed := r
			switch mode {
			case "original_format":
				changed.Format = dataWriterReceiptFormat
			case "old_window":
				changed.RecoveryIssuedMS = changed.Claim.ExpiresMS
			case "late":
				changed.SettledMS = changed.RecoveryExpiresMS
			case "binding":
				changed.RecoveryEnvelopeSHA256 = strings.Repeat("e", 64)
			case "extra_original_field":
				changed = original
				changed.RecoveryIssuedMS = "1"
			}
			if mode != "binding" {
				changed.BindingSHA256 = dataWriterReceiptBinding(changed)
			}
			encoded, _ := json.Marshal(changed)
			if _, err := ParseDataWriterReceipt(encoded); err == nil {
				t.Fatal("invalid observation semantics accepted", mode)
			}
		})
	}
}

func TestDataWriterRecoveredReceiptCurrentKeyPreservesObservation(t *testing.T) {
	s, raw, _, r := syntheticAppliedWriterFixture(t)
	defer s.Close()
	// Synthetic historical rows test reader/key continuity only. No native
	// observation is constructed or submitted to the live settlement API.
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	r.Claim.ExpiresMS = strconv.FormatInt(claimed+1, 10)
	if remaining := time.Until(time.UnixMilli(claimed + 2)); remaining > 0 {
		time.Sleep(remaining)
	}
	var c control.DataWriterCommand
	json.Unmarshal(raw, &c)
	var ref control.Command
	json.Unmarshal(c.DataRaw, &ref)
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	c.DataRaw, c.DataSignature = signRunnerCommand(t, ref)
	raw, sig := signWriterFixture(t, c)
	r.Format = dataWriterRecoveredReceiptFormat
	r.RecoveryEnvelopeSHA256 = candidateHash(raw)
	r.RecoveryIssuedMS, r.RecoveryExpiresMS = ref.IssuedMS, ref.ExpiresMS
	r.Native.ObservedMS, r.SettledMS = ref.IssuedMS, ref.IssuedMS
	r.BindingSHA256 = dataWriterReceiptBinding(r)
	encoded, _ := json.Marshal(r)
	if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=?,updated_ms=? WHERE kind='activate'`, string(encoded), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE runner_resources SET observed_ms=?,updated_ms=? WHERE kind='volume'`, now, now); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadDataWriterReceipt(context.Background(), raw, sig); err != nil || got != r {
		t.Fatal("recovered historical read", err)
	}
	e := enrollmentFixture(31)
	e.ControlRevision = 4
	if _, err := s.db.Exec(`UPDATE runner_deployment SET control_revision=4,verification_key=?,key_fingerprint=?,revision=revision+1`, e.PublicKey, e.Fingerprint); err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
	authority := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: 4, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	ref.ControlRevision = "4"
	var err error
	c.DataRaw, c.DataSignature, err = control.Sign(ref, key, authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	raw, sig = signWriterWithAuthority(t, c, key, authority)
	if got, err := s.ReadDataWriterReceipt(context.Background(), raw, sig); err != nil || got != r || got.ControlRevision != "3" {
		t.Fatal("current-key read changed recovery authority history", err)
	}
}
