package app

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestQuoteOrderAuditRecordsOnlyEffectiveChanges(t *testing.T) {
	s := quoteAdminServer(t)
	for _, body := range []string{`{"order":"tencent,sina"}`, `{"order":"sina,tencent"}`, `{"order":" SINA ,tencent,sina "}`, `{"ttlOpenSecs":45}`, `{"order":""}`} {
		if w := quoteAdminSave(t, s, body); w.Code != 200 {
			t.Fatalf("save %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	rows, total := s.st.ListAudit(AuditFilter{Action: "quote.order"})
	if total != 2 || len(rows) != 2 {
		t.Fatalf("effective changes recorded %d times, want 2", total)
	}
	for _, row := range rows {
		if row.Actor != "root" || row.TargetID != setQuoteSourceOrder || row.IP == "" {
			t.Fatalf("missing audit identity: %+v", row)
		}
		var detail struct {
			From []string `json:"from"`
			To   []string `json:"to"`
		}
		if err := json.Unmarshal([]byte(row.Detail), &detail); err != nil {
			t.Fatal(err)
		}
		if len(detail.From) != 2 || len(detail.To) != 2 || reflect.DeepEqual(detail.From, detail.To) {
			t.Fatalf("missing effective transition: %s", row.Detail)
		}
	}
	if w := quoteAdminSave(t, s, `{"order":"unknown","ttlOpenSecs":60}`); w.Code != http.StatusBadRequest {
		t.Fatal("unknown source accepted")
	}
	if _, total := s.st.ListAudit(AuditFilter{Action: "quote.order"}); total != 2 {
		t.Fatal("refused request wrote an order audit")
	}
}

func TestQuoteOrderTransactionSerializesBothDrivers(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { assertQuoteOrderTransaction(t, newTestStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "audit_log")
		t.Cleanup(func() {
			if err := st.DeleteSetting(setQuoteSourceOrder); err != nil {
				t.Error(err)
			}
		})
		assertQuoteOrderTransaction(t, st)
	})
}

func assertQuoteOrderTransaction(t *testing.T, st *Store) {
	t.Helper()
	if err := st.DeleteSetting(setQuoteSourceOrder); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entry := AuditEntry{Actor: "admin", IP: "203.0.113.8"}
	if err := st.saveQuoteOrder(ctx, "sina", entry); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			order := "sina,tencent"
			if i%2 == 0 {
				order = "tencent,sina"
			}
			errs <- st.saveQuoteOrder(ctx, order, entry)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.query(`SELECT detail FROM audit_log WHERE action='quote.order' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	previous := quoteShippedOrder()
	count := 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var delta struct {
			From []string `json:"from"`
			To   []string `json:"to"`
		}
		if err := json.Unmarshal([]byte(raw), &delta); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(previous, delta.From) || reflect.DeepEqual(delta.From, delta.To) {
			t.Fatalf("non-serial audit chain: previous=%v delta=%s", previous, raw)
		}
		previous = delta.To
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if count < 2 || !reflect.DeepEqual(previous, effectiveQuoteOrder(st.GetSetting(setQuoteSourceOrder, ""))) {
		t.Fatal("audit chain does not end at persisted order")
	}
}

func TestQuoteOrderAuditFailureRollsBackTheOrder(t *testing.T) {
	s := quoteAdminServer(t)
	if err := s.st.SetSetting(setQuoteSourceOrder, "sina"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}
	w := quoteAdminSave(t, s, `{"order":"tencent,sina","ttlOpenSecs":45}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("unaudited save succeeded: %d %s", w.Code, w.Body.String())
	}
	if got := s.st.GetSetting(setQuoteSourceOrder, ""); got != "sina" {
		t.Fatalf("failed audit left changed order %s", got)
	}
	if got := s.st.GetSetting(setQuoteTTLOpenSecs, "absent"); got != "absent" {
		t.Fatal("failed order save partially wrote TTL")
	}
}
