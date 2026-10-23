package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func TestFallbackModesTier1(t *testing.T) {
	s := newV1Server(t)
	for _, value := range []string{`"on"`, `"wrong"`, `null`, `true`, `1`, `{}`} {
		recorder := saveForecastSettings(t, s, `{"mode":"dry","alert_email_to":"new@example.com","fallback":`+value+`}`)
		var response map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusBadRequest || response["code"] != "forecast_bad_fallback" {
			t.Fatalf("Tier 2 fallback accepted: %s => %d %s", value, recorder.Code, recorder.Body.String())
		}
		if s.st.GetSetting(setForecastAuto, "0") != "0" || s.st.GetSetting(setForecastAlertEmailTo, "") != "" || s.st.GetSetting(setForecastFallback, "off") != "off" {
			t.Fatal("invalid fallback wrote another field")
		}
	}
	for _, value := range []string{"shadow", "shadow", "off", "off"} {
		recorder := saveForecastSettings(t, s, `{"fallback":"`+value+`"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("valid fallback rejected: %s", recorder.Body.String())
		}
		if got := s.st.GetSetting(setForecastFallback, "off"); got != value {
			t.Fatalf("fallback not persisted: %s", got)
		}
		reader := httptest.NewRecorder()
		s.apiForecastSettings(reader, httptest.NewRequest("GET", "/api/admin/forecast", nil), "admin")
		var got map[string]any
		if err := json.Unmarshal(reader.Body.Bytes(), &got); err != nil || got["fallback"] != value {
			t.Fatalf("fallback not readable: %s %v", reader.Body.String(), err)
		}
	}
	rows, total := s.st.ListAudit(AuditFilter{Action: AuditForecastFallback})
	if total != 2 || len(rows) != 2 {
		t.Fatalf("no-op fallback filled audit log: %+v", rows)
	}
	for _, row := range rows {
		var detail map[string]string
		if err := json.Unmarshal([]byte(row.Detail), &detail); err != nil || detail["from"] == detail["to"] {
			t.Fatalf("bad transition: %+v %v", row, err)
		}
	}
}

func TestForecastFallbackAuditFailureRollsBackMode(t *testing.T) {
	s := newV1Server(t)
	if _, err := s.st.exec(`DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}
	recorder := saveForecastSettings(t, s, `{"fallback":"shadow","mode":"dry"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unaudited shadow mode enabled: %d %s", recorder.Code, recorder.Body.String())
	}
	if s.st.GetSetting(setForecastFallback, "off") != "off" || s.st.GetSetting(setForecastAuto, "0") != "0" {
		t.Fatal("audit failure left shadow or evaluator enabled")
	}
}

func TestForecastFallbackTransactionSerializesBothDrivers(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { assertFallbackTransitionChain(t, newTestStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "audit_log")
		if err := st.DeleteSetting(setForecastFallback); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := st.DeleteSetting(setForecastFallback); err != nil {
				t.Error(err)
			}
		})
		assertFallbackTransitionChain(t, st)
	})
}

func TestForecastFallbackTransactionAcrossSQLiteConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fallback.db")
	first, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	assertFallbackTransitionChain(t, first, second)
}

func TestForecastFallbackOmittedFieldAndNullStoredValue(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.saveForecastFallback(context.Background(), "shadow", AuditEntry{Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	if rec := saveForecastSettings(t, s, `{"mode":"dry"}`); rec.Code != http.StatusOK || s.st.GetSetting(setForecastFallback, "") != "shadow" {
		t.Fatal("omitted fallback reset existing mode")
	}
	if _, err := s.st.exec(`UPDATE meta SET v=NULL WHERE k=?`, setForecastFallback); err != nil {
		t.Fatal(err)
	}
	if err := s.st.saveForecastFallback(context.Background(), "shadow", AuditEntry{Actor: "admin"}); err != nil || s.st.GetSetting(setForecastFallback, "") != "shadow" {
		t.Fatalf("NULL persisted value was not normalized: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.st.saveForecastFallback(ctx, "off", AuditEntry{}); err == nil || s.st.GetSetting(setForecastFallback, "") != "shadow" {
		t.Fatal("cancelled save changed fallback")
	}
}

func assertFallbackTransitionChain(t *testing.T, st *Store, peers ...*Store) {
	t.Helper()
	stores := append([]*Store{st}, peers...)
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Go(func() {
			next := "off"
			if i%2 == 0 {
				next = "shadow"
			}
			if err := stores[i%len(stores)].saveForecastFallback(context.Background(), next, AuditEntry{Actor: "admin", ActorOU: 42, IP: "127.0.0.1"}); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	rows, err := st.query(`SELECT detail,actor_ou,ip FROM audit_log WHERE action=? ORDER BY id`, AuditForecastFallback)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	previous, count := "off", 0
	for rows.Next() {
		var raw, ip string
		var ou int64
		if err := rows.Scan(&raw, &ou, &ip); err != nil {
			t.Fatal(err)
		}
		var detail struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.From != previous || detail.From == detail.To || ou != 42 || ip != "127.0.0.1" {
			t.Fatalf("non-serial or unattributed transition: %s %d %s", raw, ou, ip)
		}
		previous = detail.To
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if count == 0 || previous != st.GetSetting(setForecastFallback, "off") {
		t.Fatal("transition chain does not end at saved mode")
	}
}

func TestForecastFallbackStoredUnknownIsOff(t *testing.T) {
	for _, value := range []string{"", "on", "invalid", "OFF", "shadow", "off"} {
		want := "off"
		if value == "shadow" {
			want = "shadow"
		}
		if got := forecastFallbackMode(value); got != want {
			t.Fatalf("stored %q enabled fallback %q", value, got)
		}
	}
}
