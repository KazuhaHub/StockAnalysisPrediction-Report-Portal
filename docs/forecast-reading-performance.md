# Reading-server performance checkpoint

Measured 2026-10-22 on Apple M5 / darwin-arm64, Go 1.26.10; local Postgres 18.6 and
file-backed SQLite. Baseline application source: `0f0c477ffd7ce45611de56985a44a4f15634d3dd`.
After application source: `5a483f8ba39114ca6dd4b288b984098825b2be72`, plus the test-only
matrix/measurement changes. Both source trees used the identical measurement file (SHA-256
`b1e492c60e84e83d7dd34d01c6ce280f8dbebf083732e05e730f7ce57dd6ef05`).

## Method and limits

- Fresh deterministic fixture per repetition: 240 reports over 60 dates, four raw report subtabs
  per date (internal/client editions of the covered type plus two uncovered types), 41,500-byte
  bodies, and 1,120 accepted prediction sets, including 500 peer symbols in each of two cohorts.
- Internal all, internal own and external all profiles. The selected client edition is a duplicate
  whose internal sibling is the cohort member: internal all must perform the real global
  classification and counted-edition lookup; own/external must not receive that identity.
- Real route/session wrappers, security headers and gzip; ten warm-up requests followed by
  alternating 100 real and 100 prefetch requests per profile/stage. Every response is checked
  outside the timed interval for the selected body, actual summary work, scoped sibling metadata
  and timeline flags. An accidentally disabled summary cannot pass as a faster implementation.
- Three serial repetitions of each driver/source combination, without concurrent regression jobs.
  Tables show the **largest per-run p95**, using nearest-rank percentiles. Mixed p95 is computed
  from each run's 200 alternating samples, not an average of the two mode percentiles.
- This measures an in-process HTTP handler with local databases, not network/TLS latency, proxy
  queuing, concurrent evaluator load, cold starts or production p95. The deliberately compressible
  body is identical in before/after runs; this is not a general gzip-throughput benchmark.

## Before and after at card stage

All values are milliseconds. The old handler ignores the new stage setting; its archived source
is measured only at the existing card footprint.

| Driver | Profile | Before real | Before prefetch | After card real | After card prefetch |
| --- | --- | ---: | ---: | ---: | ---: |
| SQLite | Internal all | 1.021 | 1.070 | 1.133 | 1.039 |
| SQLite | Internal own | 1.047 | 0.998 | 1.218 | 1.167 |
| SQLite | External all | 1.418 | 1.326 | 0.842 | 0.814 |
| Postgres | Internal all | 2.096 | 2.117 | 4.448 | 3.845 |
| Postgres | Internal own | 2.071 | 2.065 | 6.324 | 5.480 |
| Postgres | External all | 2.121 | 2.108 | 4.337 | 3.797 |

These small local timings include scheduling/cache variation; they do not establish a universal
relative speedup or slowdown. Card-stage policy resolution adds database work on Postgres.

## Full-stage budget

| Driver | Profile | Full real p95 | Full prefetch p95 | Alternating real/prefetch p95 |
| --- | --- | ---: | ---: | ---: |
| SQLite | Internal all | 9.224 | 1.244 | 9.066 |
| SQLite | Internal own | 1.821 | 1.234 | 1.751 |
| SQLite | External all | 1.617 | 1.019 | 1.569 |
| Postgres | Internal all | 16.471 | 4.588 | 15.765 |
| Postgres | Internal own | 5.304 | 3.440 | 5.126 |
| Postgres | External all | 10.680 | 5.879 | 9.443 |

Every measured full-stage mode and mixed p95 is below the 800 ms local acceptance budget.
The test refuses an over-budget full-stage result. This is evidence for this fixture and host,
not permission to enable full in production: P20 deployment, independent review, owner sign-off
and the required week of admin full-stage production observation remain separate gates.

## Reproduce

From the feature checkout, with the local test Postgres DSN configured as described in AGENTS.md:

```bash
GOTOOLCHAIN=go1.26.10 GOWORK=off go test -v ./internal/app \
  -run '^TestForecastReadingStockBenchmark$' -forecast-reading-benchmark -count=3
GOTOOLCHAIN=go1.26.10 GOWORK=off go test -v ./internal/app \
  -run '^TestForecastReadingStockBenchmark$' -forecast-reading-benchmark \
  -forecast-reading-driver=postgres -count=3
```

To reproduce before/after, export the baseline commit into a fresh temporary directory with
`git archive`, then copy **only** `internal/app/forecast_reading_benchmark_test.go` into the same
relative path there. Verify identical file checksums and run the commands from that directory,
adding `-forecast-reading-baseline`. That flag changes assertions; it does not select old code.
The old application source must actually be compiled. Run measurements serially, especially
Postgres measurements and regressions sharing the local test database.

Normal test runs skip this opt-in timing measurement; shared CI scheduling is not treated as a
reliable local performance budget. The regular access matrix and functional tests still run.
