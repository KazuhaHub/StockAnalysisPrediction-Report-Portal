package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestDryPassReadinessNeedsASample is the gate the 2026-10-24 incident walked through. A dry pass with
// a silent vendor plans nothing and records errors=0 — correctly, because a source failure is not this
// mark's attempt and must never finalise a cohort — so the readiness item used to go green on exactly
// the evidence an operator would have called broken. "No error" is not "the vendor answered"; a sample
// is.
func TestDryPassReadinessNeedsASample(t *testing.T) {
	base := func(t *testing.T, pass passStats, sample []drySample) *Server {
		t.Helper()
		s := newV1Server(t)
		if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
			t.Fatal(err)
		}
		pass.Kind, pass.StartedAt, pass.FinishedAt = "manual",
			time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)
		if err := s.st.saveForecastHeartbeat(forecastHeartbeat{
			Mode: forecastModeDry, LastTickAt: time.Now().UTC().Format(time.RFC3339),
			Pass: pass, DrySample: sample,
		}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	dryPass := func(s *Server) readinessItem {
		for _, it := range s.forecastReadiness(time.Now()) {
			if it.Key == "dry_pass" {
				return it
			}
		}
		t.Fatal("no dry_pass item")
		return readinessItem{}
	}
	sample := []drySample{{SetID: 1, Kind: "settle", Market: "sh", Code: "600160", Source: quoteSourceTencent}}

	// Positive control, first: without one, a check that fails everything would pass this test.
	t.Run("a pass that planned a write is ready", func(t *testing.T) {
		s := base(t, passStats{Sets: 1}, sample)
		if it := dryPass(s); !it.OK {
			t.Fatalf("a sampled dry pass is not ready: %+v", it)
		}
	})

	// The vendor answered and nothing was plannable: the ledger has no due mark, or the marks it has
	// cannot settle yet. That is a fact about the ledger, not about the vendor, and it says why.
	t.Run("a vendor that answered but planned nothing says so", func(t *testing.T) {
		s := base(t, passStats{Sets: 3, Bodies: 2}, nil)
		it := dryPass(s)
		if it.OK || it.Reason != "dry_pass_no_plan" {
			t.Fatalf("dry_pass = %+v, want dry_pass_no_plan", it)
		}
	})

	// The incident: due work, no body, no sample. This is the state the old check called healthy.
	t.Run("a silent vendor is not ready", func(t *testing.T) {
		s := base(t, passStats{Sets: 3}, nil)
		it := dryPass(s)
		if it.OK || it.Reason != "dry_pass_no_source" {
			t.Fatalf("dry_pass = %+v, want dry_pass_no_source", it)
		}
	})
}

// TestReadinessRefusesTheSwitchWithoutAVendorBody is the same property where it matters: the operator
// presses On and is refused, whatever the other five items say. Then the vendor comes back, one manual
// dry pass produces a sample, and the same switch goes through.
func TestReadinessRefusesTheSwitchWithoutAVendorBody(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	now := shanghai(2026, time.November, 20, 16, 0)
	e.now = func() time.Time { return now }
	if _, _, code, err := s.st.SaveForecastPopulationFrom(
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), now); err != nil || code != "" {
		t.Fatalf("population: %v %s", err, code)
	}
	for _, code := range []string{"sh000852"} {
		if err := s.st.RecordForecastDryGate(code, forecastGate{
			At: now.UTC().Format(time.RFC3339), By: "test", Note: "checked"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	// The stamp is a real clock reading, not the fixture's own future `now`: readiness refuses a
	// verification stamped more than five minutes ahead of the wall clock, which is the rule that
	// stops a clock change from proving an untested channel.
	if err := s.st.SetSetting(setForecastAlertTestOKAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}

	// Every vendor request is refused, which is what the settlement endpoint was doing.
	silent := true
	fetch := e.fetch
	e.fetch = nil
	s.quotes.sources = []quoteSource{ledgerWalkSource(quoteSourceTencent, func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		if silent {
			return nil, errors.New("vendor web.ifzq.gtimg.cn returned 501 Not Implemented")
		}
		return fetch(ctx, market, code, from, to)
	})}

	e.step(s, "manual")
	hb, _ := s.st.ForecastHeartbeat()
	if len(hb.DrySample) != 0 || hb.Pass.Errors != 0 {
		t.Fatalf("fixture: sample=%d errors=%d, want the incident's empty-but-errorless pass",
			len(hb.DrySample), hb.Pass.Errors)
	}
	rec := saveForecastSettings(t, s, `{"mode":"1"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "dry_pass_no_source") {
		t.Fatalf("switch with a silent vendor = %d %s, want 409 dry_pass_no_source",
			rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setForecastAuto, "0"); got == forecastModeOn {
		t.Fatal("a refused switch was persisted")
	}

	// The vendor recovers; one manual dry pass now carries evidence and the switch is allowed.
	silent = false
	s.quotes.mu.Lock()
	s.quotes.cooling = nil // a new process, as a restart would be
	s.quotes.mu.Unlock()
	e.step(s, "manual")
	if hb, _ := s.st.ForecastHeartbeat(); len(hb.DrySample) == 0 {
		t.Fatal("recovered vendor produced no sample, so the positive half proves nothing")
	}
	if rec := saveForecastSettings(t, s, `{"mode":"1"}`); rec.Code != http.StatusOK {
		t.Fatalf("switch after a sampled dry pass = %d %s", rec.Code, rec.Body.String())
	}
}
