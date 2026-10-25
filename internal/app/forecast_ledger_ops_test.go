package app

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReadinessItemSixNeedsALedgerChain(t *testing.T) {
	s := newV1Server(t)
	satisfyForecastReadiness(t, s)
	for _, order := range []string{quoteSourceSina, quoteSourceYahoo} {
		if err := s.st.SetSetting(setQuoteSourceOrder, order); err != nil {
			t.Fatal(err)
		}
		items := readinessFailure(s.forecastReadiness(time.Now()))
		if len(items) != 1 || items[0].Key != "ledger_chain" {
			t.Fatalf("order %s failures = %+v", order, items)
		}
		rec := saveForecastSettings(t, s, `{"mode":"1"}`)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ledger_chain") {
			t.Fatalf("disabled primary accepted: %d %s", rec.Code, rec.Body.String())
		}
		if got := s.st.GetSetting(setForecastAuto, ""); got != forecastModeDry {
			t.Fatalf("refused mode persisted: %s", got)
		}
	}
	if err := s.st.SetSetting(setQuoteSourceOrder, quoteSourceTencent); err != nil {
		t.Fatal(err)
	}
	if items := readinessFailure(s.forecastReadiness(time.Now())); len(items) != 0 {
		t.Fatalf("restored primary not ready: %+v", items)
	}
	if rec := saveForecastSettings(t, s, `{"mode":"1"}`); rec.Code != http.StatusOK {
		t.Fatalf("restored primary refused: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWatchdogNoSourceIsDailyAndOffIsSilent(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setQuoteSourceOrder, quoteSourceSina); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, mode := range []string{forecastModeOff, forecastModeDry, forecastModeOn} {
		if err := s.st.SetSetting(setForecastAuto, mode); err != nil {
			t.Fatal(err)
		}
		if err := s.st.saveForecastHeartbeat(forecastHeartbeat{Mode: mode, LastTickAt: now.Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
		s.forecastWatchdogOnce(now)
		_, have := s.st.forecastAlertState().Last["no_source"]
		if have != (mode != forecastModeOff) {
			t.Fatalf("mode %s no_source alert = %v", mode, have)
		}
	}
	first := s.st.forecastAlertState().Last["no_source"]
	s.forecastWatchdogOnce(now.Add(23 * time.Hour))
	if got := s.st.forecastAlertState().Last["no_source"]; got != first {
		t.Fatalf("repeated inside daily window: %s -> %s", first, got)
	}
	s.forecastWatchdogOnce(now.Add(24 * time.Hour))
	second := s.st.forecastAlertState().Last["no_source"]
	if second == first {
		t.Fatal("missing daily repeat")
	}
	if err := s.st.SetSetting(setQuoteSourceOrder, quoteSourceTencent); err != nil {
		t.Fatal(err)
	}
	s.forecastWatchdogOnce(now.Add(48 * time.Hour))
	if got := s.st.forecastAlertState().Last["no_source"]; got != second {
		t.Fatal("restored primary still alerts")
	}
}

func TestReadinessLedgerChainNeedsExplicitGrantAndBothEvidenceFlags(t *testing.T) {
	for _, missing := range []string{"grant", "cash", "kind"} {
		t.Run(missing, func(t *testing.T) {
			s := newV1Server(t)
			src := ledgerWalkSource(quoteSourceTencent, nil)
			switch missing {
			case "grant":
				src.caps = nil
			case "cash":
				src.cashDividends = false
			case "kind":
				src.reportsKind = false
			}
			s.quotes.sources = []quoteSource{src}
			items := s.forecastReadiness(time.Now())
			if got := items[len(items)-1]; got.Key != "ledger_chain" || got.OK {
				t.Fatalf("missing %s admitted: %+v", missing, got)
			}
		})
	}
}

func TestWatchdogNoSourceDoesNotRequireAHeartbeat(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setQuoteSourceOrder, quoteSourceSina); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(setForecastAuto, forecastModeOff); err != nil {
		t.Fatal(err)
	}
	var reads []string
	s.st.onSettingRead = func(key string) { reads = append(reads, key) }
	s.forecastWatchdogOnce(time.Now())
	s.st.onSettingRead = nil
	if len(reads) != 1 || reads[0] != setForecastAuto {
		t.Fatalf("off performed extra reads: %v", reads)
	}
	if raw := s.st.GetSetting(setForecastAlertState, ""); raw != "" {
		t.Fatalf("off wrote alert state: %s", raw)
	}
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	if _, have := s.st.ForecastHeartbeat(); have {
		t.Fatal("fixture unexpectedly has a heartbeat")
	}
	// Dry does its vendor work only on a manual pass (step), so a missing heartbeat is the ordinary
	// state of the mode and not a stall. `no_source` is about the configuration and still fires.
	s.forecastWatchdogOnce(time.Now())
	state := s.st.forecastAlertState()
	if _, have := state.Last["no_source"]; !have {
		t.Fatalf("missing no_source without a heartbeat: %+v", state)
	}
	if _, have := state.Last["stalled"]; have {
		t.Fatalf("dry mode with no manual pass paged as stalled: %+v", state)
	}
	// Mode 1 is the one with a loop to hear from, and it must still page when that loop is silent.
	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	s.forecastWatchdogOnce(time.Now())
	if _, have := s.st.forecastAlertState().Last["stalled"]; !have {
		t.Fatal("mode 1 with no heartbeat did not page")
	}
}
