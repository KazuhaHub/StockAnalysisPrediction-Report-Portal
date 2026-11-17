package app

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformServiceCredentialAuditText is every audit row's detail, joined.
func platformServiceCredentialAuditText(t *testing.T, s *Server) string {
	t.Helper()
	rows, err := s.st.query(`SELECT action,detail FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all strings.Builder
	for rows.Next() {
		var action, detail string
		if err := rows.Scan(&action, &detail); err != nil {
			t.Fatal(err)
		}
		all.WriteString(action + " " + detail + "\n")
	}
	return all.String()
}

func testPlatformServiceEgressCoordinatorServesCredentialCalls(t *testing.T, pg bool) {
	s, admin, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsBoth)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	if strings.Join(session.command.Resources, ",") != strings.Join(platformServiceCredentialResources, ",") {
		t.Fatal("session resources", session.command.Resources)
	}
	admit := platformServiceCredentialAdmit()
	payload, err := transport.EncodeEgressAdmit(admit)
	if err != nil {
		t.Fatal(err)
	}
	call, err := session.runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	defer call.Finish()
	if err := call.Send(transport.EgressFrameAdmit, payload); err != nil {
		t.Fatal(err)
	}
	raw, signature, err := transport.DecodeEgressCommand(egressRecv(t, call, transport.EgressFrameCommand).Payload)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, session.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal("the runner would refuse the command:", err)
	}
	command := verified.Command()
	if command.CredentialName != "upstream-api" || command.CredentialHeader != "Authorization" || string(command.CredentialValue) != "Bearer "+platformServiceCredentialValue ||
		command.ActorScope != platformServiceEgressActorScope(session.command.SessionID) {
		t.Fatal("command does not carry the reviewed credential for the service")
	}
	ask := func(seq byte) bool {
		var nonce [32]byte
		nonce[0], nonce[31] = 'n', seq
		if err := call.Send(transport.EgressFrameChallenge, transport.EncodeEgressChallenge(seq, nonce)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		frame, err := call.Recv(ctx)
		if err != nil {
			return false
		}
		proof, sig, err := transport.DecodeEgressProof(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := control.VerifyNetworkHTTPCheck(command, hex.EncodeToString(nonce[:]), proof, sig, session.authority, time.Now().UnixMilli()); err != nil {
			t.Fatal("proof does not verify:", err)
		}
		return true
	}
	if !ask(1) || !ask(2) {
		t.Fatal("checks of a good credential call were not answered")
	}
	// Revoking the binding between the checks: the next one is not answered.
	id := scalar[string](t, s.st, `SELECT id FROM app_platform_secret_bindings WHERE installation_id=? AND name='upstream-api'`, installation)
	if err := s.revokePlatformSecretBinding(admin, id, scalar[int64](t, s.st, `SELECT revision FROM app_platform_secret_bindings WHERE id=?`, id)); err != nil {
		t.Fatal(err)
	}
	if ask(3) {
		t.Fatal("a check was answered after the binding was revoked")
	}
	outcome, _ := transport.EncodeEgressOutcome(transport.EgressOutcome{Class: transport.EgressOutcomeUncertain, RetryAfter: -1})
	if err := call.Send(transport.EgressFrameOutcome, outcome); err != nil {
		t.Fatal(err)
	}
	// The next call naming the revoked binding is refused before any decrypt.
	before := platformCredentialOpenCount.Load()
	second, err := session.runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Finish()
	if err := second.Send(transport.EgressFrameAdmit, payload); err != nil {
		t.Fatal(err)
	}
	if code, err := transport.DecodeEgressRefusal(egressRecv(t, second, transport.EgressFrameRefusal).Payload); err != nil || code != transport.EgressRefusalDenied {
		t.Fatal("revoked binding not denied", code, err)
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("a denied credential call reached the custody")
	}
	waitEgress(t, func() bool {
		c.audit.mu.Lock()
		defer c.audit.mu.Unlock()
		w := c.audit.windows[installation]
		return w != nil && w.bindings["upstream-api"]["denied"] == 1 && w.bindings["upstream-api"]["uncertain"] == 1
	})
	clock.Advance(11 * time.Minute)
	c.flushSummaries(clock.Now(), false)
	rows := egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)
	if len(rows) != 1 {
		t.Fatal("summary rows", rows)
	}
	var summary struct {
		Bindings []struct {
			Binding  string         `json:"binding"`
			Count    int            `json:"count"`
			Outcomes map[string]int `json:"outcomes"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal([]byte(rows[0]), &summary); err != nil || len(summary.Bindings) != 1 || summary.Bindings[0].Binding != "upstream-api" || summary.Bindings[0].Count != 2 ||
		summary.Bindings[0].Outcomes["denied"] != 1 || summary.Bindings[0].Outcomes["uncertain"] != 1 {
		t.Fatal("per-binding summary", rows[0], err)
	}
	// Nothing Portal wrote carries the value, the header or the sealed form.
	audit := platformServiceCredentialAuditText(t, s)
	for _, secret := range []string{platformServiceCredentialValue, base64.StdEncoding.EncodeToString([]byte("Bearer " + platformServiceCredentialValue)), "Bearer "} {
		if strings.Contains(audit, secret) {
			t.Fatal("audit carries credential material", secret)
		}
	}
}

func testPlatformServiceEgressCoordinatorRefusesViewerOnlyBinding(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, "")
	f := newFakeEgressRunner()
	c, _ := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	payload, _ := transport.EncodeEgressAdmit(platformServiceCredentialAdmit())
	before := platformCredentialOpenCount.Load()
	call, err := session.runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	defer call.Finish()
	if err := call.Send(transport.EgressFrameAdmit, payload); err != nil {
		t.Fatal(err)
	}
	if code, err := transport.DecodeEgressRefusal(egressRecv(t, call, transport.EgressFrameRefusal).Payload); err != nil || code != transport.EgressRefusalDenied {
		t.Fatal("viewer-only binding not denied in the background", code, err)
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("the value was opened for a viewer-only binding")
	}
	// The denial is audited as a denial, with method and origin only.
	waitEgress(t, func() bool { return len(egressAuditRows(t, s, platformServiceEgressAuditDenied, installation)) == 1 })
	if row := egressAuditRows(t, s, platformServiceEgressAuditDenied, installation)[0]; strings.Contains(row, "upstream-api") || strings.Contains(row, platformServiceCredentialValue) {
		t.Fatal("denied row", row)
	}
}

func TestPlatformServiceEgressCoordinatorServesCredentialCallsSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorServesCredentialCalls(t, false)
}
func TestPlatformServiceEgressCoordinatorServesCredentialCallsPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorServesCredentialCalls(t, true)
}
func TestPlatformServiceEgressCoordinatorRefusesViewerOnlyBindingSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorRefusesViewerOnlyBinding(t, false)
}
func TestPlatformServiceEgressCoordinatorRefusesViewerOnlyBindingPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorRefusesViewerOnlyBinding(t, true)
}
