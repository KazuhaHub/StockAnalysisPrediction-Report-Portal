package app

import (
	"bytes"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testPlatformDocumentRead(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	platformGrantLaunchUser(t, s, reader, id)
	binding, err := s.inspectPlatformLaunchAccess(reader, id)
	if err != nil {
		t.Fatal(err)
	}
	before := dumpOf(t, s.st)
	result, err := s.readPlatformLaunchDocument(reader, binding)
	if err != nil || result.SourceSHA256 != binding.frontendSHA || !platformValidSHA(result.DocumentSHA256) || !strings.Contains(string(result.Content), "Fixture") || !strings.Contains(result.CSP, "sandbox allow-scripts") {
		t.Fatalf("incorrect document: %+v %v", result, err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("compilation changed durable state")
	}
	changed := binding
	changed.generation = "2"
	if got, err := s.readPlatformLaunchDocument(reader, changed); err == nil || !reflect.DeepEqual(got, appplatform.FrontendDocument{}) {
		t.Fatal("superseded binding received document")
	}
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, id)
	if got, err := s.readPlatformLaunchDocument(reader, binding); err == nil || !reflect.DeepEqual(got, appplatform.FrontendDocument{}) {
		t.Fatal("revoked reader received derived document")
	}
}
func TestPlatformDocumentReadSQLite(t *testing.T)   { testPlatformDocumentRead(t, false) }
func TestPlatformDocumentReadPostgres(t *testing.T) { testPlatformDocumentRead(t, true) }

func TestPostgresPlatformDocumentReadExpiryAfterReleaseWait(t *testing.T) {
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
				result appplatform.FrontendDocument
				err    error
			}
			done := make(chan outcome, 1)
			go func() { r, err := s.readPlatformLaunchDocument(reader, binding); done <- outcome{r, err} }()
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
			if got.err == nil || !reflect.DeepEqual(got.result, appplatform.FrontendDocument{}) {
				t.Fatalf("expired %s disclosed launch binding: %+v", kind, got)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("expired read changed durable state")
			}
		})
	}
}
