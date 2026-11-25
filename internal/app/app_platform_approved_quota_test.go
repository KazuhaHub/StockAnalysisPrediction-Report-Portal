package app

import (
	"context"
	"testing"
)

func testNamespaceMustMatchTheApprovedQuota(t *testing.T, pg bool) {
	s, _, lease := platformDataIntentFixture(t, pg)
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	read := func() (int64, error) {
		return platformApprovedNamespaceQuota(s.st.poolExec(), lease.installationID, intent.namespaceID)
	}
	if got, err := read(); err != nil || got != 256<<20 {
		t.Fatalf("a namespace at the approved quota: %d %v", got, err)
	}
	for name, statement := range map[string]string{
		"another inode quota": `UPDATE app_platform_data_namespaces SET quota_inodes=70000`,
		"another byte quota":  `UPDATE app_platform_data_namespaces SET quota_bytes=536870912`,
	} {
		mustExec(t, s.st, statement)
		if _, err := read(); err == nil {
			t.Fatalf("%s was accepted as the approved quota", name)
		}
		mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET quota_bytes=268435456,quota_inodes=65536`)
	}
	if _, err := read(); err != nil {
		t.Fatal(err)
	}
}

func TestNamespaceMustMatchTheApprovedQuotaSQLite(t *testing.T) {
	t.Parallel()
	testNamespaceMustMatchTheApprovedQuota(t, false)
}
func TestNamespaceMustMatchTheApprovedQuotaPostgres(t *testing.T) {
	t.Parallel()
	testNamespaceMustMatchTheApprovedQuota(t, true)
}
