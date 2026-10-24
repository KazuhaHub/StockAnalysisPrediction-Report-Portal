package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func probeCompareFixture(t *testing.T) string {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("raw fixture")) }))
	defer stub.Close()
	var registry []quoteSource
	for _, name := range []string{quoteSourceTencent, quoteSourceYahoo} {
		registry = append(registry, quoteSource{name: name, fetchRange: func(ctx context.Context, market, code, _, _ string) (*QuoteResp, error) {
			if _, err := vendorGet(ctx, stub.URL, "", 1024); err != nil {
				return nil, err
			}
			bars := []QuoteBar{shadowTestBar("2026-10-19", 1000, 1100, 900, 1050), shadowTestBar("2026-10-20", 1050, 1200, 1000, 1150)}
			if name == quoteSourceYahoo {
				bars[1].Close++
			}
			return &QuoteResp{Market: market, Symbol: code, Source: name, BarsSource: name, Bars: bars}, nil
		}})
	}
	dir := filepath.Join(t.TempDir(), "capture")
	_, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{quoteSourceTencent, quoteSourceYahoo}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}}}, registry, stub.Client())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func rewriteProbeManifest(t *testing.T, dir string, change func(*quoteProbeManifest)) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest quoteProbeManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	change(&manifest)
	body, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// Test corruption intentionally replaces evidence; production never overwrites captures.
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestQuoteProbeCompareSharesComparatorOffline(t *testing.T) {
	dir := probeCompareFixture(t) // Server closes before replay; no network client is passed.
	report, err := compareQuoteProbe(dir, quoteSourceTencent, [2]string{"2026-10-19", "2026-10-20"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Status != "mismatch" {
		t.Fatalf("report = %+v", report)
	}
	primary := []QuoteBar{shadowTestBar("2026-10-19", 1000, 1100, 900, 1050), shadowTestBar("2026-10-20", 1050, 1200, 1000, 1150)}
	candidate := append([]QuoteBar{}, primary...)
	candidate[1].Close++
	want, err := compareQuoteShadow(primary, candidate, [2]string{"2026-10-19", "2026-10-20"})
	if err != nil || !reflect.DeepEqual(*report.Results[0].Comparison, want) {
		t.Fatalf("shared comparison differs: %+v %v", report, err)
	}
	again, err := compareQuoteProbe(dir, quoteSourceTencent, [2]string{"2026-10-19", "2026-10-20"})
	if err != nil || !reflect.DeepEqual(report, again) {
		t.Fatal("replay is not deterministic")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 5 {
		t.Fatal("replay wrote files")
	}
}

func TestQuoteProbeCompareRejectsInvalidEvidence(t *testing.T) {
	for _, kind := range []string{"raw_hash", "parsed_hash", "traversal", "symlink", "incomplete", "duplicate", "missing_source", "identity", "invalid_bar", "adjusted", "cached", "bar_source", "source", "no_http", "version", "anchors", "manifest_trailing", "manifest_unknown", "raw_size"} {
		t.Run(kind, func(t *testing.T) {
			dir := probeCompareFixture(t)
			anchors := [2]string{"2026-10-19", "2026-10-20"}
			rewriteProbeManifest(t, dir, func(m *quoteProbeManifest) {
				switch kind {
				case "manifest_trailing", "manifest_unknown":
					// Appended after the normal helper serializes the manifest below.
				case "raw_size":
					m.Records[0].HTTP[0].Bytes = quoteBodyLimit + 1
				case "raw_hash":
					m.Records[0].HTTP[0].SHA256 = "bad"
				case "parsed_hash":
					m.Records[0].ParsedSHA256 = "bad"
				case "traversal":
					m.Records[0].ParsedFile = "../secret"
				case "symlink":
					file := filepath.Join(dir, m.Records[0].ParsedFile)
					if err := os.Rename(file, file+".real"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(file+".real", file); err != nil {
						t.Fatal(err)
					}
				case "incomplete":
					m.Complete = false
				case "duplicate":
					m.Records = append(m.Records, m.Records[0])
				case "missing_source":
					m.Records = m.Records[:1]
				case "no_http":
					m.Records[0].HTTP = nil
				case "version":
					m.Version++
				case "anchors":
					anchors[1] = "2026-10-21"
				case "identity", "invalid_bar", "adjusted", "cached", "bar_source", "source":
					file := filepath.Join(dir, m.Records[0].ParsedFile)
					body, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					var response QuoteResp
					if err := json.Unmarshal(body, &response); err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "identity":
						response.Symbol = "600519"
					case "invalid_bar":
						response.Bars[0].Close = 0
					case "adjusted":
						response.Adjusted = true
					case "cached":
						response.Cached = true
					case "bar_source":
						response.BarsSource = quoteSourceYahoo
					case "source":
						response.Source = quoteSourceYahoo
					}
					body, err = json.Marshal(response)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, body, 0600); err != nil {
						t.Fatal(err)
					}
					m.Records[0].ParsedSHA256 = quoteProbeSHA256(body)
				}
			})
			if kind == "manifest_trailing" || kind == "manifest_unknown" {
				file := filepath.Join(dir, "manifest.json")
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "manifest_trailing" {
					body = append(body, []byte(" {}")...)
				} else {
					body = append([]byte(`{"unexpected":1,`), body[1:]...)
				}
				if err := os.WriteFile(file, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := compareQuoteProbe(dir, quoteSourceTencent, anchors); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestQuoteProbeCompareFailedCaptureIsUnavailableNotAgreement(t *testing.T) {
	for _, kind := range []string{"source", "http", "truncated", "empty_window", "primary", "missing_primary_anchor"} {
		t.Run(kind, func(t *testing.T) {
			dir := probeCompareFixture(t)
			rewriteProbeManifest(t, dir, func(m *quoteProbeManifest) {
				switch kind {
				case "primary":
					m.Records[0].Error = "primary failed"
				case "missing_primary_anchor":
					m.To = "2026-10-21"
				case "source":
					m.Records[1].Error = "history unavailable"
				case "http":
					m.Records[1].HTTP[0].Error = "partial read"
				case "truncated":
					m.Records[1].HTTP[0].Truncated = true
				case "empty_window":
					m.From, m.To = "2026-10-21", "2026-10-22"
				}
			})
			anchors := [2]string{"2026-10-19", "2026-10-20"}
			if kind == "empty_window" {
				anchors = [2]string{"2026-10-21", "2026-10-22"}
			}
			if kind == "missing_primary_anchor" {
				anchors[1] = "2026-10-21"
			}
			report, err := compareQuoteProbe(dir, quoteSourceTencent, anchors)
			if err != nil || len(report.Results) != 1 || report.Results[0].Status != "unavailable" || report.Results[0].Comparison != nil {
				t.Fatalf("failed evidence looks valid: %+v %v", report, err)
			}
		})
	}
}

func TestQuoteProbeCompareTrimsCountSeriesBeforeSharedComparison(t *testing.T) {
	dir := probeCompareFixture(t)
	rewriteProbeManifest(t, dir, func(m *quoteProbeManifest) {
		m.From, m.To = "2026-10-19", "2026-10-19"
		m.Records[1].FetchMode = "count" // The only mismatch is outside the requested window.
	})
	report, err := compareQuoteProbe(dir, quoteSourceTencent, [2]string{"2026-10-19", "2026-10-19"})
	if err != nil || len(report.Results) != 1 || report.Results[0].Status != "match" || report.Results[0].Comparison.ComparedDates != 1 {
		t.Fatalf("count window not isolated: %+v %v", report, err)
	}
}

func TestQuoteProbeEvidenceReaderBoundsBeforeReading(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "body")
	if err := os.WriteFile(file, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	reader := quoteProbeEvidenceReader{root: root, remaining: 4}
	if _, err := reader.read("body", 10); err == nil {
		t.Fatal("aggregate limit ignored")
	}
	reader.remaining = 10
	if _, err := reader.read("body", 4); err == nil {
		t.Fatal("file size limit ignored")
	}
	if body, err := reader.read("body", 5); err != nil || string(body) != "12345" || reader.remaining != 5 {
		t.Fatal("bounded positive control failed")
	}
}
