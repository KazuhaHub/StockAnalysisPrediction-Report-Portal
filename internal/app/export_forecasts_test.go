package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// ---------- fixtures ----------

// newExportQuoteVendor is a stub quote vendor with a price table keyed by six-digit code (fen). It
// counts upstream batches and records each batch's size; a code absent from the table is a symbol the
// vendor cannot price.
func newExportQuoteVendor(prices map[string]int64) *quoteVendorStub {
	v := &quoteVendorStub{}
	v.parseBatch = func(targets []quoteTarget) (map[string]*QuoteResp, error) {
		out := map[string]*QuoteResp{}
		for _, t := range targets {
			if fen, ok := prices[t.Code]; ok {
				out[t.Symbol] = &QuoteResp{Symbol: t.Code, Market: t.Market.id, Currency: "CNY", Source: quoteSourceTencent,
					Snapshot: QuoteSnapshot{Last: fen, AsOf: "2026-10-29T14:59:00+08:00", Session: "closed"}}
			}
		}
		return out, nil
	}
	return v
}

func (e *exportEnv) vendor(prices map[string]int64) *quoteVendorStub {
	v := newExportQuoteVendor(prices)
	wireQuoteSources(e.s, v, failingStub("sina has no batch endpoint"))
	return v
}

type forecastSeed struct {
	symbol, madeAt, rtype, stance, date, version string
	entry, term                                  int64 // fen; entry 0 = the producer sent none
}

var forecastSeq int

func seedForecast(t *testing.T, st *Store, f forecastSeed) int64 {
	t.Helper()
	forecastSeq++
	if f.rtype == "" {
		f.rtype = predTestRtype
	}
	if f.stance == "" {
		f.stance = "long"
	}
	if f.date == "" {
		f.date = f.madeAt[:10]
	}
	title := "decision " + strconv.Itoa(forecastSeq)
	rid, _, err := st.UpsertReport(Rep{Symbol: f.symbol, Name: "Name" + f.symbol, Date: f.date, RType: f.rtype, Kind: "K", Title: title, Version: f.version, MD: "b"})
	if err != nil {
		t.Fatal(err)
	}
	claims := []predClaimRow{
		{ClaimKey: "stance", Kind: "stance", Stance: f.stance},
		{ClaimKey: "terminal_value", Kind: "terminal_value", PriceFen: &f.term},
	}
	if f.entry > 0 {
		claims = append(claims, predClaimRow{ClaimKey: "entry_price", Kind: "entry_price", PriceFen: &f.entry})
	}
	id, _, err := st.InsertPredictionSet(rid, &predSetRow{
		Symbol: f.symbol, Rtype: f.rtype, Version: f.version, Title: title, ReportDate: f.date,
		MadeAt: f.madeAt, MadeOn: f.madeAt[:10], Cohort: f.madeAt[:7], Zone: "Asia/Shanghai",
		State: predStateAccepted, PayloadSHA: fmt.Sprintf("sha-%d", forecastSeq), HorizonStd: 1,
	}, claims)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type forecastPreview struct {
	Total int64
	Rows  []map[string]any
}

func (e *exportEnv) preview(body string) (forecastPreview, int, string) {
	e.t.Helper()
	w := e.do("admin", "POST", "/api/admin/exports/preview", body)
	var out forecastPreview
	json.Unmarshal(w.Body.Bytes(), &out)
	return out, w.Code, w.Body.String()
}

func yesFilter(field string) string {
	return `{"field":"` + field + `","operator":"is","values":["yes"]}`
}

func belowFilter() string {
	return `{"op":"and","items":[` + yesFilter("below_entry") + `]}`
}

func exportRowSymbols(rows []map[string]any) []string {
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprint(r["symbol"]))
	}
	return out
}

// ---------- the source ----------

func TestExportForecastsSourceDeclaresFieldsColumnsAndDefaultFormat(t *testing.T) {
	e := newExportEnv(t)
	w := e.do("admin", "GET", "/api/admin/exports/sources", "")
	var out struct {
		Sources []struct {
			Key     string
			Formats []string
			Fields  []struct {
				Key     string
				Type    string
				Options []string
			}
			Columns []struct{ Key string }
		}
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	for _, s := range out.Sources {
		if s.Key != "forecasts" {
			continue
		}
		if len(s.Formats) != 2 || s.Formats[0] != "csv" || s.Formats[1] != "jsonl" {
			t.Fatalf("formats = %v", s.Formats)
		}
		var fields, cols []string
		for _, f := range s.Fields {
			fields = append(fields, f.Key)
			if f.Key == "below_entry" || f.Key == "latest_per_symbol" {
				if f.Type != "enum" || strings.Join(f.Options, ",") != "yes,no" {
					t.Errorf("%s = %+v", f.Key, f)
				}
			}
		}
		for _, c := range s.Columns {
			cols = append(cols, c.Key)
		}
		wantF := "report_date,symbol,rtype,version,stance,latest_per_symbol,below_entry"
		wantC := "set_id,report_id,symbol,name,report_date,made_at,rtype,version,stance,entry_price,terminal_value,latest_price,latest_at,vs_entry_pct"
		if strings.Join(fields, ",") != wantF || strings.Join(cols, ",") != wantC {
			t.Fatalf("fields %v columns %v", fields, cols)
		}
		return
	}
	t.Fatal("forecasts source is missing")
}

func TestExportForecastsQuotesOffRemovesTheFieldAndTheColumns(t *testing.T) {
	e := newExportEnv(t)
	seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-16T14:40:12Z", entry: 1000, term: 1200})
	v := e.vendor(map[string]int64{"600519": 900})
	e.s.st.SetSetting(setQuoteHomeCards, "false")

	w := e.do("admin", "GET", "/api/admin/exports/sources", "")
	var out struct {
		Sources []struct {
			Key     string
			Fields  []struct{ Key string }
			Columns []struct{ Key string }
		}
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	found := false
	for _, s := range out.Sources {
		if s.Key != "forecasts" {
			continue
		}
		found = true
		for _, f := range s.Fields {
			if f.Key == "below_entry" {
				t.Errorf("below_entry must be absent with quotes off")
			}
		}
		for _, c := range s.Columns {
			if c.Key == "latest_price" || c.Key == "latest_at" || c.Key == "vs_entry_pct" {
				t.Errorf("column %s must be absent with quotes off", c.Key)
			}
		}
		if len(s.Fields) != 6 || len(s.Columns) != 11 {
			t.Errorf("remaining fields/columns = %d/%d", len(s.Fields), len(s.Columns))
		}
	}
	if !found {
		t.Fatal("forecasts source is missing")
	}
	// The absent field is a client mistake, not an empty result.
	if _, code, body := e.preview(`{"source":"forecasts","filter":` + belowFilter() + `}`); code != http.StatusBadRequest {
		t.Errorf("below_entry with quotes off = %d %s", code, body)
	}
	if _, code, body := e.preview(`{"source":"forecasts","columns":["latest_price"]}`); code != http.StatusBadRequest {
		t.Errorf("latest_price column with quotes off = %d %s", code, body)
	}
	// And a default preview still works, sending nothing to the vendor.
	pv, code, body := e.preview(`{"source":"forecasts"}`)
	if code != 200 || pv.Total != 1 || len(pv.Rows[0]) != 11 {
		t.Fatalf("default preview with quotes off: %d %s", code, body)
	}
	if v.calls != 0 {
		t.Errorf("quotes off must send no codes to the vendor, got %d calls", v.calls)
	}
}

// ---------- rendering ----------

func TestExportForecastsRenderYuanAndPercent(t *testing.T) {
	e := newExportEnv(t)
	seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-16T14:40:12Z", entry: 1000, term: 1250, stance: "no_position"})
	seedForecast(t, e.s.st, forecastSeed{symbol: "000001", madeAt: "2026-10-15T14:40:12Z", term: 705}) // no entry price
	seedForecast(t, e.s.st, forecastSeed{symbol: "300750", madeAt: "2026-10-14T14:40:12Z", entry: 333, term: 400})
	e.vendor(map[string]int64{"600519": 900, "000001": 650, "300750": 334})

	pv, code, body := e.preview(`{"source":"forecasts"}`)
	if code != 200 || len(pv.Rows) != 3 {
		t.Fatalf("%d %s", code, body)
	}
	// Newest made_at first.
	r := pv.Rows[0]
	if r["symbol"] != "600519" || fmt.Sprint(r["entry_price"]) != "10" && fmt.Sprint(r["entry_price"]) != "10.00" {
		t.Fatalf("row 0 = %v", r)
	}
	// Raw JSON keeps the two decimals.
	for _, want := range []string{`"entry_price":10.00`, `"terminal_value":12.50`, `"latest_price":9.00`, `"vs_entry_pct":-10.00`, `"stance":"no_position"`, `"name":"Name600519"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response lacks %s: %s", want, body)
		}
	}
	// Missing entry: blank entry and blank percentage, but the price is shown.
	r = pv.Rows[1]
	if r["symbol"] != "000001" || r["entry_price"] != nil || r["vs_entry_pct"] != nil || fmt.Sprint(r["latest_price"]) != "6.5" && fmt.Sprint(r["latest_price"]) != "6.50" {
		t.Fatalf("row without an entry price = %v", r)
	}
	// (3.34-3.33)/3.33 = 0.3003% -> 0.30
	if got := fmt.Sprint(pv.Rows[2]["vs_entry_pct"]); got != "0.3" && got != "0.30" {
		t.Errorf("vs_entry_pct = %s", got)
	}
	if !strings.Contains(body, `"latest_at":"`) {
		t.Errorf("latest_at missing: %s", body)
	}
}

func TestPctChangeRoundsHalfAwayFromZeroToTwoDecimals(t *testing.T) {
	for _, c := range []struct {
		latest, entry int64
		want          string
	}{
		{900, 1000, "-10.00"}, {1100, 1000, "10.00"}, {1000, 1000, "0.00"}, {334, 333, "0.30"},
		{1, 3, "-66.67"}, {2, 3, "-33.33"}, {10001, 10000, "0.01"}, {10000, 10001, "-0.01"},
	} {
		if got := string(pctChange(c.latest, c.entry)); got != c.want {
			t.Errorf("pctChange(%d,%d) = %s, want %s", c.latest, c.entry, got, c.want)
		}
	}
	if string(yuan(5)) != "0.05" || string(yuan(123456)) != "1234.56" {
		t.Error("yuan formatting")
	}
}

func TestExportForecastsCSVAndJSONLExports(t *testing.T) {
	e := newExportEnv(t)
	seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-16T14:40:12Z", entry: 1000, term: 1200})
	e.vendor(map[string]int64{"600519": 900})
	j := e.run2(`{"source":"forecasts"}`)
	if j.Format != "csv" || j.Status != exportDone || j.RowsDone != 1 {
		t.Fatalf("%+v", j)
	}
	recs := parseCSV(t, e.fileOf(j))
	row := recs[1]
	if row[colIndex(t, recs[0], "entry_price")] != "10.00" || row[colIndex(t, recs[0], "latest_price")] != "9.00" ||
		row[colIndex(t, recs[0], "vs_entry_pct")] != "-10.00" || row[colIndex(t, recs[0], "terminal_value")] != "12.00" {
		t.Fatalf("csv row = %v", row)
	}
	j = e.run2(`{"source":"forecasts","format":"jsonl","columns":["symbol","entry_price","vs_entry_pct"]}`)
	if got := strings.TrimSpace(string(e.fileOf(j))); got != `{"symbol":"600519","entry_price":10.00,"vs_entry_pct":-10.00}` {
		t.Fatalf("jsonl = %s", got)
	}
}

// ---------- the screen ----------

func TestExportForecastsBelowEntryScreen(t *testing.T) {
	e := newExportEnv(t)
	st := e.s.st
	seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-16T14:40:12Z", entry: 1000, term: 1200}) // 9.00 < 10.00
	seedForecast(t, st, forecastSeed{symbol: "000001", madeAt: "2026-10-16T14:41:12Z", entry: 1000, term: 1200}) // 10.00 == 10.00
	seedForecast(t, st, forecastSeed{symbol: "000002", madeAt: "2026-10-16T14:42:12Z", term: 1200})              // no entry price, priced far below anything
	seedForecast(t, st, forecastSeed{symbol: "300750", madeAt: "2026-10-16T14:43:12Z", entry: 1000, term: 1200}) // vendor cannot price it
	e.vendor(map[string]int64{"600519": 900, "000001": 1000, "000002": 1})

	pv, code, body := e.preview(`{"source":"forecasts","filter":` + belowFilter() + `}`)
	if code != 200 || pv.Total != 1 || len(pv.Rows) != 1 || pv.Rows[0]["symbol"] != "600519" {
		t.Fatalf("below_entry = yes: %d %s", code, body)
	}
	// The complement: everything that is not shown to be below, including the set with no entry
	// price and the symbol nobody could price.
	pv, _, body = e.preview(`{"source":"forecasts","filter":{"op":"and","items":[{"field":"below_entry","operator":"is","values":["no"]}]}}`)
	if pv.Total != 3 {
		t.Fatalf("below_entry = no: %s", body)
	}
	got := map[string]bool{}
	for _, s := range exportRowSymbols(pv.Rows) {
		got[s] = true
	}
	if got["600519"] || !got["000002"] || !got["300750"] || !got["000001"] {
		t.Fatalf("below_entry = no rows: %v", got)
	}
	// An unpriced symbol is blank in an ordinary preview.
	pv, _, _ = e.preview(`{"source":"forecasts","filter":{"op":"and","items":[{"field":"symbol","operator":"is","values":["300750"]}]}}`)
	if pv.Rows[0]["latest_price"] != nil || pv.Rows[0]["vs_entry_pct"] != nil || pv.Rows[0]["latest_at"] != nil {
		t.Fatalf("unpriced row = %v", pv.Rows[0])
	}
	// And the export applies the same screen.
	j := e.run2(`{"source":"forecasts","filter":` + belowFilter() + `}`)
	if j.Status != exportDone || j.RowsDone != 1 || j.RowsEstimate != 1 {
		t.Fatalf("export: %+v", j)
	}
}

func TestExportForecastsWithoutEntryPriceNeverMatchesBelowEntry(t *testing.T) {
	e := newExportEnv(t)
	seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-16T14:40:12Z", term: 1200}) // no entry
	e.vendor(map[string]int64{"600519": 1})
	pv, code, body := e.preview(`{"source":"forecasts","filter":` + belowFilter() + `}`)
	if code != 200 || pv.Total != 0 || len(pv.Rows) != 0 {
		t.Fatalf("%d %s", code, body)
	}
}

func TestExportForecastsBelowEntryUnderOrAndNot(t *testing.T) {
	e := newExportEnv(t)
	seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-16T14:40:12Z", entry: 1000, term: 1})
	seedForecast(t, e.s.st, forecastSeed{symbol: "000001", madeAt: "2026-10-15T14:40:12Z", entry: 1000, term: 1})
	e.vendor(map[string]int64{"600519": 900, "000001": 1100})
	f := `{"op":"or","items":[` + yesFilter("below_entry") + `,{"field":"symbol","operator":"is","values":["000001"]}]}`
	if pv, _, body := e.preview(`{"source":"forecasts","filter":` + f + `}`); pv.Total != 2 {
		t.Fatalf("or: %s", body)
	}
	f = `{"op":"and","items":[{"field":"below_entry","operator":"is_not","values":["no"]}]}`
	if pv, _, body := e.preview(`{"source":"forecasts","filter":` + f + `}`); pv.Total != 1 || pv.Rows[0]["symbol"] != "600519" {
		t.Fatalf("is_not no: %s", body)
	}
	bad := `{"op":"and","items":[{"field":"below_entry","operator":"is","values":["maybe"]}]}`
	if _, code, _ := e.preview(`{"source":"forecasts","filter":` + bad + `}`); code != http.StatusBadRequest {
		t.Fatalf("bad value = %d", code)
	}
}

func TestExportForecastsOneUpstreamBatchPerChunkRegardlessOfRows(t *testing.T) {
	e := newExportEnv(t)
	prices := map[string]int64{}
	// 120 symbols, 4 sets each: 480 rows, and 3 chunks of at most 50 symbols.
	for i := 0; i < 120; i++ {
		sym := fmt.Sprintf("60%04d", i)
		prices[sym] = 900
		for k := 0; k < 4; k++ {
			seedForecast(t, e.s.st, forecastSeed{symbol: sym, madeAt: fmt.Sprintf("2026-10-%02dT14:40:12Z", 10+k), entry: 1000, term: 1})
		}
	}
	v := e.vendor(prices)
	pv, code, body := e.preview(`{"source":"forecasts","page_size":200,"filter":` + belowFilter() + `}`)
	if code != 200 || pv.Total != 480 || len(pv.Rows) != 200 {
		t.Fatalf("%d total=%d rows=%d %s", code, pv.Total, len(pv.Rows), body[:min(len(body), 200)])
	}
	// Count and page share one computation: three chunks, once.
	if v.calls != 3 {
		t.Fatalf("upstream batches = %d, want 3 (480 rows over 120 symbols in chunks of %d)", v.calls, exportQuoteChunk)
	}
	// The export over the same screen prices from the shared cache: still no per-row calls.
	j := e.run2(`{"source":"forecasts","filter":` + belowFilter() + `}`)
	if j.RowsDone != 480 || v.calls > 6 {
		t.Fatalf("rows=%d upstream batches=%d", j.RowsDone, v.calls)
	}
}

func TestExportForecastsPriceColumnsFetchPerBatchNotPerRow(t *testing.T) {
	e := newExportEnv(t)
	e.s.exports.batchSize = 100
	prices := map[string]int64{}
	for i := 0; i < 30; i++ {
		sym := fmt.Sprintf("60%04d", i)
		prices[sym] = 900
		for k := 0; k < 10; k++ {
			seedForecast(t, e.s.st, forecastSeed{symbol: sym, madeAt: fmt.Sprintf("2026-09-%02dT14:40:12Z", 10+k), entry: 1000, term: 1})
		}
	}
	v := e.vendor(prices)
	j := e.run2(`{"source":"forecasts"}`)
	if j.RowsDone != 300 {
		t.Fatalf("%+v", j)
	}
	// 300 rows streamed in 3 batches of 100: never more upstream calls than batches, and far fewer
	// than rows.
	if v.calls < 1 || v.calls > 3 {
		t.Fatalf("upstream batches = %d for 300 rows in 3 read batches", v.calls)
	}
	// A column set without prices sends nothing at all.
	before := v.calls
	e.run2(`{"source":"forecasts","columns":["symbol","entry_price"]}`)
	if v.calls != before {
		t.Fatalf("export without price columns made %d upstream calls", v.calls-before)
	}
}

func TestExportForecastsLatestPerSymbolPicksTheNewestMadeAt(t *testing.T) {
	e := newExportEnv(t)
	st := e.s.st
	seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-10T10:00:00Z", term: 1, version: "old"})
	newest := seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-12T10:00:00Z", term: 2, version: "new"})
	seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-11T10:00:00Z", term: 3, version: "mid"})
	// Equal made_at: the higher id wins.
	seedForecast(t, st, forecastSeed{symbol: "000001", madeAt: "2026-10-12T10:00:00Z", term: 4, version: "tie-a"})
	tieWin := seedForecast(t, st, forecastSeed{symbol: "000001", madeAt: "2026-10-12T10:00:00Z", term: 5, version: "tie-b"})
	f := `{"op":"and","items":[` + yesFilter("latest_per_symbol") + `]}`
	pv, code, body := e.preview(`{"source":"forecasts","columns":["set_id","symbol","version"],"filter":` + f + `}`)
	if code != 200 || pv.Total != 2 || len(pv.Rows) != 2 {
		t.Fatalf("%d %s", code, body)
	}
	ids := map[int64]bool{}
	for _, r := range pv.Rows {
		n, _ := r["set_id"].(float64)
		ids[int64(n)] = true
	}
	if !ids[newest] || !ids[tieWin] {
		t.Fatalf("latest per symbol = %v, want %d and %d", ids, newest, tieWin)
	}
	f = `{"op":"and","items":[{"field":"latest_per_symbol","operator":"is","values":["no"]}]}`
	if pv, _, body = e.preview(`{"source":"forecasts","filter":` + f + `}`); pv.Total != 3 {
		t.Fatalf("latest_per_symbol = no: %s", body)
	}
	// The pair the preset sets, with quotes answering for one symbol only.
	e.vendor(map[string]int64{"600519": 1})
	f = `{"op":"and","items":[` + yesFilter("latest_per_symbol") + `,` + yesFilter("below_entry") + `]}`
	if pv, _, body = e.preview(`{"source":"forecasts","filter":` + f + `}`); pv.Total != 0 {
		t.Fatalf("no entry prices anywhere, so nothing is below: %s", body)
	}
}

func rtypeIs(v string) string { return `{"field":"rtype","operator":"is","values":["` + v + `"]}` }

func TestExportForecastsLatestIsAmongTheSetsMatchingTheRestOfTheFilter(t *testing.T) {
	e := newExportEnv(t)
	st := e.s.st
	// 600519: the newest set overall is another rtype; its newest DECISION set is the 10-12 one.
	seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-10T10:00:00Z", term: 1, rtype: "decision"})
	want := seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-12T10:00:00Z", term: 1, rtype: "decision", entry: 1000})
	seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-20T10:00:00Z", term: 1, rtype: "other"})
	// 000001 has only an "other" set.
	seedForecast(t, st, forecastSeed{symbol: "000001", madeAt: "2026-10-15T10:00:00Z", term: 1, rtype: "other"})
	e.vendor(map[string]int64{"600519": 900})
	f := `{"op":"and","items":[` + rtypeIs("decision") + `,` + yesFilter("latest_per_symbol") + `]}`
	pv, code, body := e.preview(`{"source":"forecasts","columns":["set_id","symbol"],"filter":` + f + `}`)
	if code != 200 || pv.Total != 1 || len(pv.Rows) != 1 || int64(pv.Rows[0]["set_id"].(float64)) != want {
		t.Fatalf("%d %s (want set %d)", code, body, want)
	}
	// With the entry-price screen on top, the newest matching set is the one tested.
	f = `{"op":"and","items":[` + rtypeIs("decision") + `,` + yesFilter("latest_per_symbol") + `,` + yesFilter("below_entry") + `]}`
	if pv, _, body = e.preview(`{"source":"forecasts","columns":["set_id"],"filter":` + f + `}`); pv.Total != 1 || int64(pv.Rows[0]["set_id"].(float64)) != want {
		t.Fatalf("with below_entry: %s", body)
	}
}

func TestExportForecastsLatestInsideOrGroupsAndCountPreviewExportAgree(t *testing.T) {
	e := newExportEnv(t)
	st := e.s.st
	e.s.exports.batchSize = 2
	seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-10T10:00:00Z", term: 1, rtype: "A"})
	a2 := seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-12T10:00:00Z", term: 1, rtype: "A"})
	seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-20T10:00:00Z", term: 1, rtype: "C"})
	b1 := seedForecast(t, st, forecastSeed{symbol: "000001", madeAt: "2026-10-11T10:00:00Z", term: 1, rtype: "B"})
	seedForecast(t, st, forecastSeed{symbol: "300750", madeAt: "2026-10-09T10:00:00Z", term: 1, rtype: "C"})
	// (rtype A AND latest) OR rtype B, nested one level.
	f := `{"op":"or","items":[{"op":"and","items":[` + rtypeIs("A") + `,` + yesFilter("latest_per_symbol") + `]},` + rtypeIs("B") + `]}`
	pv, code, body := e.preview(`{"source":"forecasts","columns":["set_id"],"filter":` + f + `}`)
	if code != 200 || pv.Total != 2 || len(pv.Rows) != 2 {
		t.Fatalf("%d %s", code, body)
	}
	got := map[int64]bool{}
	for _, r := range pv.Rows {
		got[int64(r["set_id"].(float64))] = true
	}
	if !got[a2] || !got[b1] {
		t.Fatalf("or group rows = %v, want %d and %d", got, a2, b1)
	}
	j := e.run2(`{"source":"forecasts","columns":["set_id"],"filter":` + f + `}`)
	if j.RowsEstimate != pv.Total || j.RowsDone != pv.Total {
		t.Fatalf("count %d, preview %d, export estimate %d rows %d", pv.Total, pv.Total, j.RowsEstimate, j.RowsDone)
	}
	recs := parseCSV(t, e.fileOf(j))
	if len(recs)-1 != 2 {
		t.Fatalf("exported %d rows", len(recs)-1)
	}
	// latest = no is the exact complement within the OR (rows failing the whole tree stay out).
	f = `{"op":"and","items":[` + rtypeIs("A") + `,{"field":"latest_per_symbol","operator":"is","values":["no"]}]}`
	if pv, _, body = e.preview(`{"source":"forecasts","filter":` + f + `}`); pv.Total != 1 {
		t.Fatalf("latest=no among rtype A: %s", body)
	}
}

func TestExportForecastsOtherFieldsFilter(t *testing.T) {
	e := newExportEnv(t)
	st := e.s.st
	seedForecast(t, st, forecastSeed{symbol: "600519", madeAt: "2026-10-10T10:00:00Z", term: 1, stance: "long", rtype: "A", version: "ext"})
	seedForecast(t, st, forecastSeed{symbol: "000001", madeAt: "2026-10-11T10:00:00Z", term: 1, stance: "no_position", rtype: "B", version: "int"})
	for filter, want := range map[string]int64{
		`{"field":"stance","operator":"is","values":["no_position"]}`:                       1,
		`{"field":"rtype","operator":"in","values":["A","B"]}`:                              2,
		`{"field":"version","operator":"is","values":["ext"]}`:                              1,
		`{"field":"report_date","operator":"between","values":["2026-10-11","2026-10-12"]}`: 1,
		`{"field":"symbol","operator":"contains","values":["0001"]}`:                        1,
	} {
		pv, code, body := e.preview(`{"source":"forecasts","filter":{"op":"and","items":[` + filter + `]}}`)
		if code != 200 || pv.Total != want {
			t.Errorf("%s -> %d %s (want %d)", filter, code, body, want)
		}
	}
}

func TestExportForecastsCandidateCapIs400(t *testing.T) {
	e := newExportEnv(t)
	e.s.exports.maxCandidates = 5
	prices := map[string]int64{}
	for i := 0; i < 6; i++ {
		sym := fmt.Sprintf("60%04d", i)
		prices[sym] = 900
		seedForecast(t, e.s.st, forecastSeed{symbol: sym, madeAt: "2026-10-10T10:00:00Z", entry: 1000, term: 1})
	}
	v := e.vendor(prices)
	_, code, body := e.preview(`{"source":"forecasts","filter":` + belowFilter() + `}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "more than 5 symbols") {
		t.Fatalf("over the cap = %d %s", code, body)
	}
	if v.calls != 0 {
		t.Errorf("a refused screen must not reach the vendor (%d calls)", v.calls)
	}
	// Creating the export is refused the same way; nothing is queued.
	w := e.do("admin", "POST", "/api/admin/exports", `{"source":"forecasts","filter":`+belowFilter()+`}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create over the cap = %d %s", w.Code, w.Body.String())
	}
	// At the cap it is accepted.
	e.s.exports.maxCandidates = 6
	if pv, code, body := e.preview(`{"source":"forecasts","filter":` + belowFilter() + `}`); code != 200 || pv.Total != 6 {
		t.Fatalf("at the cap = %d %s", code, body)
	}
	if exportBelowEntryMaxSymbols != 2000 {
		t.Errorf("default cap = %d", exportBelowEntryMaxSymbols)
	}
}

func TestExportForecastsOnlyAcceptedSetsAreExported(t *testing.T) {
	e := newExportEnv(t)
	seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-10T10:00:00Z", term: 1})
	if _, err := e.s.st.exec(`UPDATE prediction_sets SET state=?`, predStateRejected); err != nil {
		t.Fatal(err)
	}
	if pv, _, body := e.preview(`{"source":"forecasts"}`); pv.Total != 0 {
		t.Fatalf("rejected set visible: %s", body)
	}
}

// postgresForecastChecks runs the forecast source on the production driver.
func postgresForecastChecks(t *testing.T, e *exportEnv) {
	t.Helper()
	for _, tbl := range []string{"prediction_claims", "prediction_marks", "prediction_sets"} {
		if _, err := e.s.st.exec("DELETE FROM " + tbl); err != nil {
			t.Fatal(err)
		}
	}
	// The fixtures' reports must not leak into the archive checks that follow.
	defer e.s.st.exec(`DELETE FROM reports WHERE title LIKE 'decision %'`)
	seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-10T10:00:00Z", entry: 1000, term: 1100})
	newest := seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-12T10:00:00Z", entry: 1000, term: 1200})
	seedForecast(t, e.s.st, forecastSeed{symbol: "000001", madeAt: "2026-10-12T10:00:00Z", term: 1200}) // no entry
	v := e.vendor(map[string]int64{"600519": 900, "000001": 1})
	f := `{"op":"and","items":[` + yesFilter("latest_per_symbol") + `,` + yesFilter("below_entry") + `]}`
	pv, code, body := e.preview(`{"source":"forecasts","filter":` + f + `}`)
	if code != 200 || pv.Total != 1 || len(pv.Rows) != 1 || int64(pv.Rows[0]["set_id"].(float64)) != newest {
		t.Fatalf("forecast screen on postgres: %d %s", code, body)
	}
	if !strings.Contains(body, `"name":"Name600519"`) || !strings.Contains(body, `"entry_price":10.00`) || !strings.Contains(body, `"vs_entry_pct":-10.00`) {
		t.Fatalf("forecast rendering on postgres: %s", body)
	}
	if v.calls != 1 {
		t.Fatalf("upstream batches = %d", v.calls)
	}
	if j := e.run2(`{"source":"forecasts","filter":` + f + `}`); j.Status != exportDone || j.RowsDone != 1 {
		t.Fatalf("forecast export on postgres: %+v", j)
	}
	// Newest AMONG the matching sets: a newer set of another rtype does not hide the older match.
	seedForecast(t, e.s.st, forecastSeed{symbol: "600519", madeAt: "2026-10-20T10:00:00Z", term: 1, rtype: "other"})
	g := `{"op":"and","items":[` + rtypeIs(predTestRtype) + `,` + yesFilter("latest_per_symbol") + `]}`
	if pv, _, body := e.preview(`{"source":"forecasts","columns":["set_id"],"filter":` + g + `}`); pv.Total != 2 {
		t.Fatalf("latest among matching sets on postgres: %s", body)
	}
	if pv, _, body := e.preview(`{"source":"forecasts","filter":{"op":"and","items":[{"field":"stance","operator":"is","values":["long"]},{"field":"report_date","operator":"after","values":["2026-10-10"]}]}}`); pv.Total != 3 {
		t.Fatalf("stance/date on postgres: %s", body)
	}
}
