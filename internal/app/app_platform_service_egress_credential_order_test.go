package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The admit's target is judged against the reviewed policy before the binding is
// resolved, so a call for another destination never reaches the custody.
func testPlatformServiceEgressCredentialWrongTargetNeverOpens(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	for name, admit := range map[string]transport.EgressAdmit{
		"other origin": {Method: "GET", URL: "https://other.example.com/v1/x", Accept: "application/json", BodySHA256: platformDigest(nil), Credential: "upstream-api"},
		"other path":   {Method: "GET", URL: "https://api.example.com/admin", Accept: "application/json", BodySHA256: platformDigest(nil), Credential: "upstream-api"},
		"other method": {Method: "DELETE", URL: "https://api.example.com/v1/x", BodySHA256: platformDigest(nil), Credential: "upstream-api"},
		"oversize":     {Method: "POST", URL: "https://api.example.com/v1/x", BodyBytes: 1 << 20, BodySHA256: platformDigest(nil), Credential: "upstream-api"},
	} {
		before := platformCredentialOpenCount.Load()
		a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{
			identity: state.identity, resources: state.resources, credential: "upstream-api", decrypt: true, admit: &admit,
		})
		if err == nil {
			a.Close()
			t.Fatal(name, "was not refused")
		}
		if !errors.Is(err, errPlatformServiceEgressDenied) || platformCredentialOpenCount.Load() != before {
			t.Fatal(name, "not denied before decrypt", err)
		}
	}
	// The matching admit still opens it, exactly once.
	admit := platformServiceCredentialAdmit()
	before := platformCredentialOpenCount.Load()
	a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{
		identity: state.identity, resources: state.resources, credential: "upstream-api", decrypt: true, admit: &admit,
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	if platformCredentialOpenCount.Load() != before+1 {
		t.Fatal("matching admit did not open the value once")
	}
}

// A store failure is not a verdict on the binding: it must not look like a
// denial, so no denied row is written and the denied cap is not spent.
func testPlatformServiceEgressCredentialStoreErrorIsUnavailable(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	facts, err := loadPlatformSecretBindingFacts(e, installation, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPlatformSecretBindingUseFor(e, installation, "upstream-api", facts, platformPrincipalService, platformTargetDefaultURL); err != nil {
		t.Fatal(err)
	}
	cancel()
	_, _, err = loadPlatformSecretBindingUseFor(e, installation, "upstream-api", facts, platformPrincipalService, platformTargetDefaultURL)
	if err == nil || !errors.Is(err, errPlatformSecretBindingStore) {
		t.Fatal("a store failure is not marked as one:", err)
	}
	if errors.Is(platformServiceEgressCredentialError(err), errPlatformServiceEgressDenied) {
		t.Fatal("a store failure was mapped to a denial")
	}
	if !errors.Is(platformServiceEgressCredentialError(errAppPlatformCredentialUnavailable), errPlatformServiceEgressDenied) {
		t.Fatal("an unknown or refused binding is not a denial")
	}
	// A binding that is simply absent is a verdict.
	e = s.st.poolExec()
	facts, _ = loadPlatformSecretBindingFacts(e, installation, false)
	_, _, err = loadPlatformSecretBindingUseFor(e, installation, "no-such-binding", facts, platformPrincipalService, platformTargetDefaultURL)
	if err == nil || errors.Is(err, errPlatformSecretBindingStore) {
		t.Fatal("an absent binding is not a plain refusal", err)
	}
}

func TestPlatformServiceEgressCredentialWrongTargetNeverOpensSQLite(t *testing.T) {
	testPlatformServiceEgressCredentialWrongTargetNeverOpens(t, false)
}
func TestPlatformServiceEgressCredentialWrongTargetNeverOpensPostgres(t *testing.T) {
	testPlatformServiceEgressCredentialWrongTargetNeverOpens(t, true)
}
func TestPlatformServiceEgressCredentialStoreErrorIsUnavailableSQLite(t *testing.T) {
	testPlatformServiceEgressCredentialStoreErrorIsUnavailable(t, false)
}
func TestPlatformServiceEgressCredentialStoreErrorIsUnavailablePostgres(t *testing.T) {
	testPlatformServiceEgressCredentialStoreErrorIsUnavailable(t, true)
}
