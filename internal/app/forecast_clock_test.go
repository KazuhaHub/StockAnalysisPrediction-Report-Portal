package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

func TestForecastDemoClockValidation(t *testing.T) {
	for _, tc := range []struct {
		name, build, raw string
		present, allowed bool
	}{
		{"unset_release", "v2026.42", "", false, true},
		{"unset_dev", "dev", "", false, true},
		{"dev_instant", "dev", "2027-06-20T15:00:00+08:00", true, true},
		{"release_instant", "v2026.42", "2027-06-20T07:00:00Z", true, false},
		{"ci_is_not_dev", "ci", "2027-06-20T07:00:00Z", true, false},
		{"empty_release", "v2026.42", "", true, false},
		{"empty_dev", "dev", "", true, false},
		{"bad_dev", "dev", "tomorrow", true, false},
		{"date_only", "dev", "2027-06-20", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseForecastDemoClock(tc.raw, tc.present, tc.build)
			if (err == nil) != tc.allowed {
				t.Fatalf("parse = %s / %v, allowed=%v", got, err, tc.allowed)
			}
			if err == nil && tc.present && got.Format(time.RFC3339) != "2027-06-20T07:00:00Z" {
				t.Fatalf("clock not normalized to UTC: %s", got)
			}
			if err == nil && !tc.present && !got.IsZero() {
				t.Fatal("unset clock is not real time")
			}
		})
	}
}

func TestForecastDemoReadClockDoesNotDriveReceipt(t *testing.T) {
	previous := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = previous })
	t.Setenv("RP_FORECAST_DEMO_NOW", "2027-06-20T07:00:00Z")
	s := newV1Server(t)
	if got := s.forecastNow().Format(time.RFC3339); got != "2027-06-20T07:00:00Z" {
		t.Fatalf("read clock = %s", got)
	}
	before := time.Now().UTC().Truncate(time.Second)
	raw := predWire(func(m map[string]any) { m["horizon_end"] = time.Now().AddDate(1, 0, 0).Format("2006-01-02") })
	code, response := ingestPost(t, s, ingestBody(string(raw)))
	if code != 200 || response["predictions_status"] != predStateAccepted {
		t.Fatalf("real receipt ingest status = %d / %v", code, response)
	}
	var made string
	if err := s.st.queryRow(`SELECT made_at FROM prediction_sets ORDER BY id DESC LIMIT 1`).Scan(&made); err != nil {
		t.Fatal(err)
	}
	received, err := time.Parse(time.RFC3339, made)
	if err != nil || received.Before(before) || received.After(time.Now().UTC()) {
		t.Fatalf("demo read clock changed trusted receipt: %s / %v", made, err)
	}
	// The seed can control receipt through a per-server test seam, never through the environment.
	seedReceipt := time.Date(2026, 3, 2, 13, 0, 0, 0, time.UTC)
	s.forecastReceiptForTest = func() time.Time { return seedReceipt }
	code, response = ingestPost(t, s, ingestBody(string(predWire(nil))))
	if code != 200 || response["predictions_status"] != predStateAccepted {
		t.Fatalf("seed receipt ingest status = %d / %v", code, response)
	}
	if err := s.st.queryRow(`SELECT made_at FROM prediction_sets ORDER BY id DESC LIMIT 1`).Scan(&made); err != nil {
		t.Fatal(err)
	}
	if made != seedReceipt.Format(time.RFC3339) {
		t.Fatalf("seed receipt = %s", made)
	}
	// Pin the read-route call site as well as the helper: an evaluator clock before S0 must not
	// override the demo read clock, which has already passed these sets' projected starts.
	s.forecastEval.now = func() time.Time { return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) }
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/reports/1/predictions", nil)
	req.SetPathValue("id", itoa(int64(response["id"].(float64))))
	s.serveReportPredictions(rec, req, nil)
	var out struct {
		Sets []PredictionSet `json:"sets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 || len(out.Sets) != 2 {
		t.Fatalf("preview read route = %d %s / %v", rec.Code, rec.Body.String(), err)
	}
	for _, set := range out.Sets {
		if len(set.Marks) == 0 {
			t.Fatal("accepted seed has no read phases")
		}
		for _, mark := range set.Marks {
			if mark.Phase == "" || mark.Phase == "waiting_s0" {
				t.Fatalf("read route ignored demo clock: %+v", mark)
			}
		}
	}
}

// Exercise the real linked executable: refusal must precede config generation and every DB write.
func TestDemoClockRefusedInReleaseBuilds(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "portal-release")
	build := exec.Command("go", "build", "-o", executable, "-ldflags",
		"-X github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version.Version=v2026.42", "./cmd/report-portal")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build release: %v\n%s", err, output)
	}
	for _, raw := range []string{"2027-06-20T07:00:00Z", "", "invalid"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.yaml")
		cmd := exec.CommandContext(ctx, executable)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "RP_CONFIG="+cfg, "RP_FORECAST_DEMO_NOW="+raw)
		output, err := cmd.CombinedOutput()
		cancel()
		if err == nil || !strings.Contains(string(output), "RP_FORECAST_DEMO_NOW") {
			t.Fatalf("release accepted demo clock %q: %v\n%s", raw, err, output)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("release wrote before rejecting demo clock: %v / %v", entries, err)
		}
	}
}
