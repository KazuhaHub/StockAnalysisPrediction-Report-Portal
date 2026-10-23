package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func shadowStateFixture(at time.Time) forecastShadowRun {
	return forecastShadowRun{At: at.UTC().Format(time.RFC3339), Reads: 1, HTTPAttempts: 2, Results: []forecastShadowResult{{
		At: at.UTC().Format(time.RFC3339), Target: quoteProbeTarget{Market: "sh", Code: "601899"}, Primary: quoteSourceTencent, Candidate: quoteSourceSina,
		Anchors: [2]string{"2026-10-19", "2026-10-20"}, Status: "match", Comparison: &quoteShadowComparison{Match: true, ComparedDates: 2, Differences: []quoteShadowDifference{}},
	}}}
}

func TestForecastShadowDiagnosticsSurviveRestartAndRetain14Days(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 1, 16, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		run := shadowStateFixture(at.AddDate(0, 0, i))
		if err := st.saveForecastDiagnostics(context.Background(), run, []forecastCallObservation{{Source: quoteSourceTencent, Calls: 1}, {Source: quoteSourceSina, Calls: 1, ShadowHTTPAttempts: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	shadow, calls, err := st.forecastDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if len(shadow.Days) != 14 || len(calls.Days) != 14 || shadow.Days[0].Date != "2026-10-07" || shadow.Days[13].Matches != 1 || calls.Days[13].Sources[quoteSourceSina].ShadowHTTPAttempts != 2 || len(shadow.Recent) != 14 {
		t.Fatalf("retention/restart lost diagnostics: %+v %+v", shadow, calls)
	}
}

func TestForecastShadowDiagnosticsTransactionAndNoLostCounts(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { assertShadowDiagnosticCounts(t, newTestStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t)
		for _, key := range []string{setForecastStateShadow, setForecastStateCalls} {
			if err := st.DeleteSetting(key); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			for _, key := range []string{setForecastStateShadow, setForecastStateCalls} {
				if err := st.DeleteSetting(key); err != nil {
					t.Error(err)
				}
			}
		})
		assertShadowDiagnosticCounts(t, st)
	})
}

func TestForecastShadowDiagnosticsSecondWriteFailureRollsBackPair(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.exec(`CREATE TRIGGER fail_shadow_update BEFORE UPDATE ON meta WHEN NEW.k='forecast_state_shadow' BEGIN SELECT RAISE(ABORT,'diagnostic write fault'); END`); err != nil {
		t.Fatal(err)
	}
	run := shadowStateFixture(time.Date(2026, time.October, 22, 16, 0, 0, 0, time.UTC))
	if err := st.saveForecastDiagnostics(context.Background(), run, []forecastCallObservation{{Source: quoteSourceSina, Calls: 1}}); err == nil {
		t.Fatal("injected second write succeeded")
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM meta WHERE k IN ('forecast_state_calls','forecast_state_shadow')`); n != 0 {
		t.Fatalf("second failure left a partial diagnostic pair: %d", n)
	}
}

func TestForecastShadowDiagnosticsCancelledReadDoesNotInventEmptyHistory(t *testing.T) {
	st := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := st.forecastDiagnosticsContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read became empty history: %v", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM meta WHERE k IN ('forecast_state_calls','forecast_state_shadow')`); n != 0 {
		t.Fatal("diagnostic read created state")
	}
}

func TestForecastShadowDiagnosticsAcrossSQLiteConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.db")
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
	assertShadowDiagnosticCounts(t, first, second)
}

func assertShadowDiagnosticCounts(t *testing.T, st *Store, peers ...*Store) {
	t.Helper()
	stores := append([]*Store{st}, peers...)
	run := shadowStateFixture(time.Date(2026, time.October, 22, 16, 0, 0, 0, time.UTC))
	var group sync.WaitGroup
	for i := range 24 {
		group.Go(func() {
			individual := run
			individual.At = time.Date(2026, time.October, 22, 16, i, 0, 0, time.UTC).Format(time.RFC3339)
			if err := stores[i%len(stores)].saveForecastDiagnostics(context.Background(), individual, []forecastCallObservation{{Source: quoteSourceSina, Calls: 1, ShadowHTTPAttempts: 2}}); err != nil {
				t.Error(err)
			}
		})
	}
	group.Go(func() {
		for range 30 {
			if _, _, err := st.forecastDiagnostics(); err != nil {
				t.Error(err)
			}
		}
	})
	group.Wait()
	shadow, calls, err := st.forecastDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if len(shadow.Days) != 1 || shadow.Days[0].Matches != 24 || shadow.Days[0].Reads != 24 || len(shadow.Recent) != 20 || len(calls.Days) != 1 || calls.Days[0].Sources[quoteSourceSina].Calls != 24 || calls.Days[0].Sources[quoteSourceSina].ShadowHTTPAttempts != 48 {
		t.Fatalf("concurrent diagnostics lost counts: %+v %+v", shadow, calls)
	}
}

func TestForecastShadowDiagnosticsRefusesCorruptionWithoutOverwrite(t *testing.T) {
	for _, raw := range []string{"broken", `{"version":999}`, "null", `{}`, `{"version":1}`} {
		st := newTestStore(t)
		if err := st.SetSetting(setForecastStateCalls, raw); err != nil {
			t.Fatal(err)
		}
		run := shadowStateFixture(time.Date(2026, time.October, 22, 16, 0, 0, 0, time.UTC))
		if err := st.saveForecastDiagnostics(context.Background(), run, nil); err == nil {
			t.Fatal("damaged history silently reset")
		}
		if st.GetSetting(setForecastStateCalls, "") != raw || st.GetSetting(setForecastStateShadow, "") != "" {
			t.Fatal("failed transaction overwrote diagnostic evidence")
		}
	}
}

func TestForecastShadowDiagnosticsEmptyOrNullPairIsNotReset(t *testing.T) {
	for _, sqlNull := range []bool{false, true} {
		st := newTestStore(t)
		for _, key := range []string{setForecastStateCalls, setForecastStateShadow} {
			if err := st.SetSetting(key, ""); err != nil {
				t.Fatal(err)
			}
		}
		if sqlNull {
			if _, err := st.exec(`UPDATE meta SET v=NULL WHERE k IN (?,?)`, setForecastStateCalls, setForecastStateShadow); err != nil {
				t.Fatal(err)
			}
		}
		run := shadowStateFixture(time.Date(2026, time.October, 22, 16, 0, 0, 0, time.UTC))
		if err := st.saveForecastDiagnostics(context.Background(), run, nil); err == nil {
			t.Fatal("stored empty evidence silently reset")
		}
		if _, _, err := st.forecastDiagnostics(); err == nil {
			t.Fatal("stored empty evidence read as initialized history")
		}
		if sqlNull && scalar[int](t, st, `SELECT COUNT(*) FROM meta WHERE k IN ('forecast_state_calls','forecast_state_shadow') AND v IS NULL`) != 2 {
			t.Fatal("NULL state changed")
		}
		if !sqlNull && (st.GetSetting(setForecastStateCalls, "missing") != "" || st.GetSetting(setForecastStateShadow, "missing") != "") {
			t.Fatal("empty state changed")
		}
	}
}

func TestForecastShadowDiagnosticsOutOfOrderPreservesLatestEvidence(t *testing.T) {
	st := newTestStore(t)
	at := time.Date(2026, time.October, 22, 16, 0, 0, 0, time.UTC)
	for i := range 24 {
		run := shadowStateFixture(at.Add(time.Duration(i) * time.Minute))
		if err := st.saveForecastDiagnostics(context.Background(), run, []forecastCallObservation{{Source: quoteSourceSina, Calls: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, days := range []int{-1, -30} {
		if err := st.saveForecastDiagnostics(context.Background(), shadowStateFixture(at.AddDate(0, 0, days)), []forecastCallObservation{{Source: quoteSourceSina, Calls: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	shadow, calls, err := st.forecastDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if shadow.UpdatedAt != "2026-10-22T16:23:00Z" || calls.UpdatedAt != shadow.UpdatedAt || len(shadow.Days) != 2 || shadow.Days[1].Matches != 24 || len(calls.Days) != 2 || calls.Days[1].Sources[quoteSourceSina].Calls != 24 || len(shadow.Recent) != 20 || shadow.Recent[0].At != "2026-10-22T16:04:00Z" || shadow.Recent[19].At != shadow.UpdatedAt {
		t.Fatalf("late write erased newer evidence: %+v %+v", shadow, calls)
	}
}

func TestForecastShadowDiagnosticsValidJSONCorruptionAndOverflow(t *testing.T) {
	for _, kind := range []string{"negative", "overflow", "duplicate_day", "missing_comparison", "null_pair", "missing_version", "missing_days", "counter_balance"} {
		t.Run(kind, func(t *testing.T) {
			st := newTestStore(t)
			run := shadowStateFixture(time.Date(2026, time.October, 22, 16, 0, 0, 0, time.UTC))
			if err := st.saveForecastDiagnostics(context.Background(), run, []forecastCallObservation{{Source: quoteSourceSina, Calls: 1}}); err != nil {
				t.Fatal(err)
			}
			shadow, calls, err := st.forecastDiagnostics()
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "counter_balance":
				shadow.Days[0].Selected = 0
				shadow.Days[0].Matches = 1<<63 - 1
				shadow.Days[0].Mismatches = 1<<63 - 1
				shadow.Days[0].Unavailable = 2
			case "negative", "overflow":
				count := calls.Days[0].Sources[quoteSourceSina]
				count.Calls = -1
				if kind == "overflow" {
					count.Calls = 1<<63 - 1
				}
				calls.Days[0].Sources[quoteSourceSina] = count
			case "duplicate_day":
				shadow.Days = append(shadow.Days, shadow.Days[0])
			case "missing_comparison":
				shadow.Recent[0].Comparison = nil
			case "null_pair":
				if err := st.SetSetting(setForecastStateShadow, "null"); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "null_pair" {
				for _, item := range []struct {
					key   string
					value any
				}{{setForecastStateShadow, shadow}, {setForecastStateCalls, calls}} {
					body, err := json.Marshal(item.value)
					if err != nil {
						t.Fatal(err)
					}
					if kind == "missing_version" || kind == "missing_days" {
						var fields map[string]json.RawMessage
						if err := json.Unmarshal(body, &fields); err != nil {
							t.Fatal(err)
						}
						key := "version"
						if kind == "missing_days" {
							key = "days"
						}
						delete(fields, key)
						body, err = json.Marshal(fields)
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := st.SetSetting(item.key, string(body)); err != nil {
						t.Fatal(err)
					}
				}
			}
			beforeShadow, beforeCalls := st.GetSetting(setForecastStateShadow, ""), st.GetSetting(setForecastStateCalls, "")
			if err := st.saveForecastDiagnostics(context.Background(), run, []forecastCallObservation{{Source: quoteSourceSina, Calls: 1}}); err == nil {
				t.Fatal("invalid counters/evidence accepted")
			}
			if st.GetSetting(setForecastStateShadow, "") != beforeShadow || st.GetSetting(setForecastStateCalls, "") != beforeCalls {
				t.Fatal("refusal overwrote damaged evidence")
			}
		})
	}
}

func TestForecastShadowDiagnosticsBoundsStoredDifferences(t *testing.T) {
	st := newTestStore(t)
	run := shadowStateFixture(time.Date(2026, time.October, 22, 16, 0, 0, 0, time.UTC))
	run.Results[0].Status = "mismatch"
	run.Results[0].Comparison.Match = false
	run.Results[0].Comparison.Differences = make([]quoteShadowDifference, 100)
	if err := st.saveForecastDiagnostics(context.Background(), run, []forecastCallObservation{{Source: quoteSourceSina, Calls: 1, Failure: true}, {Source: "configuration", Refusal: "no_shadow"}}); err != nil {
		t.Fatal(err)
	}
	shadow, calls, err := st.forecastDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	result := shadow.Recent[0]
	if !result.Truncated || result.DifferenceCount != 100 || len(result.Comparison.Differences) != 32 || result.Comparison.Match || calls.Days[0].Sources[quoteSourceSina].Failures != 1 || calls.Days[0].Sources["configuration"].Calls != 0 || calls.Days[0].Sources["configuration"].Refusals["no_shadow"] != 1 {
		t.Fatalf("bounded evidence/call classes wrong: %+v %+v", result, calls)
	}
	if len(run.Results[0].Comparison.Differences) != 100 {
		t.Fatal("persistence mutated live comparison")
	}
	var encoded map[string]any
	if err := json.Unmarshal([]byte(st.GetSetting(setForecastStateShadow, "")), &encoded); err != nil || encoded["version"] != float64(1) {
		t.Fatal("invalid persisted schema version")
	}
}
