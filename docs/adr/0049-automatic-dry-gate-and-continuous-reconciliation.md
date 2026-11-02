# ADR 0049 — Automatic dry-run gate and continuous reconciliation

**Status: Proposed.** 2026-11-02. Amends [ADR 0037](0037-forecast-verification-operations.md)
("Switching on is gated by what has been exercised") and the `awaiting_gate` runbook entry in
[forecast-verification-operations.md](../forecast-verification-operations.md). Bounded by
[ADR 0041](0041-ledger-grade-market-data.md) §3 and §6 and by
[ADR 0043](0043-public-quote-request-scheduling.md), none of which this decision relaxes.

## Context

Mode 1 writes a scored mark for a benchmark only after an admin has recorded a dry-run check for
that benchmark code (`forecast_dry_gate`, readiness item 4). The check is done by hand. An admin
compares one would-be `d5` with the published closes, by eye:

- the stock's close on `base_date` and on the due session;
- the index's close on `s0_date` and on the due session.

The admin then types a note saying what was compared.

That check has three weaknesses.

1. **It happens once.** After the gate is recorded, nothing compares the writer's bodies with
   anything again. A vendor that changes its adjustment basis, its index identity or its rounding
   in March writes March's marks unnoticed. Marks are immutable, so the error is permanent.
2. **It waits for a person.** A benchmark scheduled for a new cohort (CSI 800 `sh000906` from
   2026-11) sits in `awaiting_gate` until someone notices and checks it. Every set frozen with that
   code waits as well.
3. **What was compared is free text.** The note is the only evidence. Nobody can tell later which
   sessions, which closes or which reference were compared.

The owner asked for the check to run without a person.

P25 (`forecast_shadow_runtime.go`, `quote_shadow_compare.go`) provides a comparison function and a
budgeted fetch path for a `settled_shadow` peer. It is not a per-mark reconciliation, for four
reasons:

- it samples whole fetched series before any plan exists, and its anchors run from the first
  fetched bar to today;
- it takes its two slots in offer order, and the index series is offered first;
- it runs only on a completed pass;
- it is switched on by `forecast_fallback = shadow`.

This decision reuses its comparison function and its source plumbing. It does not reuse its
sampling or its switch.

## Decision

### 1. The gate can be earned by deterministic code, never by a model

A benchmark code's gate can be recorded by the evaluator once a bounded set of per-mark
reconciliations agree exactly (§3). An integer comparison decides it, with fixed thresholds and a
recorded list of what was compared. No language model takes part, whether a Dify workflow or any
other agent. The reasons:

- The question is yes or no with an exact answer: two closes are equal or they are not. A model
  adds nondeterminism to a question that has none, and it can judge a 0.01 difference "close
  enough", which is the error the gate exists to catch.
- Whatever a model reports as "the exchange's close" has no provenance the portal can check. Its
  retrieval becomes a third, unaudited source.
- The Dify sandbox cannot reliably reach private data endpoints (the SSRF refusals in the batch
  history), and a workflow run is outside the evaluator's pass, its budget and its audit.

The hand check remains valid. An admin can record a gate by hand at any time, for example when the
automatic reference is unavailable. Either kind satisfies readiness item 4.

**What a gate means does not change.** A gate, automatic or by hand, says that the writer's bodies
were reconciled for a sample of marks and agreed. It does not certify the bodies written later:
every pass fetches afresh, and the hand check never covered later bodies either. Later bodies are
what §4's continuous reconciliation covers.

### 2. A reconciliation stage, per would-be mark, with two legs

A new stage in the evaluator pass, `reconcile`, takes plan entries (`settle` and `baseline` kinds),
not fetched series. Each reconciled entry is identified by `(set_id, mark)` and has two legs. The
two legs use the same values the planner used.

| Leg | Writer (Tier 1) | Reference | Compared |
|---|---|---|---|
| Index | Tencent | **The index issuer's own published close** (China Securities Index, for every code in the current allowlist) | The index closes on `s0_date` and on the due session, exactly as the planner read them for `bench_ret_bp` |
| Stock | Tencent | The `settled_shadow` peer (Sina), through P25's comparison function | Every bar from `base_date` to the due session (OHLC and the date set) |

**The index leg is real corroboration.** The issuer is the source of truth for an index, not a
mirror. Its endpoint is **unmeasured**. Before any code relies on it, a P24-style capture must
establish:

- reachability from the production egress;
- the response shape;
- the number of decimals in a close;
- holiday and half-day behaviour;
- that the code returned is the code asked for.

The capture must also measure Tencent's index precision. Values are compared exactly, with no
rounding. If the two sources publish a different number of decimals, the capture fails, and §6
step 1 stops the work. Until the capture is accepted, the index leg is `unavailable` (§3), so no
automatic gate can be earned.

**The stock leg is not corroboration.** Two public mirrors agreeing is not corroboration (ADR 0041
§3), and that remains true. It is still the evidence a person produces when checking "the published
close" in a market app. It catches:

- a parser fault;
- a unit or rounding fault;
- a shifted date;
- an adjusted series served as unadjusted, which shows up as an OHLC difference against Sina's
  unadjusted bars.

It does **not** reconcile the corporate-action flag. `corp_action` comes from Tencent's separate
adjustment-ratio read (ADR 0029 §10, `fetchTencentAdjustRatio`), which no reference covers. The hand
check did not cover it either. This decision claims nothing for it.

This decision admits no new writer, grants nothing and adds no provenance. Tier 2 admission (ADR
0041 §6) is unchanged.

**Budget (ADR 0043).** The stage has its own limits, separate from P25's diagnostics, whose
validator keeps its present caps:

- at most **2 reconciled entries per pass**;
- each entry makes at most one issuer read and one Sina range read (a snapshot plus a history
  request);
- 6 HTTP attempts per pass in total.

The issuer gets its own vendor priority (below the writer's), its own per-pass breaker and its own
cross-pass cooldown, on the same terms as ADR 0037's source cooldown. Slots are allocated in this
order:

1. codes with no gate, round-robin across codes, oldest due entry first;
2. continuous checks for gated codes, round-robin across codes.

The stage runs after the planner and before the pass deadline, for whatever entries were planned.
A pass that ran out of time records `skipped` for each entry it could not reconcile, rather than
running nothing.

### 3. What earns a gate

Each leg of a reconciled entry has exactly one outcome:

- `agree`: the reference returned a final body for every compared date, and every compared value is
  equal.
- `disagree`: the reference returned a final body, its identity matches, and at least one compared
  value differs.
- `unavailable`: anything else, including a timeout, a refusal, a budget limit, a missing date, a
  body that is not final, or an identity mismatch in the response.

An entry `agrees` only if both legs agree. `unavailable` and `skipped` neither earn nor block. They
are counted and shown, and they never turn into `agree`.

The evaluator records an automatic gate for benchmark code `B` when the stored evidence for `B`
holds at least:

- **3 agreeing entries**, from distinct sets **on distinct stocks**,
- covering **at least 2 distinct due sessions**,
- with no suspension in force (§4).

The thresholds are Go constants, not settings. Changing them changes what a gate means, and that
goes through review.

**Evidence storage.** Evidence for codes without a gate lives in a new meta value,
`forecast_gate_evidence`: `{code: [{set_id, mark, symbol, session, legs}]}`, at most 10 entries per
code, newest kept. It is shown on the operations card. A restore resets it together with the gates.

**Effect.** A gate earned in a pass takes effect from the next pass. Nothing is written for `B` in
the pass that earned it. The gate record keeps its present shape and gains two fields inside the
existing JSON value:

- `kind`: `"auto"`, or `"hand"`, which is also what an absent field means;
- `evidence`: the entries that earned it.

`by` is `system`. The gate is written first and then audited as `forecast.dry_gate` with actor
`system`, the same order as the hand path (`forecast_admin_api.go`). This decision does not make
either path transactional.

The stage runs in dry mode (during an operator's "Evaluate now") and in mode 1 (during the ticker).
In mode 1 a code in `awaiting_gate` can therefore earn its gate without a person, which is the CSI
800 case.

### 4. Reconciliation continues after the gate, and a disagreement suspends writes

Once `B` has a gate of either kind, mode 1 keeps reconciling `B`'s entries with the slots left after
gate-earning work (§2). This is detection, not prevention. Marks are written in the same pass, as
today, because reconciling every mark before its write would exceed ADR 0043's budget.

If continuous reconciliation has run zero entries on 5 consecutive working days while marks were
written, the watchdog emits `lagging` with the reason `reconcile_starved`. A check that silently
stopped running must not look like a check that keeps agreeing.

**A `disagree` on either leg, in any mode, suspends writes for every code**, gated or not:

- The writer is one source, and a stock-leg fault is not specific to one benchmark.
- The suspension is recorded once, in `forecast_gate_suspended` =
  `{at, code, set_id, mark, leg, ours, theirs}`, and audited as `forecast.gate.suspend` with actor
  `system`.
- It emits a new alert reason, `gate_disagreement`. Its de-duplication key is the suspension's `at`,
  not an interval, so every distinct suspension alerts once.
- Readiness item 4 fails with `gate_suspended`, naming the leg and the code.

**Enforcement does not depend on `forecast_gate_auto`.** A suspension stays in force with the switch
off. Turning the switch off neither clears it nor hides it.

**Only an explicit resume clears it.** The resume is a dedicated admin action:
`POST /api/admin/forecast {"gate_resume": {"code": "<the suspended code>", "note": "..."}}`. The code
must equal `forecast_gate_suspended.code`, and the note must be non-empty. The resume:

- records a hand gate for that code;
- clears the suspension;
- is audited as `forecast.gate.resume`.

A hand gate recorded for any other code does not clear it. The automatic path can never clear it,
so a reference that flaps cannot switch writes off and on. Other codes' existing gates come back
with the resume. Their evidence was not contradicted.

**What a suspension does to marks.** A suspended pass plans as dry does and spends no attempt, so it
behaves like a source outage (ADR 0041 §2). The finality clock (`predRetryDeadlineTicks` sessions
past due) keeps running, exactly as it does during an outage. A mark that had failed attempts
before the suspension can therefore become final on its first mark-specific refusal after the
resume. This is accepted for the same reason it is accepted for outages: the clock exists so that
refusals cannot hold a cell open indefinitely.

Marks written before the disagreement are not rewritten by this decision. Whether they need
correcting is the judgement of whoever resolves the suspension, under ADR 0037's finality rules.

### 5. Switch, storage, restore and rollback

- **The switch.** A meta setting, `forecast_gate_auto`, is `off` or `on` and defaults to `off`. An
  admin changes it on the forecast operations card, and the change is audited. It controls whether
  the `reconcile` stage runs and whether gates can be earned. It does not control suspension
  enforcement (§4). It is independent of `forecast_fallback`: P25's diagnostics keep their own
  switch, and turning them off does not stop reconciliation.
- **Storage.** No table, column or migration is added. The new state is three meta values
  (`forecast_gate_auto`, `forecast_gate_evidence`, `forecast_gate_suspended`) plus the two new
  fields inside the existing `forecast_dry_gate` value.
- **Restore.** `applyRestoreEvaluator`'s reset list gains `forecast_gate_evidence` and
  `forecast_gate_suspended`. A dump's evidence must never earn a gate on another database.
  `forecast_gate_auto` takes the lower of the dump's and the live value. Mode 1 is still capped at
  dry when the gates are empty, as today.
- **Rollback.** An older binary reads an automatic gate as a gate and ignores the suspension. A
  rollback while suspended therefore resumes writes. An older binary's hand save also rewrites the
  gate map through its three-field struct, which strips `kind` and `evidence` from every automatic
  gate. The release note says both. Neither loses data that decides a score.

New identifiers that need entries:

- the audit actions `forecast.gate.suspend`, `forecast.gate.resume` and `forecast.gate.auto` (the switch change), in the
  audit category list (`audit.go`);
- the readiness code `gate_suspended`, the watchdog reason `reconcile_starved` and the alert reason
  `gate_disagreement`, in every locale.

### 6. Out of scope

- **`p_profit` calibration.** Calibration is a metric at `h12`, and the first `h12` cell matures
  around 2027-11, so there is nothing to automate before then. When there is, measuring can be
  automatic, but adopting a `probability_transform` stays a human decision. A system that fits,
  applies and then scores its own transform is grading itself, which is why the design excludes
  transformed sets from the calibration metric.
- **Writers.** No source gains a `settled` grant. The issuer endpoint is a reference only.
- **The corporate-action ratio** (§2).

## Acceptance tests

Each test states which way it fails. "Fails closed" means no write happens that the gate would
otherwise have allowed.

1. `TestAutoGateThresholds`: 2 agreeing entries record no gate; neither do 3 entries on one stock,
   nor 3 entries on one due session. 3 entries on 3 stocks over 2 sessions record a gate. Fails
   closed.
2. `TestAutoGateUnavailableAndSkippedNeverCount`: entries with either leg `unavailable` or `skipped`
   add nothing to the evidence count. Fails closed.
3. `TestAutoGateSwitchOff`: with `forecast_gate_auto=off`, the `reconcile` stage issues no issuer or
   peer call and records no gate. Fails closed.
4. `TestReconcileIndependentOfFallback`: with the switch on and `forecast_fallback=off`, the stage
   still runs. Fails closed.
5. `TestAutoGateTakesEffectNextPass`: the pass that earns `B`'s gate writes nothing for `B`, and the
   next pass does. Fails closed.
6. `TestAutoGateRecordCarriesEvidence`: the gate has `kind=auto`, `by=system` and the earning entries,
   and the `forecast.dry_gate` audit row carries them.
7. `TestReconcileComparesPlannerValues`: the index leg compares the planner's own `s0_date` and
   due-session closes, and the stock leg compares `base_date` through the due session only. A
   difference on a bar after the due session is ignored. A 0.01-point difference on either index
   anchor is `disagree`. An issuer body whose code differs from the requested code is
   `unavailable`. Fails closed.
8. `TestReconcileSlotsPreferUngatedCodes`: with two codes awaiting a gate and one gated code, the
   two slots go to the ungated codes, round-robin across passes. No code is starved over 4 passes.
9. `TestReconcileRecordsSkippedOnDeadline`: a pass that hits its deadline records `skipped` for its
   unreconciled entries. 5 working days of written marks with zero reconciled entries emits
   `reconcile_starved`.
10. `TestDisagreeSuspendsEverything`: a stock-leg `disagree` on `B`, in dry mode and in mode 1, puts
    every code into `awaiting_gate` from the next pass, records the suspension once, alerts once and
    fails readiness item 4 with `gate_suspended`. A second, later suspension alerts again. Fails
    closed.
11. `TestSuspensionSurvivesSwitchOff`: turning `forecast_gate_auto` off while suspended keeps every
    write off and the readiness item failing. Fails closed.
12. `TestSuspensionClearsOnlyByResumeOfTheSuspendedCode`: more agreeing automatic evidence does not
    clear it; a hand gate on another code does not clear it; `gate_resume` on another code is
    refused with 400. `gate_resume` on the suspended code clears it, records a hand gate and audits
    `forecast.gate.resume`. Fails closed.
13. `TestSuspensionSpendsNoAttempt`: marks due during a suspension keep their attempt count and stay
    `pending`, and settle after the resume.
14. `TestRestoreResetsEvidenceAndSuspension`: after a restore, the evidence, the suspension and the
    gates are empty, and the switch is the lower of the two values. Fails closed.
15. `TestHandGateStillSatisfiesReadiness`: with the switch on or off, a hand gate satisfies item 4
    exactly as today. This guards against regression.

Tests 2, 7 and 10 run on recorded fixtures. The issuer capture (§2) must be accepted before those
fixtures can stand for the live endpoint.

## Rollout

1. Capture and measure the issuer endpoint and Tencent's index precision, and record the result in
   the implementation log. If the capture fails, stop: the automatic gate is not built, and this ADR
   is amended to say so.
2. Implement behind `forecast_gate_auto=off` with the tests above, and run `scripts/e2e_check.py`.
3. After deployment, the owner switches it on. The next benchmark scheduled for a new cohort is the
   first live use.

CSI 800's gate for cohort 2026-11 falls before this can ship, so it is checked by hand.

The `awaiting_gate` runbook entry is corrected in the same change: the index is compared on
`s0_date`, not `base_date` (`forecast_settle.go`).
