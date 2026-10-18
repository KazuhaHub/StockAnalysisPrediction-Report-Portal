package app

// Forecast verification: the ingest classifier (ADR 0029 §3, plan §3.2).
//
// This file is the whole of the producer contract's enforcement. It is pure: no DB, no clock of its
// own, no logging. That is not tidiness — v1Ingest runs it BEFORE RegisterType, and the store's
// SQLite pool is a single connection, so a classifier that reached for the store would either do
// work a later refusal has to undo or deadlock against the very transaction it is meant to precede.
//
// Everything below is about one question: is this payload a thing the ledger can score? A number it
// cannot resolve, a symbol no vendor prices, or a horizon already in the past all produce a claim
// that would sit on the ledger for ever looking like an unanswered question rather than a mistake.
// So they are refused — but the REPORT is not (ADR 0029 §5.5, D3): a 400 here would destroy a run
// nobody can re-run, and the ingest endpoint already refuses to create that failure for a bad
// symbol.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// predRejectReasons is the closed set of reasons a payload is refused. Stable strings, because each
// one is a test assertion and an operator's explanation of what the producer sent.
const (
	reasonNotObject      = "not_object"
	reasonTooLarge       = "too_large"
	reasonBadSchema      = "bad_schema"
	reasonBadEnum        = "bad_enum"
	reasonBadPrice       = "bad_price"
	reasonBadProbability = "bad_probability"
	reasonProbabilitySum = "probability_sum"
	reasonMissingClaim   = "missing_claim"
	reasonDuplicateKey   = "duplicate_key"
	reasonReservedKind   = "reserved_kind"
	reasonHorizon        = "horizon_not_future"
	reasonSymbolScope    = "symbol_out_of_scope"
	reasonBadDate        = "bad_date"
)

// predError is one refusal, with enough context to fix the producer without opening the payload.
type predError struct {
	Reason string `json:"reason"`
	Path   string `json:"path,omitempty"`   // e.g. "claims[3].price"
	Detail string `json:"detail,omitempty"` // what was wrong, in the producer's own terms
}

// predInput is everything the classifier is allowed to see.
type predInput struct {
	Raw     json.RawMessage
	Rtype   string
	Symbol  string    // already normalised by the caller
	MadeAt  time.Time // server receipt, UTC. Never a payload field.
	BodySHA string
	// Benchmark for a new set (D1). Passed in rather than read from settings, so the classifier stays
	// pure and a test can name the index it expects.
	BenchCode string
}

// predSetRow mirrors prediction_sets minus id and report_id, which the store assigns.
type predSetRow struct {
	Symbol     string
	Rtype      string
	Version    string
	Title      string
	ReportDate string
	MadeAt     string
	MadeOn     string
	Cohort     string
	Zone       string
	State      string
	Errors     string
	Payload    string
	Truncated  int
	PayloadSHA string
	BodySHA    string

	EmitterApp     string
	EmitterVersion string
	Calculator     string
	SchemaName     string

	PolicyVersion        string
	ProbabilityMethod    string
	ProbabilityTransform string
	DecisionStatus       string
	DecisionQuality      string
	InformationCutoff    string

	HorizonEnd string
	HorizonStd int

	EmittedBaseFen  *int64
	EmittedBaseAsOf string
	EmittedBaseSrc  string

	BenchCode string
}

// predClaimRow mirrors prediction_claims minus id and set_id.
type predClaimRow struct {
	ClaimKey string
	Kind     string
	Stance   string
	Label    string
	PriceFen *int64
	ProbBp   *int32
	WeightBp *int32
}

// ---------- the wire shape ----------

// predPrice is a price, a probability or a weight as it arrives: a JSON *string*.
//
// It is json.RawMessage rather than string on purpose. encoding/json refuses a JSON number for a
// string field at DECODE time, so a producer sending `"price": 8.65` would fail as bad_schema and the
// bad_price rule would be unreachable — the specific, actionable error would be replaced by a generic
// one. Holding the raw bytes lets parseFen/parseBp decide, and lets each say what it got.
type predPrice = json.RawMessage

type predClaimWire struct {
	Key         string    `json:"key"`
	Kind        string    `json:"kind"`
	Stance      string    `json:"stance"`
	Label       string    `json:"label"`
	Price       predPrice `json:"price"`
	PProfit     predPrice `json:"p_profit"`
	Probability predPrice `json:"probability"`
	TargetW     predPrice `json:"target_weight"`
}

type predBaselineWire struct {
	Price predPrice `json:"price"`
	AsOf  string    `json:"as_of"`
	// Source is free text from the producer (company_quote, derived_summary...). Recorded, never
	// interpreted: the portal's own baseline check is what decides whether it was right.
	Source string `json:"source"`
}

type predEmitterWire struct {
	App            string `json:"app"`
	Version        string `json:"version"`
	Calculator     string `json:"calculator"`
	DecisionSchema string `json:"decision_schema"`
}

type predPayloadWire struct {
	Schema               string           `json:"schema"`
	Emitter              predEmitterWire  `json:"emitter"`
	PolicyVersion        string           `json:"policy_version"`
	ProbabilityMethod    string           `json:"probability_method"`
	ProbabilityTransform string           `json:"probability_transform"`
	DecisionStatus       string           `json:"decision_status"`
	DecisionQuality      string           `json:"decision_quality"`
	InformationCutoff    string           `json:"information_cutoff"`
	Baseline             predBaselineWire `json:"baseline"`
	HorizonEnd           string           `json:"horizon_end"`
	Claims               []predClaimWire  `json:"claims"`
}

// ---------- the classifier ----------

// classifyPredictions decides what a payload means and returns the rows to write.
//
// Returns (nil, nil, nil) when NOTHING should be written: an uncovered rtype with no payload. The
// caller writes an `absent` set for a covered rtype that sent none, which is what makes coverage
// computable from the ledger alone (ADR 0029 §9) — the difference between "predicted nothing" and
// "was never asked" has to be a row, or a producer that stopped sending the field is invisible.
func classifyPredictions(in predInput) (*predSetRow, []predClaimRow, []predError) {
	covered := predictionCovered(in.Rtype)
	trimmed := bytes.TrimSpace(in.Raw)
	empty := len(trimmed) == 0 || string(trimmed) == "null"

	if empty {
		if !covered {
			return nil, nil, nil
		}
		return absentSet(in), nil, nil
	}

	// From here the payload exists, so a set row is written whatever happens — accepted, rejected or
	// backtest. A payload that arrives and is thrown away would be the one thing worse than a
	// rejection: a producer with a bug and no trace of it anywhere.
	base := setSkeleton(in)

	if len(in.Raw) > predPayloadMax {
		truncated := in.Raw[:predPayloadMax]
		set := *base
		set.Payload, set.Truncated = string(truncated), 1
		return &set, nil, []predError{{Reason: reasonTooLarge, Path: "",
			Detail: fmt.Sprintf("payload is %d bytes; the limit is %d", len(in.Raw), predPayloadMax)}}
	}

	var wire predPayloadWire
	if trimmed[0] != '{' {
		// The single most likely producer bug, and the one the message has to name: Dify renders an
		// object parameter from a template and passes a STRING when the rendered text is not valid
		// JSON, so the payload arrives double-encoded or as prose.
		return rejected(base, in, reasonNotObject, "", "payload is not a JSON object (got "+firstKind(trimmed)+")")
	}
	dec := json.NewDecoder(bytes.NewReader(in.Raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return rejected(base, in, reasonBadSchema, "", err.Error())
	}
	if wire.Schema != predSchemaV1 {
		return rejected(base, in, reasonBadSchema, "schema", fmt.Sprintf("schema is %q, want %q", wire.Schema, predSchemaV1))
	}

	var errs []predError
	add := func(reason, path, detail string) { errs = append(errs, predError{reason, path, detail}) }

	// Enums. Each is a closed vocabulary the producer and this build must agree on.
	if !containsStr(predDecisionStatuses, wire.DecisionStatus) {
		add(reasonBadEnum, "decision_status", fmt.Sprintf("%q is not one of %s", wire.DecisionStatus, strings.Join(predDecisionStatuses, ", ")))
	}
	if wire.DecisionQuality != "" && !containsStr(predDecisionQualities, wire.DecisionQuality) {
		add(reasonBadEnum, "decision_quality", fmt.Sprintf("%q is not one of %s", wire.DecisionQuality, strings.Join(predDecisionQualities, ", ")))
	}
	if wire.ProbabilityMethod != "" && !containsStr(predProbabilityMethods, wire.ProbabilityMethod) {
		add(reasonBadEnum, "probability_method", fmt.Sprintf("%q is not one of %s", wire.ProbabilityMethod, strings.Join(predProbabilityMethods, ", ")))
	}

	// Dates.
	madeOn, err := shanghaiDate(in.MadeAt)
	if err != nil {
		// The server's own clock, so this is a bug here rather than in the payload.
		return rejected(base, in, reasonBadDate, "", "server receipt time is unusable: "+err.Error())
	}
	if !validReportDate(wire.HorizonEnd) {
		add(reasonBadDate, "horizon_end", fmt.Sprintf("%q is not a YYYY-MM-DD date", wire.HorizonEnd))
	}
	if wire.Baseline.AsOf != "" && !validReportDate(wire.Baseline.AsOf) {
		add(reasonBadDate, "baseline.as_of", fmt.Sprintf("%q is not a YYYY-MM-DD date", wire.Baseline.AsOf))
	}
	cutoff, cutoffOK := parseRFC3339(wire.InformationCutoff)
	if wire.InformationCutoff != "" && !cutoffOK {
		add(reasonBadDate, "information_cutoff", fmt.Sprintf("%q is not an RFC3339 instant", wire.InformationCutoff))
	}

	// Baseline price.
	var baseFen *int64
	if fen, err := parseFen(wire.Baseline.Price); err != nil {
		add(reasonBadPrice, "baseline.price", err.Error())
	} else {
		baseFen = &fen
	}

	// Horizon: after receipt, and standard when it is ~12 months out.
	horizonStd := 0
	if validReportDate(wire.HorizonEnd) {
		he, _ := time.ParseInLocation("2006-01-02", wire.HorizonEnd, chinaZone)
		today, _ := time.ParseInLocation("2006-01-02", madeOn, chinaZone)
		if !he.After(today) {
			add(reasonHorizon, "horizon_end",
				fmt.Sprintf("horizon_end %s is not after the receipt date %s; a claim about a horizon that has closed is not a prediction", wire.HorizonEnd, madeOn))
		} else if days := int(he.Sub(today.AddDate(1, 0, 0)).Hours() / 24); abs(days) <= predStdHorizonTol {
			horizonStd = 1
		}
	}

	// Claims.
	claims, claimErrs := parseClaims(wire.Claims)
	errs = append(errs, claimErrs...)

	// The symbol has to be one a vendor prices daily, or every mark on this set is a question that
	// can never be answered. Checked here rather than at the report level: the report is stored
	// whatever happens, and refusal is about the claims only.
	if !predictionsSymbolIsScorable(in.Symbol) {
		add(reasonSymbolScope, "symbol", fmt.Sprintf(
			"%q has no usable daily history for scoring (A-shares on sh/sz only; Beijing codes and B-shares are out of scope)",
			in.Symbol))
	}

	if len(errs) == 0 {
		set := *base
		set.PolicyVersion = wire.PolicyVersion
		set.ProbabilityMethod = wire.ProbabilityMethod
		set.ProbabilityTransform = wire.ProbabilityTransform
		set.DecisionStatus = wire.DecisionStatus
		set.DecisionQuality = wire.DecisionQuality
		set.InformationCutoff = wire.InformationCutoff
		set.HorizonEnd = wire.HorizonEnd
		set.HorizonStd = horizonStd
		set.EmittedBaseFen = baseFen
		set.EmittedBaseAsOf = wire.Baseline.AsOf
		set.EmittedBaseSrc = wire.Baseline.Source
		set.EmitterApp, set.EmitterVersion = wire.Emitter.App, wire.Emitter.Version
		set.Calculator, set.SchemaName = wire.Emitter.Calculator, wire.Emitter.DecisionSchema

		// A run over history is stored and listed apart, never scored (plan §3.2 #13). The comparison
		// is between INSTANTS with slack, not dates: the producer defaults the cutoff to the report
		// date's end-of-day, so a run finishing after Shanghai midnight would look historical under a
		// date comparison and be filed as a backtest for ever.
		if cutoffOK && cutoff.Before(in.MadeAt.Add(-predBacktestSlack)) {
			set.State = predStateBacktest
		} else {
			set.State = predStateAccepted
		}
		return &set, claims, nil
	}

	rej := *base
	// Keep whatever is decodable even on a rejection: the emitter and horizon are what an operator
	// needs to tell which producer sent it, and a rejected row with every column blank is a row
	// nobody can act on.
	rej.EmitterApp, rej.EmitterVersion = wire.Emitter.App, wire.Emitter.Version
	rej.Calculator, rej.SchemaName = wire.Emitter.Calculator, wire.Emitter.DecisionSchema
	rej.DecisionStatus, rej.DecisionQuality = wire.DecisionStatus, wire.DecisionQuality
	rej.PolicyVersion, rej.ProbabilityMethod = wire.PolicyVersion, wire.ProbabilityMethod
	rej.HorizonEnd, rej.HorizonStd = wire.HorizonEnd, horizonStd
	rej.State = predStateRejected
	rej.Errors = marshalErrors(errs)
	return &rej, nil, errs
}

// parseClaims validates the claim list and returns the rows to insert.
func parseClaims(in []predClaimWire) ([]predClaimRow, []predError) {
	var errs []predError
	var out []predClaimRow
	seen := map[string]bool{}
	seenKind := map[string]bool{}
	scenarioBp := int32(0)
	var scenarioLabels []string

	for i, c := range in {
		path := fmt.Sprintf("claims[%d]", i)
		if c.Key == "" {
			errs = append(errs, predError{reasonBadSchema, path + ".key", "claim_key is required"})
			continue
		}
		if seen[c.Key] {
			errs = append(errs, predError{reasonDuplicateKey, path + ".key", fmt.Sprintf("claim_key %q appears more than once", c.Key)})
			continue
		}
		seen[c.Key] = true

		if !containsStr(predClaimKinds, c.Kind) {
			errs = append(errs, predError{reasonBadEnum, path + ".kind", fmt.Sprintf("%q is not one of %s", c.Kind, strings.Join(predClaimKinds, ", "))})
			continue
		}
		if c.Kind == kindStopLoss {
			// Reserved. The contract has the name because a stop is a claim worth scoring, and it is
			// refused until a code node computes one — admitting it now would let a producer invent a
			// level the analysis never committed to.
			errs = append(errs, predError{reasonReservedKind, path + ".kind",
				"stop_loss is reserved and not accepted yet: no producer computes it in code"})
			continue
		}

		if seenKind[c.Kind] && c.Kind != kindScenario {
			errs = append(errs, predError{reasonDuplicateKey, path + ".kind",
				fmt.Sprintf("a second %s claim; each kind may appear once", c.Kind)})
			continue
		}
		seenKind[c.Kind] = true

		row := predClaimRow{ClaimKey: c.Key, Kind: c.Kind}

		if c.Stance != "" {
			if !containsStr(predStances, c.Stance) {
				errs = append(errs, predError{reasonBadEnum, path + ".stance", fmt.Sprintf("%q is not one of %s", c.Stance, strings.Join(predStances, ", "))})
				continue
			}
			row.Stance = c.Stance
		}
		if c.Label != "" {
			if !containsStr(predLabels, c.Label) {
				errs = append(errs, predError{reasonBadEnum, path + ".label", fmt.Sprintf("%q is not one of %s", c.Label, strings.Join(predLabels, ", "))})
				continue
			}
			row.Label = c.Label
		}

		if len(c.Price) > 0 {
			fen, err := parseFen(c.Price)
			if err != nil {
				errs = append(errs, predError{reasonBadPrice, path + ".price", err.Error()})
				continue
			}
			row.PriceFen = &fen
		}
		if len(c.Probability) > 0 {
			bp, err := parseBp(c.Probability)
			if err != nil {
				errs = append(errs, predError{reasonBadProbability, path + ".probability", err.Error()})
				continue
			}
			row.ProbBp = &bp
			if c.Kind == kindScenario {
				scenarioBp += bp
				scenarioLabels = append(scenarioLabels, c.Label)
			}
		}
		if len(c.PProfit) > 0 {
			bp, err := parseBp(c.PProfit)
			if err != nil {
				errs = append(errs, predError{reasonBadProbability, path + ".p_profit", err.Error()})
				continue
			}
			row.ProbBp = &bp
		}
		if len(c.TargetW) > 0 {
			bp, err := parseBp(c.TargetW)
			if err != nil {
				errs = append(errs, predError{reasonBadProbability, path + ".target_weight", err.Error()})
				continue
			}
			row.WeightBp = &bp
		}
		out = append(out, row)
	}

	// Required claims. A set without these is unscorable, and a set that scores three of five claims
	// is worse than one that scores none: it looks complete in every count.
	if !seenKind[kindStance] {
		errs = append(errs, predError{reasonMissingClaim, "claims", "a stance claim is required"})
	}
	if !seenKind[kindTerminal] {
		errs = append(errs, predError{reasonMissingClaim, "claims", "a terminal_value claim is required"})
	}
	for _, label := range predLabels {
		if !containsStr(scenarioLabels, label) {
			errs = append(errs, predError{reasonMissingClaim, "claims", fmt.Sprintf("a scenario claim labelled %q is required", label)})
		}
	}
	// Probabilities are basis points, so the sum is exact integer arithmetic. A float tolerance would
	// be the one place a rounding difference between two producers is invisible.
	if len(scenarioLabels) == len(predLabels) && scenarioBp != 10000 {
		errs = append(errs, predError{reasonProbabilitySum, "claims",
			fmt.Sprintf("scenario probabilities sum to %d bp, want exactly 10000 (%.4f)", scenarioBp, float64(scenarioBp)/10000)})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].ClaimKey < out[j].ClaimKey })
	return out, errs
}

// ---------- helpers ----------

func absentSet(in predInput) *predSetRow {
	set := *setSkeleton(in)
	set.State = predStateAbsent
	return &set
}

// setSkeleton fills the columns that describe the INGEST rather than the payload: identity, the
// receipt instant and its Shanghai civil date, and the payload hash. Everything a payload supplies is
// filled in by the caller afterwards.
func setSkeleton(in predInput) *predSetRow {
	madeOn, _ := shanghaiDate(in.MadeAt) // the caller checked the clock; a zero time is the server's bug
	return &predSetRow{
		Symbol:     in.Symbol,
		Rtype:      in.Rtype,
		MadeAt:     in.MadeAt.UTC().Format(time.RFC3339),
		MadeOn:     madeOn,
		Cohort:     madeOn[:7],
		Zone:       predZone,
		Payload:    string(in.Raw),
		PayloadSHA: sha256Hex(in.Raw),
		BodySHA:    in.BodySHA,
		BenchCode:  in.BenchCode,
	}
}

// rejected builds the row for a payload that never got as far as the structured checks.
func rejected(base *predSetRow, in predInput, reason, path, detail string) (*predSetRow, []predClaimRow, []predError) {
	set := *base
	set.State = predStateRejected
	errs := []predError{{Reason: reason, Path: path, Detail: detail}}
	set.Errors = marshalErrors(errs)
	return &set, nil, errs
}

// parseFen converts a price string to integer fen without ever going through a float.
//
// A float would be wrong twice over: strconv.ParseFloat on "8.655" succeeds, and the value it returns
// cannot be compared for exactness against anything. Digit arithmetic cannot be off by one bit, and
// the reason strings name what was actually sent.
func parseFen(raw json.RawMessage) (int64, error) {
	s, err := priceString(raw)
	if err != nil {
		return 0, err
	}
	m := rePrice.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a plain decimal price with at most two decimal places "+
			"(send it as a JSON string, like \"8.65\"; negative, exponent and >2-decimal forms are refused)", s)
	}
	whole, frac := m[1], m[2]
	for len(frac) < 2 {
		frac += "0"
	}
	return int64(atoiDigits(whole))*100 + int64(atoiDigits(frac)), nil
}

// parseBp converts a probability or weight string to integer basis points (x 10000), exactly.
func parseBp(raw json.RawMessage) (int32, error) {
	s, err := priceString(raw)
	if err != nil {
		return 0, err
	}
	m := reProb.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a probability in [0,1] with at most four decimal places "+
			"(send it as a JSON string, like \"0.2500\")", s)
	}
	if m[2] != "" || s == "1" {
		return 10000, nil // the "1" branch: any number of trailing zeros is still exactly one
	}
	frac := m[1]
	for len(frac) < 4 {
		frac += "0"
	}
	return int32(atoiDigits(frac)), nil
}

// priceString unwraps a JSON string, and refuses anything else with a message that names the type it
// got. The number case is the one worth spelling out: `"price": 8.65` reaches here as raw bytes
// starting with a digit, and "expected string" alone would not tell a producer what to change.
func priceString(raw json.RawMessage) (string, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return "", fmt.Errorf("value is missing")
	}
	if t[0] != '"' {
		return "", fmt.Errorf("value is a JSON %s, not a string; quote it so no float rounds it in transit", firstKind(t))
	}
	var s string
	if err := json.Unmarshal(t, &s); err != nil {
		return "", fmt.Errorf("value is not a valid JSON string")
	}
	return strings.TrimSpace(s), nil
}

// firstKind names what a JSON value is, for error messages. Only the first byte is consulted, which
// is enough to say "number" rather than "string" and is not a parse.
func firstKind(t []byte) string {
	if len(t) == 0 {
		return "empty value"
	}
	switch c := t[0]; {
	case c == '"':
		return "string"
	case c == '{':
		return "object"
	case c == '[':
		return "array"
	case c == 't' || c == 'f':
		return "boolean"
	case c == 'n':
		return "null"
	default:
		return "number"
	}
}

func atoiDigits(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func marshalErrors(errs []predError) string {
	b, err := json.Marshal(errs)
	if err != nil {
		return ""
	}
	return string(b)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func parseRFC3339(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

var (
	// A price: up to 7 integer digits and at most 2 decimals. No sign and no exponent, so "-1.00" and
	// "1e2" are both refused rather than silently coerced.
	rePrice = regexp.MustCompile(`^(\d{1,7})(?:\.(\d{1,2}))?$`)
	// A probability: 0.xxxx, or exactly 1 (however many trailing zeros it was written with). Both
	// branches capture the fraction, so parseBp pads one field rather than special-casing a form.
	reProb = regexp.MustCompile(`^(?:0(?:\.(\d{1,4}))?|1(?:\.(0{1,4}))?)$`)
)

// chinaZone is the exchange's civil timezone, and the only one this feature reasons in.
//
// It is deliberately NOT panelLocation(): that falls back to time.Local (apiv1.go:88-97), the release
// image runs TZ=UTC, and an admin can change the panel timezone later — any of which would re-key a
// cohort and silently move sets between months. The zone name is stored on every set so a reader can
// see which one a row was decided in.
var chinaZone = mustLoadZone(predZone)

func mustLoadZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		// tzdata is present in the image; a build without it would mis-date every receipt, which is
		// worse than refusing to start.
		panic("forecast: cannot load " + name + ": " + err.Error())
	}
	return loc
}

// shanghaiDate is the Shanghai civil date of an instant, which is what cohort, made_on and the
// horizon comparison are all expressed in.
func shanghaiDate(t time.Time) (string, error) {
	if t.IsZero() {
		return "", fmt.Errorf("zero time")
	}
	return t.In(chinaZone).Format("2006-01-02"), nil
}

// predictionsSymbolIsScorable reports whether a claim about this symbol can be settled from daily
// bars (plan §3.2 #12).
//
// The rule is deliberately narrower than "the portal can quote it":
//
//   - Beijing codes (4/8/9): Tencent returns an EMPTY day array for them while its snapshot works
//     fine, and the alternate vendor's series is over a year stale — so a claim resolves never, or
//     resolves wrongly against a price from another era. Refusing at ingest is the only point at
//     which the producer can be told.
//   - B-shares: 900xxx (Shanghai) and 200xxx (Shenzhen) are priced in USD and HKD. marketPrefix maps
//     both to a plausible-looking A-share prefix, so nothing else would catch them, and every ratio
//     against an A-share benchmark would be comparing two currencies.
//   - An empty symbol is a thematic report. It is a fine report and an unscorable claim.
func predictionsSymbolIsScorable(symbol string) bool {
	if len(symbol) != 6 {
		return false
	}
	for i := 0; i < len(symbol); i++ {
		if symbol[i] < '0' || symbol[i] > '9' {
			return false
		}
	}
	switch marketPrefix(symbol) {
	case "sh":
		return !strings.HasPrefix(symbol, "900")
	case "sz":
		return !strings.HasPrefix(symbol, "200")
	default:
		return false // bj, or no prefix at all
	}
}

// predStatusValue is the value of the response's predictions_status field.
//
// "none" is not the same as "absent" and the difference is the point: `none` means the ledger does not
// cover this report type AND no payload was sent, `absent` means it is covered and the producer sent
// nothing. A producer that stopped sending the field shows up as a rising count of `absent`, which is
// the only way that failure is visible at all — the ledger would otherwise look exactly as it did
// before the feature existed.
func predStatusValue(status string, set *predSetRow) string {
	if status != "" {
		return status
	}
	if set == nil {
		return "none"
	}
	return set.State
}
