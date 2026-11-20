package app

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// A signed background call end to end through the coordinator: the runner receives only computed
// headers, the checks are answered, and neither the log nor the audit holds the AccessKey secret,
// the security token or the signature. The summary counts uses per binding and action.
func testPlatformServiceEgressCoordinatorServesSignedCalls(t *testing.T, pg bool) {
	s, admin, installation, credential := platformServiceACS3Fixture(t, pg, platformPrincipalsBoth, "QueryAccountBalance@2017-12-14")
	var logs syncLogBuffer
	previousOutput, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput); log.SetFlags(previousFlags) })
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	admit := platformServiceACS3Admit("QueryAccountBalance", "2017-12-14")
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
	headers, _ := command.SignedHeaderList()
	if command.CredentialName != "qwen-billing" || command.SigningScheme != "acs3" || command.CredentialHeader != "" || len(command.CredentialValue) != 0 || len(headers) < 8 ||
		command.ActorScope != platformServiceEgressActorScope(session.command.SessionID) {
		t.Fatal("command does not carry the computed headers for the service")
	}
	authorization := string(appplatform.ACS3HeaderValue(headers, "authorization"))
	signatureHex := authorization[strings.LastIndex(authorization, "=")+1:]
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
		t.Fatal("checks of a good signed call were not answered")
	}
	// Re-reviewing the binding for other actions between the checks: the next one is not answered.
	acs3Activate(t, s, admin, installation, credential, platformPrincipalsBoth, "DescribeBill@2017-12-14")
	if ask(3) {
		t.Fatal("a check was answered after the action list changed")
	}
	outcome, _ := transport.EncodeEgressOutcome(transport.EgressOutcome{Class: transport.EgressOutcomeUncertain, RetryAfter: -1})
	if err := call.Send(transport.EgressFrameOutcome, outcome); err != nil {
		t.Fatal(err)
	}
	// The old action is now outside the policy: denied before any decrypt.
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
		t.Fatal("an action outside the policy was not denied", code, err)
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("a denied signing call reached the custody")
	}
	waitEgress(t, func() bool {
		c.audit.mu.Lock()
		defer c.audit.mu.Unlock()
		w := c.audit.windows[installation]
		return w != nil && w.unresolved["denied"] == 1 && w.bindings["qwen-billing"]["uncertain"] == 1
	})
	clock.Advance(11 * time.Minute)
	c.flushSummaries(clock.Now(), false)
	rows := egressAuditRows(t, s, platformServiceEgressAuditSummary, installation)
	if len(rows) != 1 {
		t.Fatal("summary rows", rows)
	}
	var summary struct {
		Bindings []struct {
			Binding string         `json:"binding"`
			Count   int            `json:"count"`
			Actions map[string]int `json:"actions"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal([]byte(rows[0]), &summary); err != nil || len(summary.Bindings) != 1 || summary.Bindings[0].Binding != "qwen-billing" ||
		summary.Bindings[0].Actions["QueryAccountBalance@2017-12-14"] != 1 || len(summary.Bindings[0].Actions) != 1 {
		t.Fatal("per-action summary", rows[0], err)
	}
	c.shutdown()
	audit := platformServiceCredentialAuditText(t, s)
	for _, secret := range []string{acs3FixtureSecret, acs3FixtureToken, signatureHex, authorization,
		base64.StdEncoding.EncodeToString([]byte(acs3FixtureSecret)), base64.StdEncoding.EncodeToString([]byte(acs3FixtureToken)), base64.StdEncoding.EncodeToString([]byte(authorization))} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("Portal's log carries signing material", secret)
		}
		if strings.Contains(audit, secret) {
			t.Fatal("audit carries signing material", secret)
		}
	}
}

func TestPlatformServiceEgressCoordinatorServesSignedCallsSQLite(t *testing.T) {
	testPlatformServiceEgressCoordinatorServesSignedCalls(t, false)
}
func TestPlatformServiceEgressCoordinatorServesSignedCallsPostgres(t *testing.T) {
	testPlatformServiceEgressCoordinatorServesSignedCalls(t, true)
}
