package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func sohuFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/quote/sohu-" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSohuHistoryProvenanceAndUnits(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		code         string
		last, volume int64
	}{{"600081", 1013, 6976700}, {"000852", 673455, 25267628800}} {
		r, err := parseSohuHistory(sohuFixture(t, "sh"+tc.code), "sh", tc.code, "2026-10-22", "2026-10-23", now)
		if err != nil {
			t.Fatal(err)
		}
		if !r.HistoryOnly || r.HistoryThrough != "2026-10-23" || r.Snapshot.AsOf != "" || r.Snapshot.Session != quoteSessionUnknown || r.Snapshot.Last != tc.last || len(r.Bars) != 2 || r.Bars[0].Date != "2026-10-22" || r.Bars[1].Volume != tc.volume {
			t.Fatalf("historical provenance or units lost: %+v", r)
		}
	}
}

func TestSohuHistoryRejectsDrift(t *testing.T) {
	base := sohuFixture(t, "sh600081")
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ name, old, replacement string }{
		{"namespace", "cn_600081", "zs_600081"},
		{"code", "600081", "600082"},
		{"status", "\"status\":0", "\"status\":1"},
		{"missing status", "\"status\":0,", ""},
		{"ordering", "2026-10-22", "2026-10-23"},
		{"outside window", "2026-10-22", "2026-10-21"},
		{"price", "\"10.16\"", "\"9.50\""},
		{"percent", "3.16%", "31.6%"},
		{"volume", "\"69767\"", "\"-1\""},
		{"amount", "\"6824.26\"", "\"-1\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(string(base), tc.old, tc.replacement, 1)
			if _, err := parseSohuHistory([]byte(body), "sh", "600081", "2026-10-22", "2026-10-23", now); err == nil {
				t.Fatal("accepted malformed history")
			}
		})
	}
	var envelope []sohuHistoryEnvelope
	if err := json.Unmarshal(base, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope[0].HQ[0] = append(envelope[0].HQ[0], "unexpected")
	b, _ := json.Marshal(envelope)
	if _, err := parseSohuHistory(b, "sh", "600081", "2026-10-22", "2026-10-23", now); err == nil {
		t.Fatal("accepted an unmeasured row shape")
	}
}

func TestSohuHistoryExcludesUnstampedCurrentDay(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	r, err := parseSohuHistory(sohuFixture(t, "sh600081"), "sh", "600081", "2026-10-22", "2026-10-23", now)
	if err != nil || r.HistoryThrough != "2026-10-22" || len(r.Bars) != 1 {
		t.Fatalf("current-day bar escaped: %+v %v", r, err)
	}
}

func TestSohuHistoryIsolationAndDefaultOff(t *testing.T) {
	c := &quoteCache{}
	c.init()
	cfg := quoteConfigDefault()
	if strings.Contains(strings.Join(cfg.Order, ","), "sohu") {
		t.Fatal("Sohu enabled by default")
	}
	cfg.Order = append(cfg.Order, "sohu")
	target, _ := quoteResolve("600081")
	for _, iv := range []quoteInterval{quoteIntervalSnapshot, quoteIntervalDaily, quoteIntervalSettled, quoteIntervalSettledShadow} {
		for _, src := range c.sourcesFor(cfg, target, iv) {
			if src.name == "sohu" {
				t.Fatalf("history source on normal path: %s", iv)
			}
		}
	}
	cfg.HistoryOnly = true
	if got := quoteSourceNamesOf(c.sourcesFor(cfg, target, quoteIntervalDaily)); got != "sohu" {
		t.Fatal(got)
	}
	if len(c.sourcesFor(cfg, target, quoteIntervalSettled)) != 0 {
		t.Fatal("history source granted ledger authority")
	}
	for _, symbol := range []string{"sh:000001", "hk:00700", "bj:830799", "sz:399001", "sh:510300"} {
		tg, _ := quoteResolve(symbol)
		if len(c.sourcesFor(cfg, tg, quoteIntervalDaily)) != 0 {
			t.Fatalf("unmeasured symbol admitted: %s", symbol)
		}
	}
	if _, _, _, err := c.fetchUnder(context.Background(), cfg, "sh", "600081", 66, quoteIntervalSettled, quoteWindow{}); !errors.Is(err, errQuoteLedgerInterval) {
		t.Fatal(err)
	}
}
