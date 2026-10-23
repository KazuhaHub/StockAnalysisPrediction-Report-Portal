package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestQuoteProbeCommandCaptureAndRecordedFixedPanelNeverOpenDatabase(t *testing.T) {
	parent := t.TempDir()
	config := filepath.Join(parent, "config.yaml")
	database := filepath.Join(parent, "portal.db")
	if err := os.WriteFile(database, []byte("database sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("db:\n  driver: sqlite\n  dsn: "+database+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RP_CONFIG", config)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("test raw body")) }))
	defer stub.Close()
	calls := 0
	var registry []quoteSource
	for _, name := range []string{quoteSourceTencent, quoteSourceYahoo} {
		registry = append(registry, quoteSource{name: name, fetchRange: func(ctx context.Context, market, code, _, _ string) (*QuoteResp, error) {
			calls++
			if _, err := vendorGet(ctx, stub.URL, "", 1024); err != nil {
				return nil, err
			}
			return &QuoteResp{Market: market, Symbol: code, Source: name, BarsSource: name, Bars: []QuoteBar{shadowTestBar("2026-10-19", 1000, 1100, 900, 1050), shadowTestBar("2026-10-20", 1050, 1200, 1000, 1150)}}, nil
		}})
	}
	dir := filepath.Join(parent, "capture")
	var output bytes.Buffer
	err := quoteProbeCommand(context.Background(), []string{"capture", "--out", dir, "--from", "2026-10-19", "--to", "2026-10-20", "--sources", "tencent,yahoo", "--suspended", "sz:000004"}, &output, registry, stub.Client())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 48 {
		t.Fatalf("panel calls = %d, want 2*(3+20+1)", calls)
	}
	output.Reset()
	err = quoteProbeCommand(context.Background(), []string{"compare", "--in", dir, "--primary", "tencent", "--anchor-start", "2026-10-19", "--anchor-end", "2026-10-20", "--record"}, &output, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var report quoteProbeComparisonReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 24 {
		t.Fatalf("panel report = %+v", report)
	}
	for _, result := range report.Results {
		if result.Status != "match" {
			t.Fatalf("agreement lost: %+v", result)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "comparison.json")); err != nil {
		t.Fatal(err)
	}
	if err := quoteProbeCommand(context.Background(), []string{"compare", "--in", dir, "--primary", "tencent", "--anchor-start", "2026-10-19", "--anchor-end", "2026-10-20", "--record"}, &output, nil, nil); err == nil {
		t.Fatal("record overwrote existing output")
	}
	recorded := filepath.Join(dir, "comparison.json")
	if err := os.Rename(recorded, recorded+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(recorded+".saved", recorded); err != nil {
		t.Fatal(err)
	}
	if err := quoteProbeCommand(context.Background(), []string{"compare", "--in", dir, "--anchor-start", "2026-10-19", "--anchor-end", "2026-10-20", "--record"}, &output, nil, nil); err == nil {
		t.Fatal("record followed output symlink")
	}
	saved, err := os.ReadFile(recorded + ".saved")
	if err != nil {
		t.Fatal(err)
	}
	var savedReport quoteProbeComparisonReport
	if err := json.Unmarshal(saved, &savedReport); err != nil || !reflect.DeepEqual(report, savedReport) {
		t.Fatal("record changed symlink target")
	}
	if calls != 48 {
		t.Fatal("replay reached network")
	}
	body, err := os.ReadFile(database)
	if err != nil || string(body) != "database sentinel" {
		t.Fatalf("database changed: %q %v", body, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 3 {
		t.Fatal("unexpected database sidecars or output")
	}
}

func TestQuoteProbeRecordRejectsManifestReplacement(t *testing.T) {
	dir := probeCompareFixture(t)
	report, err := compareQuoteProbe(dir, quoteSourceTencent, [2]string{"2026-10-19", "2026-10-20"})
	if err != nil {
		t.Fatal(err)
	}
	rewriteProbeManifest(t, dir, func(m *quoteProbeManifest) { m.CreatedAt = "2026-10-22T00:00:00Z" })
	if err := recordQuoteProbeReport(dir, report); err == nil {
		t.Fatal("record accepted a replaced manifest")
	}
	if _, err := os.Stat(filepath.Join(dir, "comparison.json")); !os.IsNotExist(err) {
		t.Fatal("changed capture wrote report")
	}
}

type quoteProbeFailWriter struct{}

func (quoteProbeFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestQuoteProbeCommandPropagatesOutputFailure(t *testing.T) {
	dir := probeCompareFixture(t)
	err := QuoteProbeCommand(context.Background(), []string{"compare", "--in", dir, "--anchor-start", "2026-10-19", "--anchor-end", "2026-10-20"}, quoteProbeFailWriter{})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure swallowed: %v", err)
	}
}

func TestQuoteProbeCommandRejectsBadFlagsAndPartialPanelRecord(t *testing.T) {
	for _, args := range [][]string{nil, {"bad"}, {"capture", "--wat"}, {"capture", "extra"}, {"capture", "--suspended", "600519"}, {"compare", "--in", "missing"}} {
		if err := QuoteProbeCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	dir := probeCompareFixture(t)
	err := QuoteProbeCommand(context.Background(), []string{"compare", "--in", dir, "--primary", "tencent", "--anchor-start", "2026-10-19", "--anchor-end", "2026-10-20", "--record"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("partial panel recorded as full evidence")
	}
	if _, err := os.Stat(filepath.Join(dir, "comparison.json")); !os.IsNotExist(err) {
		t.Fatal("partial panel wrote report")
	}
}

func TestQuoteProbeFixedPanelHasRequiredBoardsWithoutDuplicates(t *testing.T) {
	panel := quoteProbeFixedPanel()
	if len(panel) != 23 {
		t.Fatalf("panel length %d", len(panel))
	}
	if !reflect.DeepEqual(panel[:3], []quoteProbeTarget{{Market: "sh", Code: "000852"}, {Market: "sh", Code: "000906"}, {Market: "sh", Code: "000300"}}) {
		t.Fatal("benchmark panel changed")
	}
	seen := map[quoteProbeTarget]bool{}
	have301, have689, haveBJ := false, false, false
	for _, target := range panel {
		if seen[target] {
			t.Fatal("duplicate fixed panel target")
		}
		seen[target] = true
		if target.Market == "sz" && target.Code[:3] == "301" {
			have301 = true
		}
		if target.Market == "sh" && target.Code[:3] == "689" {
			have689 = true
		}
		if target.Market == "bj" {
			haveBJ = true
		}
	}
	if !have301 || !have689 || !haveBJ {
		t.Fatal("required board missing")
	}
	panel[0].Code = "changed"
	if quoteProbeFixedPanel()[0].Code != "000852" {
		t.Fatal("panel exposed mutable global")
	}
}
