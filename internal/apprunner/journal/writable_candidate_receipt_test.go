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

func sameWritableReceipt(a, b WritableCandidateRunningReceipt) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// Synthetic historical rows test reader validation only. No native opaque
// launch/process observation is manufactured or accepted for settlement.
func syntheticWritableRunningFixture(t *testing.T) (*Store, []byte, []byte, WritableCandidateRunningReceipt) {
	t.Helper()
	s, raw, sig, claim := writableClaimFixture(t)
	if _, err := s.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	tx, f, err := s.beginWritableCandidate(context.Background(), raw, sig, "unknown", &claim.state.marker)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var m writableCandidateClaimMarker
	json.Unmarshal([]byte(claim.state.marker), &m)
	request, err := writableNativeRequest(f, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	native := driver.WritableCandidateLaunchSnapshot{Launch: driver.CandidateLaunchSnapshot{Identity: request.Identity, Data: request.Data, Platform: request.Platform, ImageID: f.source.writer.Native.ImageID, PolicySHA256: strings.Repeat("a", 64), MountsSHA256: strings.Repeat("b", 64)}, Binding: request.Binding, SandboxProfile: control.WritableCandidateSandboxProfile, SeccompSHA256: f.source.writer.Native.SeccompSHA256}
	p := writablePreflight(native)
	now := time.Now().UnixMilli()
	m.Preflight = &p
	m.PreflightBoundMS = strconv.FormatInt(now, 10)
	r := WritableCandidateRunningReceipt{Format: writableRunningFormat, Claim: m, NativeID: strings.Repeat("c", 64), NativeObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10)}
	if !validWritableRunningReceipt(r) {
		t.Fatal("synthetic running fixture shape")
	}
	encoded, _ := json.Marshal(r)
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE stage='start'`, string(encoded), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE runner_resources SET native_id=?,observed_state='running',revision=2,observed_ms=?,updated_ms=? WHERE kind='container'`, r.NativeID, now, now); err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, r
}
func TestWritableCandidateHistoricalRunningReadAndCurrentKey(t *testing.T) {
	s, raw, sig, want := syntheticWritableRunningFixture(t)
	defer s.Close()
	if got, err := s.ReadWritableCandidateRunningReceipt(context.Background(), raw, sig); err != nil || !sameWritableReceipt(got, want) {
		t.Fatal("historical running read", err)
	}
	if _, err := s.ReadWritableCandidateReservation(context.Background(), raw, sig); err == nil {
		t.Fatal("applied process treated as pending reservation")
	}
	if _, err := s.ClaimWritableCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("applied process reclaimed")
	}
	var c control.WritableCandidateCommand
	json.Unmarshal(raw, &c)
	var w control.DataWriterCommand
	json.Unmarshal(c.WriterRaw, &w)
	var ref control.Command
	json.Unmarshal(w.DataRaw, &ref)
	e := enrollmentFixture(31)
	e.ControlRevision = 4
	if _, err := s.db.Exec(`UPDATE runner_deployment SET control_revision=4,verification_key=?,key_fingerprint=?,revision=revision+1`, e.PublicKey, e.Fingerprint); err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
	a := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: 4, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	ref.ControlRevision = "4"
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	var err error
	w.DataRaw, w.DataSignature, err = control.Sign(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c.WriterRaw, c.WriterSignature, err = control.SignDataWriter(w, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig = signWritableIntentAuthority(t, c, key, a)
	if got, err := s.ReadWritableCandidateRunningReceipt(context.Background(), raw, sig); err != nil || !sameWritableReceipt(got, want) || got.Claim.ControlRevision != "3" {
		t.Fatal("renewal rewrote process history", err)
	}
}
func TestWritableCandidateRunningReadRejectsDrift(t *testing.T) {
	for _, mode := range []string{"native_id", "revision", "resource_time", "command_time", "ack", "writer_ack", "source_volume", "policy", "image", "claim_revision", "bound_time"} {
		t.Run(mode, func(t *testing.T) {
			s, raw, sig, r := syntheticWritableRunningFixture(t)
			defer s.Close()
			queries := map[string]string{
				"native_id":     `UPDATE runner_resources SET native_id='` + strings.Repeat("d", 64) + `' WHERE kind='container'`,
				"revision":      `UPDATE runner_resources SET revision=1 WHERE kind='container'`,
				"resource_time": `UPDATE runner_resources SET observed_ms=observed_ms+1 WHERE kind='container'`,
				"command_time":  `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE stage='start'`,
				"ack":           `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='start'`,
				"writer_ack":    `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='activate'`,
				"source_volume": `UPDATE runner_resources SET revision=2 WHERE kind='volume'`,
			}
			if q := queries[mode]; q != "" {
				if _, err := s.db.Exec(q); err != nil {
					t.Fatal(err)
				}
			} else {
				switch mode {
				case "policy":
					r.Claim.Preflight.SeccompSHA256 = strings.Repeat("f", 64)
				case "image":
					r.Claim.Preflight.Native.ImageID = "sha256:" + strings.Repeat("f", 64)
				case "claim_revision":
					r.Claim.ControlRevision = "2"
				case "bound_time":
					r.Claim.PreflightBoundMS = r.Claim.ExpiresMS
				}
				encoded, _ := json.Marshal(r)
				if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE stage='start'`, string(encoded)); err != nil {
					t.Fatal(err)
				}
			}
			if result, err := s.ReadWritableCandidateRunningReceipt(context.Background(), raw, sig); err == nil || result.Format != "" {
				t.Fatal("altered process history accepted", mode, err)
			}
		})
	}
}
func TestWritableCandidateCannotBindOrSettleDecodedEvidence(t *testing.T) {
	s, _, _, claim := writableClaimFixture(t)
	defer s.Close()
	if _, err := s.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.bindWritableCandidatePreflight(context.Background(), claim.state, driver.WritableCandidateLaunch{}); err == nil {
		t.Fatal("zero launch bound native preflight")
	}
	if r, err := s.settleWritableCandidateRunning(context.Background(), claim, driver.WritableCandidateRunningObservation{}); err == nil || r.Format != "" {
		t.Fatal("zero process proof settled row")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='start' AND state='unknown'`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container' AND revision=1 AND observed_ms IS NULL`) != 1 {
		t.Fatal("invented proof changed state")
	}
}
