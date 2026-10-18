package app

// Forecast verification: the closed vocabulary (ADR 0029).
//
// Everything here is a constant rather than a literal because the same values have to agree across
// four places: the ingest classifier, the evaluator, the group statistics, and the OpenAPI enums the
// producers read. A typo in any one of them is a set that is accepted and then never scored.

// predictionRtypes is the report types whose ingest the ledger covers (ADR 0029 §1). Coverage is
// defined over this set: every covered ingest writes a set row, in any outcome, which is what makes
// the coverage number computable from the ledger alone.
//
// It is one entry on purpose. 综合决策 (3-5) is excluded because its values come from an LLM agent and
// its claims are event probabilities, so nothing in the contract would be code-selected; the 1-x
// sub-reports are excluded because they feed 1-6-4 and scoring them would count one view twice.
var predictionRtypes = map[string]bool{"投资决策建议": true}

// predictionTestRtype is the covered report type the tests seed their fake sets with. It is a
// variable rather than a literal in each test so that the vocabulary above has exactly one
// declaration: if the covered set ever changes, the tests fail to compile instead of quietly seeding
// a type the ledger no longer covers and passing against a filter nothing exercises.
//
// Test-only, and named so. The classifier (PR 2) is what reads predictionRtypes in production; until
// it lands, this is the reader that keeps the map from being dead weight — which is why the whole
// vocabulary lives in one file rather than being scattered into the step that first needs a value.
var predictionTestRtype = predictionCoveredRtype()

// predictionCoveredRtype returns the single covered rtype, failing loudly if the set stopped being a
// singleton. Test-only.
func predictionCoveredRtype() string {
	if len(predictionRtypes) != 1 {
		panic("forecast: the covered rtype set is no longer a singleton; update predictionTestRtype")
	}
	for rt := range predictionRtypes {
		return rt
	}
	return ""
}

// predictionMigrationID is the step that declares the ledger's tables. It is 0004 rather than the
// implementation plan's 0002 because 0002 (users.last_used_version) and 0003 (user_sessions,
// login_history) were already released; the migration list is append-only.
const predictionMigrationID = "0004"

// Set states (ADR 0029 §2). Every covered ingest writes exactly one of these.
const (
	predStateAbsent   = "absent"   // a covered rtype arrived with no predictions
	predStateRejected = "rejected" // predictions arrived but failed a rule
	predStateBacktest = "backtest" // the information cutoff shows this was a run over history
	predStateAccepted = "accepted" // scored, subject to the maturity gates
)

// Baseline check (ADR 0029 §9.4). The emitted baseline is the LLM agent's quote, which code only
// selects, so it is checked against what the market actually printed.
const (
	baseCheckPending = "pending"
	baseMatch        = "match"
	baseMismatch     = "mismatch"
	baseRefused      = "refused"
)
