# Local forecast preview

This is an isolated, synthetic preview for forecast-verification v2 (plan P14). It never copies a
production database. The test-only writer uses the real v1 ingest classifier and evaluator; market
fetches are replaced with deterministic synthetic bars, a weekday calendar and explicit fault cases.
These are not captured exchange sessions or vendor evidence, and none of the reports is investment research.

## Create and start

Run from the repository root with the Go toolchain required by `go.mod`:

```bash
go test -v ./internal/app/ -run '^TestWriteForecastDemoDB$' -forecast-demo-out="$HOME/Downloads/forecast-demo/portal.db"
RP_CONFIG="$HOME/Downloads/forecast-demo/config.yaml" RP_FORECAST_DEMO_NOW=2027-06-20T07:00:00Z go run ./cmd/report-portal
```

Use a new output directory. The writer refuses existing database, config, WAL or SHM paths, including
dangling symlinks. It does not replace old previews. Both output files are owner-readable/writable only;
the generated config contains infrastructure only and listens on `127.0.0.1:8791`. Do not expose this
listener, reuse its credentials elsewhere, or point the command at a production config or database.

The verbose test output prints a fresh random password shared by `admin`, `internal`, `own`,
`external-all`, `external-own` and `none`. Keep that output private. Log in as `admin` at
`http://127.0.0.1:8791`: `/forecast` is the cohort record every reader sees (log in as each seeded account to see its scope), and `/manage/forecast` holds the evaluator's operations and the access summary. Normal server startup may bootstrap a new API token;
the generated database itself retains no ingest token. The evaluator stays **off**. Do not enable it:
the synthetic fetch implementation exists only in the test writer, not in the running server.

## Scenario map

| Receipt cohort / report label | Seeded state |
| --- | --- |
| 2026-01 / `positive-*` | Mature positive spread with corrected significance; a mixture of filled and unfilled entries |
| 2026-02 / `negative-*` | Mature negative spread with corrected significance |
| 2026-03 / `neutral-*` | Mature zero spread without significance |
| 2026-04 / `one-arm-*` | Mature long-only population; no two-arm spread |
| 2026-05 / `policy-slices-*` | Two policies, each with 36 names, producing mature policy slices |
| 2026-11 / `csi800-*` | CSI 800 benchmark frozen on receipt |
| 2027-04 / `pending-*` | Pending checkpoints, including sources that remain unavailable without consuming attempts |
| 2027-05 / `small-*` | Twelve names, below the publication threshold |
| 2027-06 / `open-*` | Open month and not-yet-reached S0 |
| 2026-01 / named fault reports | Test, backtest, absent, rejected, degraded, mismatch, off-benchmark, corporate action, halted and final refusal |
| 2026-06 / `pending-baseline` | Unresolved baseline isolated from the mature cohorts |
| 2025-12 / `pre-live` | Before the write-once ledger start, excluded from membership |
| First `positive-00` report | Body changed without changing predictions; original set and its body hash preserved |
| Same title, second edition | Duplicate prediction for the same symbol/cohort |
| Same title, manual edition | No prediction set; must not borrow the other edition's predictions |

The `own` account inherits ten attributed sets through its child OU. `external-own` has eight;
`external-all` is restricted for report access but explicitly has forecast scope `all`. `none` uses
the default `none` scope. Those accounts exercise the reader routes and the `/forecast` record page
(each sees its own scope); none of them can open the admin-only management route.

## Clock and fidelity boundaries

`RP_FORECAST_DEMO_NOW` controls only forecast statistics and read-phase annotation. It never changes
trusted receipt time, authentication, token expiry, retention or evaluator writes. The seed controls
historical receipt through a private, per-server test seam, not this environment variable. An unset
variable uses the normal clock. Empty or invalid values prevent startup; any set value prevents startup
in a non-`dev` build, before config generation or database writes.

Live chart/quote endpoints are not replayed by this variable. Synthetic symbols, dates and prices must
not be compared with live vendor quotes as though they were consistent market history. This preview
establishes deterministic statistics, stored marks and read phases. Full chart visual acceptance needs
an explicit browser-side replay of the synthetic market, or separately matched real-market evidence.
Later P16/P17/P19/P20 implementations still own their classifiers, detailed phases and visual states;
creating the seed does not claim those features are finished.

The automated inventory test generates two independent databases, compares their groups JSON at the
fixed clock, checks matrix/drawer agreement, data states, significant signs, one-arm output, policy
slices, fill output, edition isolation, OU attribution and private output permissions. Separate tests
exercise overwrite/symlink refusal and the actual linked release executable's clock refusal.
