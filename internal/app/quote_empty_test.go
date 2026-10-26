package app

import (
	"errors"
	"net/http"
	"testing"
)

func TestQuoteEmptyPayloadIsNotAnOutage(t *testing.T) {
	_, err := parseTencentQuote("sz", "000000", []byte(`{"code":0,"data":{"sz000000":{"day":[],"qt":{"sz000000":[]}}}}`), 60)
	if !errors.Is(err, errQuoteNoData) {
		t.Fatalf("empty snapshot: %v", err)
	}
	_, err = parseSinaSnapshot("sz", "000000", `var hq_str_sz000000="";`)
	if !errors.Is(err, errQuoteNoData) {
		t.Fatalf("empty snapshot: %v", err)
	}
	_, err = parseSinaSnapshot("sz", "000000", `var hq_str_sz000001="";`)
	if errors.Is(err, errQuoteNoData) {
		t.Fatal("wrong symbol was treated as no data")
	}
	_, err = parseTencentQuote("sz", "000000", []byte(`{"code":0,"data":{"sz000000":{"day":[],"qt":{}}}}`), 60)
	if errors.Is(err, errQuoteNoData) {
		t.Fatal("missing snapshot was treated as valid empty data")
	}
}
func TestQuoteAPISeparatesEmptyDataFromSourceFailure(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		s := quoteServer(t)
		empty := &quoteVendorStub{parse: func(string, string, int) (*QuoteResp, error) { return nil, errQuoteNoData }}
		fallback := empty
		if mixed {
			fallback = failingStub("offline")
		}
		wireQuoteSources(s, empty, fallback)
		rec := quoteGET(t, s, "/api/quote/601899")
		code, status := "quote_no_data", http.StatusNotFound
		if mixed {
			code, status = "quote_unavailable", http.StatusServiceUnavailable
		}
		for _, health := range s.QuoteHealth() {
			if !mixed && health.Failures != 0 {
				t.Fatalf("empty result counted as outage: %+v", health)
			}
		}
		if rec.Code != status || quoteErrCode(t, rec) != code {
			t.Fatalf("mixed=%v status=%d body=%s", mixed, rec.Code, rec.Body.String())
		}
	}
}
