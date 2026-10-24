package app

import (
	"reflect"
	"testing"
)

// goldenArchivedCoverage preserves the frozen characterization file's original key order and its
// never-populated refusal placeholder. It is NOT the live API type. Every live counter is copied
// exactly; the adapter cannot normalize away a behavioral or arithmetic change.
type goldenArchivedCoverage struct {
	Reports         int `json:"reports"`
	Member          int `json:"member"`
	Duplicate       int `json:"duplicate"`
	Absent          int `json:"absent"`
	Rejected        int `json:"rejected"`
	Backtest        int `json:"backtest"`
	Degraded        int `json:"degraded"`
	Mismatch        int `json:"mismatch"`
	Refused         int `json:"refused"`
	PendingBaseline int `json:"pending_baseline"`
	PreLive         int `json:"pre_live"`
	Test            int `json:"test"`
	OffBenchmark    int `json:"off_benchmark"`
	Unknown         int `json:"unknown,omitempty"`
}

func goldenArchivedCoverageOf(c forecastCoverage) goldenArchivedCoverage {
	return goldenArchivedCoverage{
		Reports: c.Reports, Member: c.Member, Duplicate: c.Duplicate, Absent: c.Absent, Rejected: c.Rejected,
		Backtest: c.Backtest, Degraded: c.Degraded, Mismatch: c.Mismatch, PendingBaseline: c.PendingBaseline,
		PreLive: c.PreLive, Test: c.Test, OffBenchmark: c.OffBenchmark, Unknown: c.Unknown,
	}
}

func TestGoldenCoverageAdapterPreservesEveryLiveField(t *testing.T) {
	var coverage forecastCoverage
	live := reflect.ValueOf(&coverage).Elem()
	for i := 0; i < live.NumField(); i++ {
		if live.Field(i).Kind() != reflect.Int {
			t.Fatalf("new non-counter field needs explicit snapshot handling: %s", live.Type().Field(i).Name)
		}
		live.Field(i).SetInt(int64(101 + i*103))
	}
	archived := reflect.ValueOf(goldenArchivedCoverageOf(coverage))
	for i := 0; i < live.NumField(); i++ {
		field := live.Type().Field(i)
		copy := archived.FieldByName(field.Name)
		copyType, ok := archived.Type().FieldByName(field.Name)
		if !copy.IsValid() || !ok || copyType.Tag.Get("json") != field.Tag.Get("json") || copy.Kind() != reflect.Int || copy.Int() != live.Field(i).Int() {
			t.Errorf("live coverage field %s is missing or altered in the archived projection", field.Name)
		}
	}
	for i := 0; i < archived.NumField(); i++ {
		field := archived.Type().Field(i)
		if field.Name == "Refused" {
			if archived.Field(i).Int() != 0 {
				t.Error("retired placeholder is not zero")
			}
			continue
		}
		if _, ok := live.Type().FieldByName(field.Name); !ok {
			t.Errorf("unexpected archived-only field %s", field.Name)
		}
	}
}
