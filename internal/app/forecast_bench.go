package app

// Benchmark identification (ADR 0029 §6, plan §3.1 F01/F02).
//
// A set is scored against the index it was frozen with, and that index is named by a string the
// owner typed ("sh000852"). Turning that string into a vendor request is not a job for marketPrefix:
// marketPrefix maps every 0xxxxx code to Shenzhen, so it files CSI 1000 (sh000852, a Shanghai index)
// under sz000852 — a Shenzhen STOCK with the same digits. Fetching that would score a whole cohort
// against an unrelated company's price.
//
// So the mapping is an explicit allowlist: each benchmark a set may be frozen with is a row here,
// with its market and vendor symbol spelled out, and each has a committed Tencent fixture under
// testdata/quote/. An empty or unknown code is refused, never guessed.

import (
	"sort"
	"strings"
)

// benchSpec is one allowed benchmark.
type benchSpec struct {
	Code   string // the stored, prefixed form: "sh000852"
	Market string // "sh" — the exchange that PUBLISHES the index, never inferred from the digits
	Sym    string // "000852" — the vendor's symbol key
}

// forecastBenchAllowlist is the closed list of benchmarks a set may be frozen with (V8). Every entry
// has a committed Tencent daily fixture under testdata/quote/tencent_fqkline_range_<code>.json,
// captured by a read-only probe, so the routing can be tested without a network.
var forecastBenchAllowlist = map[string]benchSpec{
	"sh000300": {Code: "sh000300", Market: "sh", Sym: "000300"}, // CSI 300
	"sh000905": {Code: "sh000905", Market: "sh", Sym: "000905"}, // CSI 500
	"sh000852": {Code: "sh000852", Market: "sh", Sym: "000852"}, // CSI 1000 (the October cohort)
	"sh000906": {Code: "sh000906", Market: "sh", Sym: "000906"}, // CSI 800 (from the 2026-11 cohort)
}

// benchSpecFor resolves a frozen benchmark code to its vendor request. The match is an exact key
// lookup after trimming whitespace: there is no prefix inference and no numeric parsing, so a code
// that is not a row above is refused rather than routed somewhere plausible.
func benchSpecFor(code string) (benchSpec, bool) {
	spec, ok := forecastBenchAllowlist[strings.TrimSpace(code)]
	return spec, ok
}

// forecastBenchCodes returns the allowed codes, sorted, for the admin GET and the settings Select.
func forecastBenchCodes() []string {
	out := make([]string, 0, len(forecastBenchAllowlist))
	for code := range forecastBenchAllowlist {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}
