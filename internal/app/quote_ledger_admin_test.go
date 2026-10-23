package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func ledgerAdminGet(t *testing.T, s *Server) map[string]any {
	t.Helper()
	w := quoteAdminDo(t, s, http.MethodGet, "/api/admin/quote", "root", "")
	if w.Code != 200 {
		t.Fatalf("admin state: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLedgerAdminStateDescribesExplicitGrants(t *testing.T) {
	s := quoteAdminServer(t)
	out := ledgerAdminGet(t, s)
	if !reflect.DeepEqual(out["ledgerChain"], []any{quoteSourceTencent}) || out["ledgerPrimary"] != quoteSourceTencent || out["ledgerShadowPeer"] != quoteSourceSina {
		t.Fatalf("ledger source state missing or wrong: %+v", out)
	}
	if health, ok := out["ledgerHealth"].([]any); !ok || len(health) != 0 {
		t.Fatal("unused ledger health must be an empty array")
	}
	for _, raw := range out["sources"].([]any) {
		row := raw.(map[string]any)
		if row["source"] == quoteSourceTencent {
			if row["cashDividends"] != true || row["reportsKind"] != true || !reflect.DeepEqual(row["settled"], []any{"sh", "sz"}) {
				t.Fatalf("Tencent ledger evidence: %+v", row)
			}
		} else if row["cashDividends"] != false || row["reportsKind"] != false || !reflect.DeepEqual(row["settled"], []any{}) {
			t.Fatalf("unadmitted writer: %+v", row)
		}
		wantShadow := []any{}
		if row["source"] == quoteSourceSina {
			wantShadow = []any{"sh", "sz"}
		}
		if !reflect.DeepEqual(row["settledShadow"], wantShadow) {
			t.Fatalf("wrong shadow-only markets: %+v", row)
		}
	}
	if err := s.st.SetSetting(setQuoteSourceOrder, quoteSourceSina); err != nil {
		t.Fatal(err)
	}
	out = ledgerAdminGet(t, s)
	if !reflect.DeepEqual(out["ledgerChain"], []any{}) || out["ledgerPrimary"] != "" {
		t.Fatal("disabled writer remained primary")
	}
}

func TestLedgerAdminHealthCannotBeConfusedWithDisplayHealth(t *testing.T) {
	s := quoteAdminServer(t)
	s.quotes.sources = []quoteSource{ledgerWalkSource(quoteSourceTencent, func(context.Context, string, string, string, string) (*QuoteResp, error) {
		return nil, errors.New("ledger-only failure")
	})}
	_, _ = s.quotes.walkLedger(context.Background(), quoteConfig{Order: []string{quoteSourceTencent}}, "sh", "601899", "2026-10-01", "2026-10-22", &ledgerTick{})
	out := ledgerAdminGet(t, s)
	health, ok := out["ledgerHealth"].([]any)
	if !ok || len(health) != 1 {
		t.Fatalf("ledger health missing: %+v", out)
	}
	row := health[0].(map[string]any)
	if row["source"] != quoteSourceTencent || row["consecutiveFailures"] != float64(1) || row["lastError"] != "ledger-only failure" || row["lastSuccess"] != "" || row["lastErrorAt"] == "" {
		t.Fatalf("ledger health wrong: %+v", row)
	}
	display := out["sources"].([]any)[0].(map[string]any)
	if display["lastError"] != "" || display["consecutiveFailures"] != float64(0) {
		t.Fatal("ledger error contaminated display health")
	}
	if w := quoteAdminDo(t, s, http.MethodGet, "/api/admin/quote", "kazuha", ""); w.Code != 403 {
		t.Fatalf("reader received admin diagnostics: %d", w.Code)
	}
	if w := quoteAdminDo(t, s, http.MethodPost, "/api/admin/quote/cache/clear", "root", ""); w.Code != 200 {
		t.Fatalf("clear: %d", w.Code)
	}
	if after := ledgerAdminGet(t, s)["ledgerHealth"]; !reflect.DeepEqual(after, out["ledgerHealth"]) {
		t.Fatal("clearing display cache erased ledger diagnostics")
	}
}

func TestLedgerAdminConfiguredOrderAndShadowAreExplicit(t *testing.T) {
	s := quoteAdminServer(t)
	primary := ledgerWalkSource(quoteSourceTencent, nil)
	cash := ledgerWalkSource(quoteSourceSina, nil)
	cash.reportsKind = false
	shadow := quoteSource{name: quoteSourceYahoo, caps: []quoteCapability{{intervals: []quoteInterval{quoteIntervalSettledShadow}}}}
	s.quotes.sources = []quoteSource{primary, cash, shadow}
	if err := s.st.SetSetting(setQuoteSourceOrder, "yahoo,sina,tencent"); err != nil {
		t.Fatal(err)
	}
	out := ledgerAdminGet(t, s)
	if !reflect.DeepEqual(out["ledgerChain"], []any{"sina", "tencent"}) || out["ledgerPrimary"] != "tencent" || out["ledgerShadowPeer"] != "yahoo" {
		t.Fatalf("configured declarations: %+v", out)
	}
	s.quotes.sources[2].caps = nil
	out = ledgerAdminGet(t, s)
	if out["ledgerShadowPeer"] != "" || !reflect.DeepEqual(out["ledgerChain"], []any{"sina", "tencent"}) {
		t.Fatal("display wildcard entered ledger declarations")
	}
	// Direct helper callers may supply unsanitized order names. A primary with both grants
	// must not become its own comparison peer, and a duplicate may not duplicate the chain.
	s.quotes.sources[0].caps = append(s.quotes.sources[0].caps, quoteCapability{intervals: []quoteInterval{quoteIntervalSettledShadow}})
	s.quotes.sources[2] = shadow
	direct := s.quoteLedgerAdminState(quoteConfig{Order: []string{"missing", "tencent", "tencent", "yahoo"}})
	if !reflect.DeepEqual(direct["ledgerChain"], []string{"tencent"}) || direct["ledgerPrimary"] != "tencent" || direct["ledgerShadowPeer"] != "yahoo" {
		t.Fatalf("duplicate/unknown order or self-shadow bypassed: %+v", direct)
	}
}
