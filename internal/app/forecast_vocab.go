package app

import "time"

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

// predictionCovered reports whether the ledger covers this report type's ingest (ADR 0029 §1). The
// ingest classifier is the reader.
func predictionCovered(rtype string) bool { return predictionRtypes[rtype] }

// Contract constants (ADR 0029 §3, plan §1).
const (
	predSchemaV1      = "predictions.v1"
	predPayloadMax    = 64 << 10        // bytes
	predZone          = "Asia/Shanghai" // the exchange's civil timezone, never the panel's
	predStdHorizonTol = 7               // days either side of 12 months that still counts as standard
)

// predBacktestSlack is how far before receipt an information cutoff has to be before the run is
// treated as a look at history rather than a forecast. Instants, not dates, and with slack: the
// producer defaults the cutoff to the report date's end of day, so a run finishing after Shanghai
// midnight, or dated the previous trading day, would otherwise be filed as a backtest for ever.
const predBacktestSlack = 72 * time.Hour

// The closed vocabularies the producer and this build have to agree on. Each is an OpenAPI enum too.
var (
	// The only decision statuses that carry predictions. research_price_advice means the producer had
	// no current price, so there is no baseline to score from and nothing to promise (ADR 0029 §5.3).
	predDecisionStatuses   = []string{"eligible", "no_trade"}
	predDecisionQualities  = []string{"normal", "degraded", "repaired"}
	predProbabilityMethods = []string{
		"approved_valuation_source", "approved_policy_default", "subjective_committee", "unavailable",
	}
	predClaimKinds = []string{"stance", "terminal_value", "scenario", "entry_price", "stop_loss"}
	predStances    = []string{"long", "no_position"}
	predLabels     = []string{"bull", "base", "bear"}

	// scenario_basis says how the scenario prices came to be, in statistical terms rather than in
	// terms of the tool that produced them. "estimated" means they were projected from a model rather
	// than reported, which is the only thing true of every set today: an agent in 1-4-2 composes the
	// target prices and v3valuation restates them, so nothing a code node computed them from is
	// recorded anywhere else. The vocabulary is closed and short because it is a grouping key — the
	// statistics will filter and exclude on it the way they do on probability_method. It is open to
	// more values, and adding one is a change here, in openapi.json, and on the producer.
	predScenarioBases = []string{"estimated"}

	// evidence.adoption is the one part of scenario_provenance with a closed vocabulary, and the only
	// part with discriminating power. The gate that admits a scenario set accepts EITHER a quote from
	// the report body OR a source locator, so a set adopted on a quote and a set adopted on nothing
	// but a section heading both pass it; this field is what lets a reader tell them apart. The
	// earlier name "citation" was withdrawn for claiming more than the gate checks: it covered both
	// branches, so counting it would have counted locator-only sets as cited.
	predAdoptions = []string{"quote", "locator"}
)

// Claim kinds the classifier reads by name, because a required claim is identified by its key.
const (
	kindStance   = "stance"
	kindTerminal = "terminal_value"
	kindScenario = "scenario"
	kindEntry    = "entry_price"
	// kindStopLoss was reserved until 2026-10-29, when v3risk was found to compute the level in
	// code (trade_plan.stop_loss.price); see the ADR 0029 amendment of that date.
	kindStopLoss = "stop_loss"
)

// predictionMigrationID is the step that declares the ledger's tables. It is 0004 rather than the
// implementation plan's 0002 because 0002 (users.last_used_version) and 0003 (user_sessions,
// login_history) were already released; the migration list is append-only.
const predictionMigrationID = "0004"

// predColScenarioBasis is the ledger's one contract column, as opposed to the fields echoed into the
// payload. It is a column because it is a grouping key: the statistics filter and exclude on it the
// way they do on probability_method and decision_quality, and a JSON blob cannot be indexed or
// compared in SQL. Everything else the producer sends about provenance is audit detail and stays in
// the payload, which is stored verbatim.
const predColScenarioBasis = "scenario_basis"

// Set states (ADR 0029 §2). Every covered ingest writes exactly one of these.
const (
	predStateAbsent   = "absent"   // a covered rtype arrived with no predictions
	predStateRejected = "rejected" // predictions arrived but failed a rule
	predStateBacktest = "backtest" // the information cutoff shows this was a run over history
	predStateAccepted = "accepted" // scored, subject to the maturity gates
	// predStateTest is a run the producer declared as a test (V24): stored and listed apart, no marks,
	// never scored. Absent run_mode means production.
	predStateTest = "test"
)

// predRunModes is the closed vocabulary of the run_mode wire field (V24). Absent means production.
var predRunModes = []string{"production", "test"}

// Baseline check (ADR 0029 §9.4). The emitted baseline is the LLM agent's quote, which code only
// selects, so it is checked against what the market actually printed.
const (
	baseCheckPending = "pending"
	baseMatch        = "match"
	baseMismatch     = "mismatch"
	baseRefused      = "refused"
	// baseRefusedFinal is the terminal form: no start bar could ever be resolved, so the set can
	// never be scored (P1). It is also what makes the cell stop looking pending.
	baseRefusedFinal = "refused_final"
)

// ForecastBench is the index new sets are benchmarked against (D1), read at ingest and frozen onto
// each row. It is a setting rather than a constant because the owner may switch it, and it is FROZEN
// because a set scored against one index must not change index when an admin picks another: the
// ledger would then hold a mix and every cross-month comparison would span two baselines.
//
// Default is CSI 1000: the sample this feature scores is overwhelmingly small and mid cap, and
// benchmarking a 30bn-float company against the CSI 300 would flatter it by construction.
func (s *Store) ForecastBench() string {
	return s.GetSetting("forecast_bench", defaultForecastBench)
}

const defaultForecastBench = "sh000852"

// predTestRtype is the covered report type the ledger tests seed their fake sets with. It reads the
// vocabulary rather than repeating the literal, so a change to the covered set fails a test to
// compile instead of quietly seeding a type the ledger no longer covers.
var predTestRtype = func() string {
	for rt := range predictionRtypes {
		return rt
	}
	panic("forecast: predictionRtypes is empty")
}()

// Checkpoints (D2). Order matters: it is the column order everywhere, and d5 is the only one that
// resolves fast enough to catch a broken producer early.
var predCheckpoints = []string{"d5", "d20", "d60", "d120", "h12"}

// predCheckpointSessions is how many sessions after S0 each session-based checkpoint settles on.
// h12 is absent on purpose: it is decided by the calendar, not by a count.
var predCheckpointSessions = map[string]int{"d5": 5, "d20": 20, "d60": 60, "d120": 120}

// predMarkCheckpoints is the checkpoints an accepted set gets marks for. h12 is added only when the
// horizon is standard (plan §2.2): a caller-supplied six-month or two-year horizon must never mix
// into the twelve-month column.
func predMarkCheckpoints(horizonStd bool) []string {
	out := make([]string, 0, len(predCheckpoints))
	for _, cp := range predCheckpoints {
		if cp == "h12" && !horizonStd {
			continue
		}
		out = append(out, cp)
	}
	return out
}

// Mark status. refused is retryable; refused_final is terminal, which is what lets a mature cell
// never change (ADR 0029 §9).
const (
	markPending      = "pending"
	markRefused      = "refused"
	markRefusedFinal = "refused_final"
	markResolved     = "resolved"
)
