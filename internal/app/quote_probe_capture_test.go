package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestQuoteProbeCapturePreservesRawBodiesAndPrivateManifest(t *testing.T) {
	raw := []byte{0xff, 0x00, 0x81, 'p', 'r', 'o', 'b', 'e'}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer stub.Close()
	dir := filepath.Join(t.TempDir(), "capture")
	registry := []quoteSource{{name: quoteSourceTencent, fetchRange: func(ctx context.Context, _, _, _, _ string) (*QuoteResp, error) {
		if _, err := vendorGet(ctx, stub.URL, "", 1024); err != nil {
			return nil, err
		}
		return &QuoteResp{Source: quoteSourceTencent, Bars: []QuoteBar{shadowTestBar("2026-10-19", 1000, 1100, 900, 1050)}}, nil
	}}}
	manifest, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{
		Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{quoteSourceTencent}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}},
	}, registry, stub.Client())
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Complete || len(manifest.Records) != 1 || len(manifest.Records[0].HTTP) != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
	request := manifest.Records[0].HTTP[0]
	got, err := os.ReadFile(filepath.Join(dir, request.File))
	wantHash := sha256.Sum256(raw)
	if err != nil || string(got) != string(raw) || request.Bytes != int64(len(raw)) || request.SHA256 != hex.EncodeToString(wantHash[:]) || request.Status != 200 {
		t.Fatalf("raw evidence changed: %+v / %v", request, err)
	}
	for _, name := range []string{"manifest.json", request.File, manifest.Records[0].ParsedFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private evidence %s: %v / %v", name, info, err)
		}
	}
	var saved quoteProbeManifest
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || json.Unmarshal(body, &saved) != nil || !saved.Complete {
		t.Fatalf("manifest not persisted: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("private directory: %v %v", info, err)
	}
}

func TestQuoteProbeCaptureRejectsBadInputBeforeOutputOrNetwork(t *testing.T) {
	for _, invalid := range []string{"source", "window", "target"} {
		t.Run(invalid, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "capture")
			calls := 0
			registry := []quoteSource{{name: quoteSourceTencent, fetchRange: func(context.Context, string, string, string, string) (*QuoteResp, error) { calls++; return nil, nil }}}
			options := quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{quoteSourceTencent}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}}}
			switch invalid {
			case "source":
				options.Sources[0] = "unknown"
			case "window":
				options.From = "bad"
			case "target":
				options.Targets[0].Code = "../../secret"
			}
			if _, err := captureQuoteProbe(context.Background(), options, registry, nil); err == nil {
				t.Fatal("invalid input accepted")
			}
			if calls != 0 {
				t.Fatal("invalid input reached source")
			}
			if _, err := os.Lstat(dir); !os.IsNotExist(err) {
				t.Fatal("invalid input created output")
			}
		})
	}
}

func TestQuoteProbeCaptureNeverOverwritesExistingDirectoryOrSymlink(t *testing.T) {
	parent := t.TempDir()
	for _, symlink := range []bool{false, true} {
		dir := filepath.Join(parent, "capture")
		if symlink {
			dir = filepath.Join(parent, "link")
			if err := os.Symlink(parent, dir); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{quoteSourceYahoo}, Targets: []quoteProbeTarget{{Market: "sh", Code: "600519"}}}, defaultQuoteSources(), nil); err == nil {
			t.Fatal("existing path accepted")
		}
	}
}

func TestQuoteProbeCaptureAdmitsDisplayOnlyCandidateWithoutGrantingSettlement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capture")
	calls := 0
	source := quoteSource{name: quoteSourceYahoo, fetchAt: func(_ context.Context, market, code string, bars int, interval quoteInterval) (*QuoteResp, error) {
		calls++
		if market != "sh" || code != "600519" || bars != quoteMaxBars || interval != quoteIntervalDaily {
			t.Fatal("wrong candidate request")
		}
		return &QuoteResp{Source: quoteSourceYahoo, Bars: []QuoteBar{shadowTestBar("2026-10-19", 1000, 1100, 900, 1050)}}, nil
	}}
	manifest, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{quoteSourceYahoo}, Targets: []quoteProbeTarget{{Market: "sh", Code: "600519"}}}, []quoteSource{source}, nil)
	if err != nil || calls != 1 || manifest.Records[0].FetchMode != "count" {
		t.Fatalf("candidate refused: %+v %v", manifest, err)
	}
	if source.cashDividends || source.reportsKind || len(source.caps) != 0 {
		t.Fatal("measurement granted a capability")
	}
}

func TestQuoteProbeCaptureKeepsHTTPFailuresAndBoundsRawBodies(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		body := []byte("service unavailable")
		status := http.StatusServiceUnavailable
		if oversized {
			body = bytes.Repeat([]byte("x"), quoteBodyLimit+1)
			status = http.StatusOK
		}
		stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status); _, _ = w.Write(body) }))
		dir := filepath.Join(t.TempDir(), "capture")
		source := quoteSource{name: quoteSourceTencent, fetchRange: func(ctx context.Context, _, _, _, _ string) (*QuoteResp, error) {
			_, err := vendorGet(ctx, stub.URL, "", quoteBodyLimit)
			return nil, err
		}}
		manifest, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{source.name}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}}}, []quoteSource{source}, stub.Client())
		stub.Close()
		if err != nil || !manifest.Complete || len(manifest.Records) != 1 {
			t.Fatalf("failed response lost: %+v %v", manifest, err)
		}
		record := manifest.Records[0]
		if record.Error == "" || len(record.HTTP) != 1 {
			t.Fatalf("failure presented as data: %+v", record)
		}
		request := record.HTTP[0]
		if request.Status != status || request.Truncated != oversized || request.CapturedAt == "" {
			t.Fatalf("HTTP metadata missing: %+v", request)
		}
		raw, err := os.ReadFile(filepath.Join(dir, request.File))
		if err != nil || len(raw) != min(len(body), quoteBodyLimit) || quoteProbeSHA256(raw) != request.SHA256 {
			t.Fatalf("bounded evidence lost: %+v %v", request, err)
		}
	}
}

func TestQuoteProbeCaptureCancellationKeepsAnIncompleteManifest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := filepath.Join(t.TempDir(), "capture")
	calls := 0
	source := quoteSource{name: quoteSourceTencent, fetchRange: func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls++
		cancel()
		return nil, context.Canceled
	}}
	manifest, err := captureQuoteProbe(ctx, quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{source.name}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}, {Market: "sh", Code: "600519"}}}, []quoteSource{source}, nil)
	if !errors.Is(err, context.Canceled) || manifest.Complete || calls != 1 {
		t.Fatalf("cancellation lost: %+v %v calls=%d", manifest, err, calls)
	}
	var saved quoteProbeManifest
	body, readErr := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if readErr != nil || json.Unmarshal(body, &saved) != nil || saved.Complete || len(saved.Records) != 1 {
		t.Fatalf("partial manifest missing: %s %v", body, readErr)
	}
}

func TestQuoteProbeCaptureLocalWriteFailureIsNotHiddenAsVendorFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capture")
	source := quoteSource{name: quoteSourceTencent, fetchRange: func(context.Context, string, string, string, string) (*QuoteResp, error) {
		if err := os.WriteFile(filepath.Join(dir, "0001.parsed.json"), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		return &QuoteResp{Source: quoteSourceTencent}, nil
	}}
	manifest, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{source.name}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}}}, []quoteSource{source}, nil)
	if err == nil || manifest.Complete {
		t.Fatalf("local failure hidden: %+v %v", manifest, err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "0001.parsed.json"))
	if string(got) != "keep" {
		t.Fatal("existing evidence overwritten")
	}
}

func TestQuoteProbeCaptureDoesNotReplaceTheGlobalVendorClient(t *testing.T) {
	original := vendorClient
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("raw")) }))
	defer stub.Close()
	source := quoteSource{name: quoteSourceTencent, fetchRange: func(ctx context.Context, _, _, _, _ string) (*QuoteResp, error) {
		if vendorHTTPClient(context.Background()) != original || vendorHTTPClient(ctx) == original {
			t.Fatal("capture client escaped its context")
		}
		_, err := vendorGet(ctx, stub.URL, "", quoteBodyLimit)
		return nil, err
	}}
	_, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{Directory: filepath.Join(t.TempDir(), "capture"), From: "2026-10-19", To: "2026-10-20", Sources: []string{source.name}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}}}, []quoteSource{source}, stub.Client())
	if err != nil || vendorClient != original {
		t.Fatalf("global client changed: %v", err)
	}
}

type quoteProbeReadFailure struct{}

func (quoteProbeReadFailure) Read([]byte) (int, error) { return 0, errors.New("body interrupted") }

func TestQuoteProbeCapturePreservesPartialReads(t *testing.T) {
	client := &http.Client{Transport: forecastVendorTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(io.MultiReader(bytes.NewReader([]byte("partial")), quoteProbeReadFailure{}))}, nil
	})}
	dir := filepath.Join(t.TempDir(), "capture")
	source := quoteSource{name: quoteSourceTencent, fetchRange: func(ctx context.Context, _, _, _, _ string) (*QuoteResp, error) {
		_, err := vendorGet(ctx, "https://example.test/probe", "", quoteBodyLimit)
		return nil, err
	}}
	manifest, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{source.name}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}}}, []quoteSource{source}, client)
	if err != nil || !manifest.Complete || len(manifest.Records) != 1 || len(manifest.Records[0].HTTP) != 1 {
		t.Fatalf("partial read lost: %+v %v", manifest, err)
	}
	request := manifest.Records[0].HTTP[0]
	if request.Error != "body interrupted" || manifest.Records[0].Error == "" {
		t.Fatalf("partial body presented as complete: %+v", request)
	}
	raw, err := os.ReadFile(filepath.Join(dir, request.File))
	if err != nil || string(raw) != "partial" {
		t.Fatalf("partial bytes lost: %q %v", raw, err)
	}
}

func TestQuoteProbeCapturePinsItsDirectoryDuringFetch(t *testing.T) {
	parent := t.TempDir()
	dir, moved, foreign := filepath.Join(parent, "capture"), filepath.Join(parent, "moved"), filepath.Join(parent, "foreign")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("raw")) }))
	defer stub.Close()
	source := quoteSource{name: quoteSourceTencent, fetchRange: func(ctx context.Context, _, _, _, _ string) (*QuoteResp, error) {
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(foreign, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := vendorGet(ctx, stub.URL, "", quoteBodyLimit); err != nil {
			return nil, err
		}
		return &QuoteResp{Source: quoteSourceTencent}, nil
	}}
	manifest, err := captureQuoteProbe(context.Background(), quoteProbeCaptureOptions{Directory: dir, From: "2026-10-19", To: "2026-10-20", Sources: []string{source.name}, Targets: []quoteProbeTarget{{Market: "sh", Code: "601899"}}}, []quoteSource{source}, stub.Client())
	if err == nil || manifest.Complete {
		t.Fatalf("replaced output reported complete: %+v %v", manifest, err)
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatalf("capture wrote through replacement directory: %v %v", entries, err)
	}
	for _, name := range []string{"manifest.json", "0001-001.raw", "0001.parsed.json"} {
		if _, err := os.Stat(filepath.Join(moved, name)); err != nil {
			t.Fatalf("evidence not retained in original directory: %s %v", name, err)
		}
	}
}
