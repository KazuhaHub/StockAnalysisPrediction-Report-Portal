package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func platformBindingWithPrincipals(t *testing.T, s *Server, admin platformActorSession, installation string, credential platformCredentialMetadata, principals string) platformSecretBindingMetadata {
	t.Helper()
	plan, err := s.createPlatformSecretBindingReviewPrincipals(admin, installation, "upstream-api", credential.ID, "http_bearer", principals)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.JSON, `"principals"`) != (principals != "") {
		t.Fatal("review plan principals field", principals, plan.JSON)
	}
	binding, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func testPlatformSecretBindingPrincipals(t *testing.T, pg bool) {
	s, admin, installation, credential := platformSecretBindingFixture(t, pg)
	e := s.st.poolExec()
	use := func(principal string) error {
		facts, err := loadPlatformSecretBindingFacts(e, installation, false)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = loadPlatformSecretBindingUseFor(e, installation, "upstream-api", facts, principal)
		return err
	}
	for _, c := range []struct {
		principals    string
		viewer, svc   bool
		stored        string
		listed        string
		auditContains string
	}{
		{"", true, false, "/1-dev.1", `["viewer"]`, `"Principals":["viewer"]`},
		{platformPrincipalsServiceOnly, false, true, "/2-dev.1", `["service"]`, `"Principals":["service"]`},
		{platformPrincipalsBoth, true, true, "/2-dev.1", `["service","viewer"]`, `"Principals":["service","viewer"]`},
		{"", true, false, "/1-dev.1", `["viewer"]`, `"Principals":["viewer"]`},
	} {
		binding := platformBindingWithPrincipals(t, s, admin, installation, credential, c.principals)
		if got := scalar[string](t, s.st, `SELECT policy_json FROM app_platform_secret_bindings WHERE id=?`, binding.ID); !strings.Contains(got, c.stored) {
			t.Fatal("stored policy", c.principals, got)
		}
		if (use("viewer") == nil) != c.viewer || (use("service") == nil) != c.svc || use("admin") == nil {
			t.Fatal("principal use", c.principals, use("viewer"), use("service"))
		}
		// The foreground loader is the viewer path, whatever the binding says.
		facts, _ := loadPlatformSecretBindingFacts(e, installation, false)
		if _, _, err := loadPlatformSecretBindingUse(e, installation, "upstream-api", facts); (err == nil) != c.viewer {
			t.Fatal("foreground loader ignored principals", c.principals)
		}
		list, err := s.listPlatformSecretBindings(admin, installation)
		if err != nil || len(list) != 1 {
			t.Fatal("list", err)
		}
		if raw, _ := json.Marshal(list[0].Principals); string(raw) != c.listed {
			t.Fatal("listed principals", string(raw))
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.secret_binding_active' AND target_id=? AND detail LIKE ?`, binding.ID, "%"+c.auditContains+"%") < 1 {
			t.Fatal("audit detail lacks principals", c.auditContains)
		}
	}
	// raw_read is a setup binding and never opens to the service.
	if _, err := s.createPlatformSecretBindingReviewPrincipals(admin, installation, "upstream-api", credential.ID, "raw_read", platformPrincipalsServiceOnly); err == nil {
		t.Fatal("raw_read review accepted a service principal")
	}
	if _, err := s.createPlatformSecretBindingReviewPrincipals(admin, installation, "upstream-api", credential.ID, "http_bearer", "admin"); err == nil {
		t.Fatal("review accepted an unknown principal")
	}
}

func TestPlatformSecretBindingPrincipalsSQLite(t *testing.T) {
	testPlatformSecretBindingPrincipals(t, false)
}
func TestPlatformSecretBindingPrincipalsPostgres(t *testing.T) {
	testPlatformSecretBindingPrincipals(t, true)
}
