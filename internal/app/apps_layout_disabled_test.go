package app

import (
	"context"
	"testing"
)

func disabledLayoutEntry(t *testing.T, s *Server, id string) appLayoutEntry {
	t.Helper()
	layout, err := s.appLayout()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range layout {
		if e.InstallationID == id {
			return e
		}
	}
	t.Fatalf("installation %s missing from layout", id)
	return appLayoutEntry{}
}

func disablePlatformFixture(t *testing.T, s *Server, id string) {
	t.Helper()
	mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, id)
	mustExec(t, s.st, `UPDATE app_platform_activations SET state='retired' WHERE installation_id=?`, id)
}

func TestLayoutDisabledInstallationKeepsDisplayFromCurrentRelease(t *testing.T) {
	t.Parallel()
	s, reader, id := platformLaunchFixture(t, false)
	platformGrantLaunchUser(t, s, reader, id)
	active := disabledLayoutEntry(t, s, id)
	if active.Display == nil || active.Name == active.AppID {
		t.Fatalf("fixture has no display: %+v", active)
	}
	disablePlatformFixture(t, s, id)
	got := disabledLayoutEntry(t, s, id)
	if got.State != "disabled" || got.Name != active.Name || got.Name == got.AppID || got.Icon != active.Icon || got.Icon == "" || got.Version != active.Version || got.Version == "" || got.PublisherName != active.PublisherName || got.Display == nil {
		t.Fatalf("disabled entry lost its display: got %+v want %+v", got, active)
	}
	page, err := s.listPlatformApps(context.Background(), reader, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Openable || item.Display != nil {
			t.Fatalf("reader directory must not expose a disabled install: %+v", item)
		}
	}
}

func TestLayoutDisabledInstallationWithRevokedKeyFallsBackToAppID(t *testing.T) {
	t.Parallel()
	s, reader, id := platformLaunchFixture(t, false)
	platformGrantLaunchUser(t, s, reader, id)
	disablePlatformFixture(t, s, id)
	mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET revoked_ms=1`)
	got := disabledLayoutEntry(t, s, id)
	if got.Name != got.AppID || got.Display != nil || got.PublisherName != "" || got.Version != "" {
		t.Fatalf("unverified release leaked its display: %+v", got)
	}
}

func TestLayoutDisabledInstallationWithUntrustedPublisherFallsBackToAppID(t *testing.T) {
	t.Parallel()
	s, reader, id := platformLaunchFixture(t, false)
	platformGrantLaunchUser(t, s, reader, id)
	disablePlatformFixture(t, s, id)
	mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
	got := disabledLayoutEntry(t, s, id)
	if got.Name != got.AppID || got.Display != nil {
		t.Fatalf("untrusted publisher leaked its display: %+v", got)
	}
}
