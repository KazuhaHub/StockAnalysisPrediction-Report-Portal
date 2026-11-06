package app

import (
	"bytes"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testPlatformAssetRead(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	platformGrantLaunchUser(t, s, reader, id)
	binding, err := s.inspectPlatformLaunchAccess(reader, id)
	if err != nil {
		t.Fatal(err)
	}
	before := dumpOf(t, s.st)
	asset, err := s.readPlatformLaunchAsset(reader, binding, binding.entry)
	if err != nil || len(asset.Content) == 0 || asset.Metadata.Path != binding.entry || asset.Metadata.ContentType != "text/html; charset=utf-8" || !platformValidSHA(asset.Metadata.SHA256) {
		t.Fatalf("valid asset refused: %+v %v", asset, err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("read changed durable state")
	}
	for _, kind := range []string{"generation", "policy", "release", "frontend", "entry", "installation", "path"} {
		t.Run(kind, func(t *testing.T) {
			expected := binding
			name := binding.entry
			switch kind {
			case "generation":
				expected.generation = "2"
			case "policy":
				expected.policyRevision = "2"
			case "release":
				expected.releaseSHA = strings.Repeat("a", 64)
			case "frontend":
				expected.frontendSHA = strings.Repeat("b", 64)
			case "entry":
				expected.entry = "wrong.html"
			case "installation":
				expected.installationID = platformFixtureID(999)
			case "path":
				name = "../index.html"
			}
			got, err := s.readPlatformLaunchAsset(reader, expected, name)
			if err == nil || !reflect.DeepEqual(got, appplatform.FrontendFile{}) {
				t.Fatal("mismatched request received bytes")
			}
		})
	}
	// A previously returned binding is not a cached authorization permit.
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, id)
	got, err := s.readPlatformLaunchAsset(reader, binding, binding.entry)
	if err == nil || !reflect.DeepEqual(got, appplatform.FrontendFile{}) {
		t.Fatal("revoked reader reused prior binding")
	}
}
func TestPlatformAssetReadSQLite(t *testing.T)   { testPlatformAssetRead(t, false) }
func TestPlatformAssetReadPostgres(t *testing.T) { testPlatformAssetRead(t, true) }
func TestPostgresPlatformAssetReadExpiryAfterReleaseWait(t *testing.T) {
	for _, kind := range []string{"reader", "signing_key"} {
		t.Run(kind, func(t *testing.T) {
			s, reader, id := platformLaunchFixture(t, true)
			platformGrantLaunchUser(t, s, reader, id)
			binding, err := s.inspectPlatformLaunchAccess(reader, id)
			if err != nil {
				t.Fatal(err)
			}
			now, err := platformDatabaseNow(s.st.poolExec())
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.UnixMilli(now + 3000).Truncate(time.Second)
			if kind == "reader" {
				reader.expires = deadline
				platformSeedActorSession(t, s.st, reader)
			} else {
				mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET not_after_ms=?`, deadline.UnixMilli())
			}
			before := dumpOf(t, s.st)
			holder, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			if _, err := holder.Exec(`SELECT sha256 FROM app_platform_releases FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				result appplatform.FrontendFile
				err    error
			}
			done := make(chan outcome, 1)
			go func() { r, err := s.readPlatformLaunchAsset(reader, binding, binding.entry); done <- outcome{r, err} }()
			blocked := false
			limit := time.Now().Add(5 * time.Second)
			for time.Now().Before(limit) {
				var n int
				if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE OF r,f%'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					blocked = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !blocked {
				holder.Rollback()
				t.Fatalf("inspection never reached release lock: %+v", <-done)
			}
			for time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if err := holder.Commit(); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err == nil || !reflect.DeepEqual(got.result, appplatform.FrontendFile{}) {
				t.Fatalf("expired %s disclosed launch binding: %+v", kind, got)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("expired read changed durable state")
			}
		})
	}
}
