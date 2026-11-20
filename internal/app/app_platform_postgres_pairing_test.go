package app

import (
	"testing"
)

// A PostgreSQL role is bound only as pg_password: every other operation refuses a value that is a
// username and password, and pg_password refuses any other value. Nothing leaks past custody.
func testPlatformPostgresCredentialPairing(t *testing.T, pg bool) {
	s, admin, installation, _, _ := platformPostgresFixture(t, pg, true)
	role, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Role", value: []byte(platformPostgresCredentialJSON)})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Plain", value: []byte("disposable-upstream-value")})
	if err != nil {
		t.Fatal(err)
	}
	sealed := func(id string) (string, int64) {
		return scalar[string](t, s.st, `SELECT sealed_value FROM app_platform_credentials WHERE id=?`, id), scalar[int64](t, s.st, `SELECT value_revision FROM app_platform_credentials WHERE id=?`, id)
	}
	roleSealed, roleRevision := sealed(role.ID)
	plainSealed, plainRevision := sealed(plain.ID)
	for _, operation := range []string{"http_bearer", "http_api_key", "raw_read", "acs3_hmac_sha256"} {
		if err := s.checkPlatformCredentialPairing(operation, role.ID, roleRevision, roleSealed); err == nil {
			t.Errorf("%s accepted a PostgreSQL role", operation)
		}
		if err := s.checkPlatformCredentialPairing(operation, plain.ID, plainRevision, plainSealed); err != nil {
			t.Errorf("%s refused an ordinary value: %v", operation, err)
		}
	}
	if err := s.checkPlatformCredentialPairing("pg_password", role.ID, roleRevision, roleSealed); err != nil {
		t.Errorf("pg_password refused a role: %v", err)
	}
	if err := s.checkPlatformCredentialPairing("pg_password", plain.ID, plainRevision, plainSealed); err == nil {
		t.Error("pg_password accepted an ordinary value")
	}
	// The reviews refuse it before a plan exists.
	before := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`)
	for _, operation := range []string{"http_bearer", "http_api_key"} {
		if _, err := s.createPlatformSecretBindingReviewScoped(admin, installation, "role-as-header", role.ID, operation, "", "", nil); err == nil {
			t.Errorf("%s review accepted a PostgreSQL role", operation)
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) != before {
		t.Fatal("a refused review wrote a plan")
	}
}

func TestPlatformPostgresCredentialPairingSQLite(t *testing.T) {
	testPlatformPostgresCredentialPairing(t, false)
}
func TestPlatformPostgresCredentialPairingPostgres(t *testing.T) {
	testPlatformPostgresCredentialPairing(t, true)
}
