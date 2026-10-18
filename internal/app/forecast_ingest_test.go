package app

// The ingest classifier (ADR 0029 §3, plan §3.5, design §11 #1–8).
//
// These are the tests the producer contract is: every rule the contract states has a row here, and
// every row asserts the REASON, not just that something was refused. A refusal with the wrong reason
// sends whoever fixes the producer to the wrong field.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// predWire builds a valid payload as raw JSON, then lets a case mutate it. Written as a map so a test
// can express "the same payload, but the price is a number" without copying forty lines.
func predWire(mutate func(m map[string]any)) json.RawMessage {
	m := map[string]any{
		"schema": "predictions.v1",
		"emitter": map[string]any{
			"app": "1-6-4", "version": "V3.16.2",
			"calculator": "scenario-calc-3.5", "decision_schema": "decision.v3.4",
		},
		"policy_version":     "v3config-1",
		"probability_method": "approved_policy_default",
		"decision_status":    "eligible",
		"decision_quality":   "normal",
		"baseline":           map[string]any{"price": "8.52", "as_of": "2026-10-16", "source": "company_quote"},
		"horizon_end":        "2027-10-17",
		"claims": []any{
			map[string]any{"key": "stance", "kind": "stance", "stance": "long", "target_weight": "0.1000"},
			map[string]any{"key": "expected_terminal", "kind": "terminal_value", "price": "9.95", "p_profit": "0.7000"},
			map[string]any{"key": "scenario.bull", "kind": "scenario", "label": "bull", "price": "14.00", "probability": "0.2500"},
			map[string]any{"key": "scenario.base", "kind": "scenario", "label": "base", "price": "10.00", "probability": "0.4500"},
			map[string]any{"key": "scenario.bear", "kind": "scenario", "label": "bear", "price": "6.50", "probability": "0.3000"},
			map[string]any{"key": "entry_price", "kind": "entry_price", "price": "8.65"},
		},
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// claim rewrites one claim in a built payload by key.
func claim(m map[string]any, key string, mutate func(c map[string]any)) {
	cs, _ := m["claims"].([]any)
	for _, raw := range cs {
		if c, ok := raw.(map[string]any); ok && c["key"] == key {
			mutate(c)
			return
		}
	}
	panic("no claim " + key)
}

// receipt is the instant the tests classify against: 2026-10-17 21:14 Shanghai, which is 13:14 UTC.
var receipt = time.Date(2026, 10, 17, 13, 14, 0, 0, time.UTC)

func classify(t *testing.T, raw json.RawMessage, rtype string) (*predSetRow, []predClaimRow, []predError) {
	t.Helper()
	return classifyPredictions(predInput{
		Raw: raw, Rtype: rtype, Symbol: "600160", MadeAt: receipt,
		BodySHA: "body", BenchCode: "sh000852",
	})
}

func onlyReason(t *testing.T, errs []predError) string {
	t.Helper()
	if len(errs) == 0 {
		t.Fatal("expected a refusal, got none")
	}
	return errs[0].Reason
}

// TestClassifyPredictionsAcceptsAValidPayload is the baseline the refusals are measured against: if
// this fails, every other case in the file is testing the wrong thing.
func TestClassifyPredictionsAcceptsAValidPayload(t *testing.T) {
	set, claims, errs := classify(t, predWire(nil), predTestRtype)
	if len(errs) != 0 {
		t.Fatalf("a valid payload was refused: %+v", errs)
	}
	if set.State != predStateAccepted {
		t.Errorf("state = %q, want %q", set.State, predStateAccepted)
	}
	if set.MadeOn != "2026-10-17" || set.Cohort != "2026-10" {
		t.Errorf("made_on/cohort = %q/%q, want 2026-10-17/2026-10 (Shanghai, not UTC)", set.MadeOn, set.Cohort)
	}
	if set.HorizonStd != 1 {
		t.Errorf("horizon_std = %d, want 1: 2027-10-17 is 12 months after 2026-10-17", set.HorizonStd)
	}
	if set.EmittedBaseFen == nil || *set.EmittedBaseFen != 852 {
		t.Errorf("emitted_base_fen = %v, want 852", set.EmittedBaseFen)
	}
	if len(claims) != 6 {
		t.Errorf("claims = %d, want 6", len(claims))
	}
	// The scenario probabilities are basis points, and the sum is exact integer arithmetic.
	var sum int32
	for _, c := range claims {
		if c.Kind == kindScenario && c.ProbBp != nil {
			sum += *c.ProbBp
		}
	}
	if sum != 10000 {
		t.Errorf("scenario bp sum = %d, want 10000", sum)
	}
}

// TestClassifyPredictionsRefusalReasons walks every refusal reason. Each case is a payload a real
// producer could send, and each asserts the reason — the reason is what tells a producer what to fix.
func TestClassifyPredictionsRefusalReasons(t *testing.T) {
	cases := []struct {
		name   string
		raw    json.RawMessage
		reason string
	}{
		{"string instead of object", json.RawMessage(`"{\"schema\":\"predictions.v1\"}"`), reasonNotObject},
		{"array instead of object", json.RawMessage(`[]`), reasonNotObject},
		{"prose", json.RawMessage(`not JSON at all`), reasonNotObject},
		{"unknown field", predWire(func(m map[string]any) { m["surprise"] = 1 }), reasonBadSchema},
		{"wrong schema", predWire(func(m map[string]any) { m["schema"] = "predictions.v2" }), reasonBadSchema},
		{"unknown decision_status", predWire(func(m map[string]any) { m["decision_status"] = "maybe" }), reasonBadEnum},
		{"unknown probability_method", predWire(func(m map[string]any) { m["probability_method"] = "vibes" }), reasonBadEnum},
		{"unknown scenario_basis", predWire(func(m map[string]any) { m["scenario_basis"] = "vibes" }), reasonBadEnum},
		{"unknown adoption", predWire(func(m map[string]any) {
			m["scenario_provenance"] = map[string]any{"evidence": map[string]any{"adoption": "vibes"}}
		}), reasonBadEnum},
		{"provenance as a string", predWire(func(m map[string]any) {
			m["scenario_provenance"] = `{"producer":"1-4-2/目标价合成"}`
		}), reasonNotObject},
		{"unknown claim kind", predWire(func(m map[string]any) {
			claim(m, "entry_price", func(c map[string]any) { c["kind"] = "vibes" })
		}), reasonBadEnum},
		{"reserved stop_loss", predWire(func(m map[string]any) {
			claim(m, "entry_price", func(c map[string]any) { c["kind"] = kindStopLoss })
		}), reasonReservedKind},
		{"unknown stance", predWire(func(m map[string]any) {
			claim(m, "stance", func(c map[string]any) { c["stance"] = "aggressive" })
		}), reasonBadEnum},
		{"unknown scenario label", predWire(func(m map[string]any) {
			claim(m, "scenario.bull", func(c map[string]any) { c["label"] = "moonshot" })
		}), reasonBadEnum},
		// Prices. Each of these is a real thing a producer does, and the point of every one is that it
		// is refused rather than coerced.
		{"price with three decimals", predWire(func(m map[string]any) {
			claim(m, "entry_price", func(c map[string]any) { c["price"] = "8.655" })
		}), reasonBadPrice},
		{"price as a JSON number", predWire(func(m map[string]any) {
			claim(m, "entry_price", func(c map[string]any) { c["price"] = 8.65 })
		}), reasonBadPrice},
		{"negative price", predWire(func(m map[string]any) {
			claim(m, "entry_price", func(c map[string]any) { c["price"] = "-1.00" })
		}), reasonBadPrice},
		{"exponent price", predWire(func(m map[string]any) {
			claim(m, "entry_price", func(c map[string]any) { c["price"] = "1e2" })
		}), reasonBadPrice},
		{"baseline price malformed", predWire(func(m map[string]any) {
			m["baseline"].(map[string]any)["price"] = "8.5.2"
		}), reasonBadPrice},
		// Probabilities.
		{"probability above one", predWire(func(m map[string]any) {
			claim(m, "scenario.bull", func(c map[string]any) { c["probability"] = "1.0001" })
		}), reasonBadProbability},
		{"probability with five decimals", predWire(func(m map[string]any) {
			claim(m, "scenario.bull", func(c map[string]any) { c["probability"] = "0.25000" })
		}), reasonBadProbability},
		{"p_profit malformed", predWire(func(m map[string]any) {
			claim(m, "expected_terminal", func(c map[string]any) { c["p_profit"] = "70%" })
		}), reasonBadProbability},
		{"weight malformed", predWire(func(m map[string]any) {
			claim(m, "stance", func(c map[string]any) { c["target_weight"] = "10%" })
		}), reasonBadProbability},
		// Dates.
		{"horizon malformed", predWire(func(m map[string]any) { m["horizon_end"] = "2027/10/17" }), reasonBadDate},
		{"baseline as_of malformed", predWire(func(m map[string]any) {
			m["baseline"].(map[string]any)["as_of"] = "16-10-2026"
		}), reasonBadDate},
		{"cutoff not an instant", predWire(func(m map[string]any) { m["information_cutoff"] = "2026-10-17" }), reasonBadDate},
		// Claims.
		{"duplicate claim key", predWire(func(m map[string]any) {
			cs := m["claims"].([]any)
			m["claims"] = append(cs, map[string]any{"key": "entry_price", "kind": "entry_price", "price": "9.00"})
		}), reasonDuplicateKey},
		{"missing stance", predWire(func(m map[string]any) { m["claims"] = dropClaim(m, "stance") }), reasonMissingClaim},
		{"missing terminal value", predWire(func(m map[string]any) { m["claims"] = dropClaim(m, "expected_terminal") }), reasonMissingClaim},
		{"missing a scenario", predWire(func(m map[string]any) { m["claims"] = dropClaim(m, "scenario.bear") }), reasonMissingClaim},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, claims, errs := classify(t, tc.raw, predTestRtype)
			if got := onlyReason(t, errs); got != tc.reason {
				t.Fatalf("reason = %q, want %q (errors: %+v)", got, tc.reason, errs)
			}
			if set == nil {
				t.Fatal("no set row: a payload that arrived must leave a trace")
			}
			if set.State != predStateRejected {
				t.Errorf("state = %q, want %q", set.State, predStateRejected)
			}
			if len(claims) != 0 {
				t.Errorf("a rejected set kept %d claims; claims are only written for accepted sets", len(claims))
			}
			if set.Errors == "" {
				t.Error("errors is empty; the reason a set was refused has to be recorded on it")
			}
		})
	}
}

func dropClaim(m map[string]any, key string) []any {
	var out []any
	for _, raw := range m["claims"].([]any) {
		if c, ok := raw.(map[string]any); ok && c["key"] == key {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// TestClassifyPredictionsScenarioSumIsExact: probabilities are basis points, so the boundary is
// 9999/10000/10001 rather than a float tolerance. A tolerance would be the one place a rounding
// difference between two producers is invisible.
func TestClassifyPredictionsScenarioSumIsExact(t *testing.T) {
	for _, tc := range []struct {
		bull, base, bear string
		ok               bool
	}{
		{"0.2500", "0.4500", "0.3000", true},
		{"0.2500", "0.4500", "0.2999", false}, // 9999 bp
		{"0.2500", "0.4500", "0.3001", false}, // 10001 bp
	} {
		t.Run(tc.bull+"/"+tc.base+"/"+tc.bear, func(t *testing.T) {
			raw := predWire(func(m map[string]any) {
				claim(m, "scenario.bull", func(c map[string]any) { c["probability"] = tc.bull })
				claim(m, "scenario.base", func(c map[string]any) { c["probability"] = tc.base })
				claim(m, "scenario.bear", func(c map[string]any) { c["probability"] = tc.bear })
			})
			_, _, errs := classify(t, raw, predTestRtype)
			if tc.ok && len(errs) != 0 {
				t.Fatalf("refused a valid sum: %+v", errs)
			}
			if !tc.ok && onlyReason(t, errs) != reasonProbabilitySum {
				t.Fatalf("reason = %q, want %q", onlyReason(t, errs), reasonProbabilitySum)
			}
		})
	}
}

// TestClassifyPredictionsSizeCap: the payload is capped, and the cap keeps the bytes it can — a
// refusal that threw the payload away would leave an operator with nothing to look at.
func TestClassifyPredictionsSizeCap(t *testing.T) {
	big := make([]byte, predPayloadMax+1)
	copy(big, `{"schema":"predictions.v1","filler":"`)
	for i := len(`{"schema":"predictions.v1","filler":"`); i < len(big)-2; i++ {
		big[i] = 'x'
	}
	copy(big[len(big)-2:], `"}`)

	set, _, errs := classify(t, big, predTestRtype)
	if onlyReason(t, errs) != reasonTooLarge {
		t.Fatalf("reason = %q, want %q", onlyReason(t, errs), reasonTooLarge)
	}
	if set.Truncated != 1 {
		t.Error("payload_truncated = 0; a capped payload has to say it was capped")
	}
	if len(set.Payload) != predPayloadMax {
		t.Errorf("kept %d bytes, want %d", len(set.Payload), predPayloadMax)
	}
}

// TestClassifyPredictionsHorizon: the horizon is compared against the SHANGHAI date of receipt, and
// the standard window is what decides the h12 mark.
func TestClassifyPredictionsHorizon(t *testing.T) {
	// 2026-10-17T23:30Z is already 2026-10-18 in Shanghai. A horizon of the 18th is therefore today,
	// not the future — and this is exactly the case a UTC comparison gets wrong.
	lateReceipt := time.Date(2026, 10, 17, 23, 30, 0, 0, time.UTC)
	raw := predWire(func(m map[string]any) { m["horizon_end"] = "2026-10-18" })
	set, _, errs := classifyPredictions(predInput{
		Raw: raw, Rtype: predTestRtype, Symbol: "600160", MadeAt: lateReceipt, BenchCode: "sh000852",
	})
	if onlyReason(t, errs) != reasonHorizon {
		t.Fatalf("reason = %q, want %q: the receipt date is 2026-10-18 in Shanghai", onlyReason(t, errs), reasonHorizon)
	}
	if set.MadeOn != "2026-10-18" {
		t.Errorf("made_on = %q, want 2026-10-18", set.MadeOn)
	}

	// Standard is ~12 months; six months is accepted but not standard, so no h12 mark.
	short := predWire(func(m map[string]any) { m["horizon_end"] = "2027-04-17" })
	set, _, errs = classify(t, short, predTestRtype)
	if len(errs) != 0 {
		t.Fatalf("a six-month horizon was refused: %+v", errs)
	}
	if set.HorizonStd != 0 {
		t.Errorf("horizon_std = %d, want 0 for a six-month horizon", set.HorizonStd)
	}

	// A day inside the tolerance still counts as standard: the Dify sandbox runs in UTC and dates a
	// report a day early between 00:00 and 08:00 Shanghai time.
	edge := predWire(func(m map[string]any) { m["horizon_end"] = "2027-10-16" })
	set, _, errs = classify(t, edge, predTestRtype)
	if len(errs) != 0 {
		t.Fatalf("refused: %+v", errs)
	}
	if set.HorizonStd != 1 {
		t.Errorf("horizon_std = 0 for a horizon one day short; the sandbox's UTC date lag must stay in band")
	}
}

// TestClassifyPredictionsBacktest: a run over history is stored and marked, not scored, and the
// comparison is between INSTANTS with slack.
func TestClassifyPredictionsBacktest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cutoff string
		want   string
	}{
		{"no cutoff", "", predStateAccepted},
		{"cutoff at receipt", "2026-10-17T13:14:00Z", predStateAccepted},
		{"cutoff a day before receipt", "2026-10-16T13:14:00Z", predStateAccepted},
		{"cutoff two days before receipt", "2026-10-15T00:00:00Z", predStateAccepted},
		{"cutoff a week before receipt", "2026-10-10T00:00:00Z", predStateBacktest},
		{"cutoff months before receipt", "2026-06-01T00:00:00Z", predStateBacktest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := predWire(func(m map[string]any) {
				if tc.cutoff != "" {
					m["information_cutoff"] = tc.cutoff
				}
			})
			set, _, errs := classify(t, raw, predTestRtype)
			if len(errs) != 0 {
				t.Fatalf("refused: %+v", errs)
			}
			if set.State != tc.want {
				t.Errorf("state = %q, want %q", set.State, tc.want)
			}
		})
	}
}

// TestClassifyPredictionsCoverageRows: an absent row is written for a COVERED rtype that sent
// nothing, and no row at all for one the ledger does not cover. This is what makes coverage
// computable from the ledger alone.
func TestClassifyPredictionsCoverageRows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     json.RawMessage
		rtype   string
		wantSet bool
		want    string
	}{
		{"covered, no payload", nil, predTestRtype, true, predStateAbsent},
		{"covered, null payload", json.RawMessage(`null`), predTestRtype, true, predStateAbsent},
		{"covered, whitespace payload", json.RawMessage("  \n "), predTestRtype, true, predStateAbsent},
		{"uncovered, no payload", nil, "行业分析", false, ""},
		{"uncovered, payload present", predWire(nil), "行业分析", true, predStateAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _, errs := classify(t, tc.raw, tc.rtype)
			if len(errs) != 0 {
				t.Fatalf("refused: %+v", errs)
			}
			if !tc.wantSet {
				if set != nil {
					t.Fatalf("wrote a set for an uncovered rtype with no payload: %+v", set)
				}
				return
			}
			if set == nil {
				t.Fatal("no set row")
			}
			if set.State != tc.want {
				t.Errorf("state = %q, want %q", set.State, tc.want)
			}
		})
	}
}

// TestClassifyPredictionsSymbolScope: a symbol no vendor prices is refused for the PREDICTIONS and
// not for the report. Beijing codes have no usable daily history and B-shares are priced in another
// currency, so a claim against either would score silently and wrongly or never resolve.
func TestClassifyPredictionsSymbolScope(t *testing.T) {
	for _, sym := range []string{"830799", "900901", "200002", ""} {
		t.Run("symbol "+sym, func(t *testing.T) {
			set, _, errs := classifyPredictions(predInput{
				Raw: predWire(nil), Rtype: predTestRtype, Symbol: sym, MadeAt: receipt, BenchCode: "sh000852",
			})
			if onlyReason(t, errs) != reasonSymbolScope {
				t.Fatalf("reason = %q, want %q", onlyReason(t, errs), reasonSymbolScope)
			}
			if set == nil || set.State != predStateRejected {
				t.Errorf("set = %+v, want a rejected row", set)
			}
		})
	}
	// And an ordinary A-share is in scope.
	for _, sym := range []string{"600160", "000001", "300772", "688111"} {
		t.Run("in scope "+sym, func(t *testing.T) {
			_, _, errs := classifyPredictions(predInput{
				Raw: predWire(nil), Rtype: predTestRtype, Symbol: sym, MadeAt: receipt, BenchCode: "sh000852",
			})
			if len(errs) != 0 {
				t.Fatalf("%s was refused: %+v", sym, errs)
			}
		})
	}
}

// TestPredictionsSymbolIsScorable pins the scope rule itself.
func TestPredictionsSymbolIsScorable(t *testing.T) {
	for _, tc := range []struct {
		sym  string
		want bool
	}{
		{"600160", true}, {"000001", true}, {"300772", true}, {"688111", true}, {"002415", true}, {"601398", true},
		{"830799", false}, // Beijing: Tencent returns no daily history
		{"900901", false}, // Shanghai B-share
		{"200002", false}, // Shenzhen B-share
		{"", false},       // dropped by normalizeSymbol
		{"12345", false},  // not six digits
		{"60016a", false}, // not numeric
	} {
		if got := predictionsSymbolIsScorable(tc.sym); got != tc.want {
			t.Errorf("predictionsSymbolIsScorable(%q) = %v, want %v", tc.sym, got, tc.want)
		}
	}
}

// TestParseFenIsExactNotFloat: the conversion is digit arithmetic. A float would accept "8.655" and
// then be unable to say by how much it was wrong.
func TestParseFenIsExactNotFloat(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{`"8.65"`, 865, true},
		{`"8"`, 800, true},
		{`"0.01"`, 1, true},
		{`"294.00"`, 29400, true},
		{`"8.655"`, 0, false},
		{`8.65`, 0, false},
		{`"-1.00"`, 0, false},
		{`"1e2"`, 0, false},
		{`"8.6.5"`, 0, false},
		{`""`, 0, false},
		{`" 8.65 "`, 865, true}, // surrounding whitespace is trimmed, not refused
	} {
		got, err := parseFen(json.RawMessage(tc.in))
		if tc.ok && err != nil {
			t.Errorf("parseFen(%s) failed: %v", tc.in, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("parseFen(%s) = %d, want a refusal", tc.in, got)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("parseFen(%s) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseBpIsExact(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int32
		ok   bool
	}{
		{`"0.2500"`, 2500, true},
		{`"0.25"`, 2500, true},
		{`"0"`, 0, true},
		{`"1"`, 10000, true},
		{`"1.0000"`, 10000, true},
		{`"1.0001"`, 0, false},
		{`"0.25000"`, 0, false},
		{`0.25`, 0, false},
		{`"-0.5"`, 0, false},
		{`"70%"`, 0, false},
	} {
		got, err := parseBp(json.RawMessage(tc.in))
		if tc.ok && err != nil {
			t.Errorf("parseBp(%s) failed: %v", tc.in, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("parseBp(%s) = %d, want a refusal", tc.in, got)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("parseBp(%s) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestClassifyPredictionsIsPureAndIdempotent: the classifier must be a function of its input. It is
// called before RegisterType, so anything it did twice would be a write nobody asked for.
func TestClassifyPredictionsIsPureAndIdempotent(t *testing.T) {
	raw := predWire(nil)
	a, ca, ea := classify(t, raw, predTestRtype)
	b, cb, eb := classify(t, raw, predTestRtype)
	if len(ea) != 0 || len(eb) != 0 {
		t.Fatal("the valid payload was refused")
	}
	// Compared field by field rather than with ==: the row holds *int64 for the nullable price
	// columns, and a pointer comparison would fail on two identical values.
	if ja, jb := mustJSON(t, a), mustJSON(t, b); ja != jb {
		t.Errorf("two runs over one payload produced different set rows:\n%s\n%s", ja, jb)
	}
	if len(ca) != len(cb) {
		t.Error("two runs over one payload produced different claim counts")
	}
	if a.Errors != "" {
		t.Errorf("an accepted set carries errors: %q", a.Errors)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestClassifyPredictionsScenarioBasisAndProvenance: the two set-level fields the producer sends about
// where its scenario prices came from. scenario_basis is a closed enum the portal stores as a COLUMN;
// scenario_provenance is audit detail the portal stores verbatim and does not police.
func TestClassifyPredictionsScenarioBasisAndProvenance(t *testing.T) {
	t.Run("absent on an older producer's payload", func(t *testing.T) {
		// Neither field is required: a producer that predates them keeps working. That is what makes
		// this an additive change rather than a contract break.
		set, _, errs := classify(t, predWire(nil), predTestRtype)
		if len(errs) != 0 {
			t.Fatalf("a payload without either field was refused: %+v", errs)
		}
		if set.ScenarioBasis != "" {
			t.Errorf("scenario_basis = %q, want empty when the producer said nothing", set.ScenarioBasis)
		}
	})

	t.Run("basis is carried through", func(t *testing.T) {
		set, _, errs := classify(t, predWire(func(m map[string]any) { m["scenario_basis"] = "estimated" }), predTestRtype)
		if len(errs) != 0 {
			t.Fatalf("refused: %+v", errs)
		}
		if set.ScenarioBasis != "estimated" {
			t.Errorf("scenario_basis = %q, want estimated", set.ScenarioBasis)
		}
	})

	t.Run("provenance is accepted whatever it carries", func(t *testing.T) {
		// The point of this payload: it has a field the portal does not know about, nested inside a
		// field it does. DisallowUnknownFields applies at the payload's own level, and it must NOT
		// reach inside scenario_provenance — if it did, the gates vocabulary could not grow without a
		// portal release, which is the whole reason the object is held raw.
		set, _, errs := classify(t, predWire(func(m map[string]any) {
			m["scenario_provenance"] = map[string]any{
				"producer":      "1-4-2/目标价合成",
				"producer_node": "1788061256164",
				"extraction":    "llm",
				"gates": []any{"unique_labels", "positive_prices", "ordered_prices",
					"source_scope", "horizon_match", "symbol_match"},
				"evidence":                     map[string]any{"adoption": "quote"},
				"a_field_a_later_release_adds": map[string]any{"nested": []any{1, 2, 3}},
			}
		}), predTestRtype)
		if len(errs) != 0 {
			t.Fatalf("a provenance object with an unknown member was refused: %+v", errs)
		}
		if set.State != predStateAccepted {
			t.Errorf("state = %q, want %q", set.State, predStateAccepted)
		}
		// Stored verbatim, which is what makes the unknown member survivable: the payload column is
		// where it lives, and it holds exactly what arrived.
		if !strings.Contains(set.Payload, `"a_field_a_later_release_adds"`) {
			t.Error("the payload was not stored verbatim: the unknown provenance member is missing")
		}
		if !strings.Contains(set.Payload, `"adoption":"quote"`) {
			t.Error("the payload did not keep the adoption value it arrived with")
		}
	})

	t.Run("provenance may be null", func(t *testing.T) {
		set, _, errs := classify(t, predWire(func(m map[string]any) { m["scenario_provenance"] = nil }), predTestRtype)
		if len(errs) != 0 {
			t.Fatalf("an explicit null was refused: %+v", errs)
		}
		if set.State != predStateAccepted {
			t.Errorf("state = %q, want %q", set.State, predStateAccepted)
		}
	})

	t.Run("an empty adoption is the producer saying nothing", func(t *testing.T) {
		// An empty string is "not stated", which is accepted; it is not an unknown value, and
		// refusing it would punish a producer that filled the object in partially.
		_, _, errs := classify(t, predWire(func(m map[string]any) {
			m["scenario_provenance"] = map[string]any{"producer": "x", "evidence": map[string]any{"adoption": ""}}
		}), predTestRtype)
		if len(errs) != 0 {
			t.Fatalf("an empty adoption was refused: %+v", errs)
		}
	})

	t.Run("both adoption values are accepted", func(t *testing.T) {
		for _, v := range predAdoptions {
			_, _, errs := classify(t, predWire(func(m map[string]any) {
				m["scenario_provenance"] = map[string]any{"evidence": map[string]any{"adoption": v}}
			}), predTestRtype)
			if len(errs) != 0 {
				t.Errorf("adoption %q was refused: %+v", v, errs)
			}
		}
	})
}

// TestBaselineAsOfAcceptsAnInstant: baseline.as_of arrives as a civil date from one producer version
// and a full instant from another, and the portal takes both. This is the change that stopped the
// Dify node needing to truncate a field that is not its to normalise.
func TestBaselineAsOfAcceptsAnInstant(t *testing.T) {
	asOf := func(v any) string {
		t.Helper()
		set, _, errs := classify(t, predWire(func(m map[string]any) {
			m["baseline"].(map[string]any)["as_of"] = v
		}), predTestRtype)
		if len(errs) != 0 {
			t.Fatalf("as_of %v was refused: %+v", v, errs)
		}
		return set.EmittedBaseAsOf
	}

	// The date case is unchanged, and is returned as sent rather than re-derived.
	if got := asOf("2026-10-16"); got != "2026-10-16" {
		t.Errorf("a civil date came back as %q", got)
	}
	// An instant keeps its offset: 15:00+08:00 is 07:00Z, the same Shanghai day either way.
	if got := asOf("2026-10-09T15:00:00+08:00"); got != "2026-10-09" {
		t.Errorf("an instant with a +08:00 offset came back as %q, want 2026-10-09", got)
	}
	// The load-bearing case, and the reason truncation is forbidden: 23:30Z on the 9th is already the
	// 10th in Shanghai. Taking the first ten characters would answer 2026-10-09 and be wrong by a day.
	if got := asOf("2026-10-09T23:30:00Z"); got != "2026-10-10" {
		t.Errorf("a UTC instant after Shanghai midnight came back as %q, want 2026-10-10", got)
	}
	// The mirror case, an offset that names the PREVIOUS Shanghai day: 20:00-08:00 is 04:00Z on the 10th.
	if got := asOf("2026-10-09T20:00:00-08:00"); got != "2026-10-10" {
		t.Errorf("an instant crossing midnight the other way came back as %q, want 2026-10-10", got)
	}
}

// TestBaselineAsOfRefusesNonsense pins what is still refused, so widening the field did not widen it
// into accepting anything.
func TestBaselineAsOfRefusesNonsense(t *testing.T) {
	for _, bad := range []string{"16-10-2026", "2026-13-01", "2026-10-09T15:00", "no", "2026-10-09 15:00:00"} {
		t.Run(bad, func(t *testing.T) {
			_, _, errs := classify(t, predWire(func(m map[string]any) {
				m["baseline"].(map[string]any)["as_of"] = bad
			}), predTestRtype)
			if got := onlyReason(t, errs); got != reasonBadDate {
				t.Errorf("reason = %q, want %q", got, reasonBadDate)
			}
		})
	}
}
