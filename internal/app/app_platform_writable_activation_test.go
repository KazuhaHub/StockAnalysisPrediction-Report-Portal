package app

import (
	"bytes"
	"context"
	"testing"
)

func testPlatformWritableActivationMetadata(t *testing.T, pg bool) {
	s, lease, i := platformWritableAppliedFixture(t, pg)
	defer i.registration.Close()
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := loadPlatformWritableActivationMetadata(s.st.poolExec(), lease, i, now)
	if err != nil || !platformValidSHA(metadata.frontendSHA) || !platformValidSHA(metadata.documentSHA) || len(metadata.audience) != 1 {
		t.Fatal("complete activation metadata", err)
	}
	before := dumpOf(t, s.st)
	if _, err := s.commitPlatformWritableActivation(context.Background(), lease, i, platformWritableBootObservation{}); err == nil {
		t.Fatal("historical receipts activated without actual boot")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("zero boot changed install or audience")
	}
}
func TestPlatformWritableActivationMetadataSQLite(t *testing.T) {
	t.Parallel()
	testPlatformWritableActivationMetadata(t, false)
}
func TestPlatformWritableActivationMetadataPostgres(t *testing.T) {
	t.Parallel()
	testPlatformWritableActivationMetadata(t, true)
}
func testPlatformWritableActivationRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"archive", "plan", "audience", "runtime", "unapplied", "stage"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, i := platformWritableAppliedFixture(t, pg)
			defer i.registration.Close()
			switch mode {
			case "archive":
				archive := scalar[[]byte](t, s.st, `SELECT content FROM app_platform_frontend_artifacts LIMIT 1`)
				archive[0] ^= 1
				mustExec(t, s.st, `UPDATE app_platform_frontend_artifacts SET content=?`, archive)
			case "plan":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=?`, platformDigest([]byte("foreign")))
			case "audience":
				mustExec(t, s.st, `UPDATE user_groups SET created_at='recreated-ou' WHERE is_default=1`)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "unapplied":
				i.applied = false
			case "stage":
				lease.snapshot.Stage = "probe"
			}
			before := dumpOf(t, s.st)
			now, err := platformDatabaseNow(s.st.poolExec())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loadPlatformWritableActivationMetadata(s.st.poolExec(), lease, i, now); err == nil {
				t.Fatal("invalid activation metadata", mode)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("metadata refusal changed state")
			}
		})
	}
}
func TestPlatformWritableActivationRefusalsSQLite(t *testing.T) {
	t.Parallel()
	testPlatformWritableActivationRefusals(t, false)
}
func TestPlatformWritableActivationRefusalsPostgres(t *testing.T) {
	t.Parallel()
	testPlatformWritableActivationRefusals(t, true)
}
