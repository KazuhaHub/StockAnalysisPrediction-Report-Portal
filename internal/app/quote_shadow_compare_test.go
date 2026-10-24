package app

import (
	"reflect"
	"testing"
)

func shadowTestBar(date string, open, high, low, close int64) QuoteBar {
	return QuoteBar{Date: date, Open: open, High: high, Low: low, Close: close}
}

func TestQuoteShadowComparesFenDatesAndBothAnchors(t *testing.T) {
	primary := []QuoteBar{shadowTestBar("2026-10-19", 1000, 1100, 900, 1050), shadowTestBar("2026-10-20", 1050, 1200, 950, 1100)}
	anchors := [2]string{"2026-10-19", "2026-10-20"}
	for _, tc := range []struct {
		name        string
		change      func([]QuoteBar) []QuoteBar
		field, date string
	}{
		{"same", func(b []QuoteBar) []QuoteBar { return b }, "", ""},
		{"first anchor close", func(b []QuoteBar) []QuoteBar { b[0].Close++; return b }, "close", anchors[0]},
		{"last anchor close", func(b []QuoteBar) []QuoteBar { b[1].Close++; return b }, "close", anchors[1]},
		{"open", func(b []QuoteBar) []QuoteBar { b[0].Open++; return b }, "open", anchors[0]},
		{"high", func(b []QuoteBar) []QuoteBar { b[0].High++; return b }, "high", anchors[0]},
		{"low", func(b []QuoteBar) []QuoteBar { b[0].Low++; return b }, "low", anchors[0]},
		{"missing date", func(b []QuoteBar) []QuoteBar { return b[1:] }, "date", anchors[0]},
		{"extra date", func(b []QuoteBar) []QuoteBar { return append(b, shadowTestBar("2026-10-21", 1100, 1200, 1000, 1150)) }, "date", "2026-10-21"},
		{"volume is not price evidence", func(b []QuoteBar) []QuoteBar { b[0].Volume++; return b }, "", ""},
		{"source order is irrelevant", func(b []QuoteBar) []QuoteBar { b[0], b[1] = b[1], b[0]; return b }, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := tc.change(append([]QuoteBar(nil), primary...))
			got, err := compareQuoteShadow(primary, candidate, anchors)
			if err != nil {
				t.Fatal(err)
			}
			if got.Match != (tc.field == "") {
				t.Fatalf("comparison = %+v", got)
			}
			if tc.field != "" && (len(got.Differences) != 1 || got.Differences[0].Field != tc.field || got.Differences[0].Date != tc.date) {
				t.Fatalf("difference = %+v", got.Differences)
			}
			if tc.field != "" && tc.field != "date" {
				difference := got.Differences[0]
				if difference.Primary == nil || difference.Candidate == nil || *difference.Candidate-*difference.Primary != 1 {
					t.Fatalf("exact fen evidence missing: %+v", difference)
				}
			}
		})
	}
}

func TestQuoteShadowIncludesNonAnchorOHLC(t *testing.T) {
	p := []QuoteBar{shadowTestBar("2026-10-19", 1000, 1100, 900, 1000), shadowTestBar("2026-10-20", 1000, 1100, 900, 1000), shadowTestBar("2026-10-21", 1000, 1100, 900, 1000)}
	c := append([]QuoteBar(nil), p...)
	c[1].Close++
	got, err := compareQuoteShadow(p, c, [2]string{p[0].Date, p[2].Date})
	if err != nil || got.Match || got.ComparedDates != 3 || len(got.Differences) != 1 {
		t.Fatalf("interior mismatch lost: %+v %v", got, err)
	}
	d := got.Differences[0]
	if d.Date != p[1].Date || d.Anchor || d.Field != "close" || d.Primary == nil || d.Candidate == nil || *d.Primary != 1000 || *d.Candidate != 1001 {
		t.Fatalf("interior mismatch evidence changed: %+v", d)
	}
}

func TestQuoteShadowRejectsUnusableEvidence(t *testing.T) {
	valid := []QuoteBar{shadowTestBar("2026-10-19", 1000, 1100, 900, 1050)}
	anchors := [2]string{valid[0].Date, valid[0].Date}
	for _, bad := range [][]QuoteBar{nil, {valid[0], valid[0]}, {shadowTestBar("invalid", 1000, 1100, 900, 1050)}, {shadowTestBar(valid[0].Date, 0, 1100, 900, 1050)}} {
		for _, primaryBad := range []bool{false, true} {
			p, c := valid, bad
			if primaryBad {
				p, c = bad, valid
			}
			if got, err := compareQuoteShadow(p, c, anchors); err == nil || got.Match {
				t.Fatalf("unusable evidence compared: primary=%v candidate=%v result=%+v error=%v", p, c, got, err)
			}
		}
	}
	for _, a := range [][2]string{{}, {"bad", "bad"}, {"2026-10-20", "2026-10-19"}, {"2026-10-19", "2026-10-20"}} {
		if _, err := compareQuoteShadow(valid, valid, a); err == nil {
			t.Fatalf("invalid or absent primary anchors accepted: %v", a)
		}
	}
}

func TestQuoteShadowIsDeterministicAndDoesNotMutateInputs(t *testing.T) {
	p := []QuoteBar{shadowTestBar("2026-10-20", 1050, 1200, 950, 1100), shadowTestBar("2026-10-19", 1000, 1100, 900, 1050)}
	c := append([]QuoteBar(nil), p...)
	c[0].Open++
	c[1].Close++
	pBefore, cBefore := append([]QuoteBar(nil), p...), append([]QuoteBar(nil), c...)
	anchors := [2]string{"2026-10-19", "2026-10-20"}
	want, err := compareQuoteShadow(p, c, anchors)
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		got, err := compareQuoteShadow(p, c, anchors)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("unstable comparison: %+v %v", got, err)
		}
	}
	if !reflect.DeepEqual(p, pBefore) || !reflect.DeepEqual(c, cBefore) {
		t.Fatal("comparison mutated a source body")
	}
	if len(want.Differences) != 2 || want.Differences[0].Date != anchors[0] || !want.Differences[0].Anchor || !want.Differences[1].Anchor {
		t.Fatalf("anchors or deterministic order lost: %+v", want)
	}
}
