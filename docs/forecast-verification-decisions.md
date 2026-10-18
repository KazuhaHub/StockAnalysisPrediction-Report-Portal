# Forecast verification — PR 0 decisions for sign-off

Companion to [`docs/adr/0029-prediction-scoring-deferred.md`](adr/0029-prediction-scoring-deferred.md).
PR 0 is not complete until the owner answers this page. **No PR whose blocking decision is still open
starts.**

Every decision below is a choice *within* the architecture the ADR now records. The architecture
itself — an append-only ledger, server-receipt time in `Asia/Shanghai`, checkpoints at `S0`, coverage
from the ledger alone, terminal inputs behind a mature cell — is decided and is not on this list.

## 1. The nine decisions

| # | Decision | Blocks | Recommended default | Alternative | What changes if you pick the alternative |
|---|---|---|---|---|---|
| **D1** | **Benchmark.** One index for every set, or chosen by board (CSI 300 / ChiNext / STAR 50)? | PR 1 (`bench_code` is written at insert and never changes) | `sh000852` (CSI 1000) for every set | per-board index | Rows written before the change keep the old benchmark, so the ledger carries a mix and every cross-month comparison spans two baselines. The index must also have bars on every session, including days a stock is halted — that is what the checkpoint calendar is built from. |
| **D2** | **Checkpoints.** `d5` / `d20` / `d60` / `d120` / `h12`? | PR 1 (the enum), PR 5 | as listed | add/remove/replace | Dropping `d5` removes the only checkpoint that resolves fast enough to catch a broken projection early. Adding one is a new column value everywhere and a second mark row per set; existing sets never get marks for a checkpoint that did not exist when they were written. |
| **D3** | **Invalid predictions.** Keep the report, or refuse the ingest? | PR 2 | **Keep the report.** Write a `rejected` set, an audit row, and fire `predictions.rejected` | refuse the report (4xx) | Refusing loses a finished report behind a status nobody reads — the same failure the ingest endpoint already refuses to create for a bad symbol. It would also make a projection-node bug look like a Dify outage. |
| **D4** | **Membership.** The earliest `accepted` set per `(symbol, group)` decides; a degraded or `mismatch` first set excludes the symbol and a rerun never replaces it. | PR 6 | yes, as §9 of the ADR | newest set wins; or first-by-quality wins | "Newest wins" makes a member replaceable by rerunning, which is exactly the selection bias this rule exists to forbid. "Best quality wins" is worse — it selects on the outcome. |
| **D5** | **Purge.** Does the ledger outlive the report? | PR 1 | **Yes.** `report_id` → NULL; sets survive. The ledger also has **its own** retention target — the sixth in the storage console (ADR 0029 §8): off by default, 180-day floor against the reports target's 365, danger zone. It deletes a **whole month at a time**, and only when every set in it is orphaned, fully terminal and past the cutoff | ledger dies with the report | The reports floor is 365 days (ADR 0017) and `h12` resolves 12 months after a set is made, so this option purges every set on the day its long-horizon mark becomes answerable. This is one of the four rules the ADR overturns. The ledger's own retention was added after review: "the ledger must outlive its report" does not imply "the ledger can never be deleted", and the predicate — not the day count — is what makes the target safe. |
| **D7** | **3-5 重组决策.** In v1? | scope only | **Out of v1** | ask its owner for a deterministic value table first | Its values come from an LLM agent and its claims are event probabilities, so nothing in the contract would be code-selected. Note the 2026-09/10 sample found its relative calls inverted — a number from it would be worth having, but only once the values are computed rather than written. |
| **D8** | **Corporate actions.** Detect from the dividend element the unadjusted feed already carries, with the qfq ratio as fallback? | PR 5 | **Yes** — evaluate the dividend element first; use the qfq ratio only for the kinds it misses; exclude and count | score on a frozen adjusted ratio instead | The alternative needs a proportional-adjustment proof this design does not have, and it makes the display rule (ADR 0028's "never qfq") unnecessary rather than amended. The evidence for the element is real: a captured fixture's day row carries a seventh object on an ex-dividend date (`tencent_fqkline_sh601899.json`, rows at 2026-06-26 and 2026-08-21). **Whichever way this goes, the fallback is bounded to one detection-only function** (ADR 0029 §10). |
| **D9** | **Corroboration.** One vendor (Tencent) in v1, or two? | PR 5 | **One vendor** | fetch Tencent and Sina and keep `sources_disagree` | This overturns a rule of the deferred ADR. The quote stack is primary-plus-fallback, not corroboration, so on the evaluator path the second source is a different code path answering the same question — and a refusal that can never fire is worse than no refusal at all. |

Two open questions the implementation plan raises, which the ADR does not settle:

| Question | Recommendation |
|---|---|
| Should flipping `forecast_auto` write an audit entry? | **Yes** — it matches other admin settings that write `audit.t.*`, and a background loop that egresses to a third party is exactly what an operator would want a record of turning on. |
| The label text of UI strings not already named in plan §8.3 | Needs your wording. The key families are fixed; the strings are not. |

## 2. The table shape

The DDL is **ADR 0029 §7** — three tables, six indices, written out in full there so it can be
reviewed without opening the implementation plan. Please sign off on:

- `prediction_sets` — one row per covered ingest, insert-only, `report_id` nullable.
- `prediction_claims` — one row per claim, unique on `(set_id, claim_key)`.
- `prediction_marks` — one row per `accepted` set × checkpoint.
- Six indices, named in the migration's `indices` list because the runner verifies each by name.
- `restore: restoreRule{olderDumpOK: true}` — the tables are new and empty, so a dump written before
  the step restores cleanly.
- **No foreign keys**, matching the rest of the schema.

Three points worth your eye specifically:

1. **The unique index is `(report_id, payload_sha256)`.** It is the retry idempotency key. NULL
   `report_id` is distinct on both drivers, so purged sets never collide with each other.
2. **`base_check` drives the only guarded update.** The evaluator may write `s0_date`, `base_date`,
   `s0_fen` and `base_check` — and `bench_code` while it is empty — only `WHERE base_check IN
   ('pending','refused')`. Everything else on the row is insert-only.
3. **`schema_name`, not `schema`.** `schema` is non-reserved on Postgres, so this is for clarity
   beside "migration schema", not a dialect requirement.

## 3. What I found that the documents do not say

These are **collisions between the documents and the code**, reported rather than resolved, per the
working rule. One of them blocks a PR.

### 3.1 The migration id is taken — blocks PR 1

The implementation plan names the step **`migration0002`** — in §2.1, in the function it prints, and
in two of its test names (§2.5). On `main` today:

- `migration0002` = `users.last_used_version` (`migrate_steps.go:251`);
- `migration0003` = `user_sessions` + `login_history` (`migrate_steps.go:183`);
- `migrations()` (`migrate_steps.go:167`) returns `0001, 0002, 0003`.

The list is append-only and a released step is never edited, so the step must be **`0004`**. I have
written `0004` into ADR 0029 §7 and the table declarations read `migration0004`. This is not a design
choice and I am not asking you to make it — it is a factual collision, and I am flagging it because the
plan says otherwise and someone will otherwise paste it in.

### 3.2 The design branch is behind `main`, and some citations were already wrong — does not block PR 1

`docs/forecast-verification` was cut before 18 commits landed on `main`, and it is the only place the
two forecast documents and the UI mock exist; they are not on `main`. The documents cite code by
`file.go:line`, and those numbers are the branch's, not `main`'s.

I checked **all 44 code citations** in both documents, by locating each cited symbol at the commit the
documents were written against (`2d06d68`, the parent of the design commit) and on `main`:

- **31 are correct on `main`** — no action.
- **10 have moved.** The symbol exists and the reference was right when written; `main` grew above it.
- **3 were wrong when written**, and point at unrelated code at *both* commits. Re-reading the branch
  would not have caught these.

**Correct on `main` (31)** — no action:

`apiui.go:50`, `apiui.go:160`, `apiv1.go:69`, `apiv1.go:88-97`, `apiv1.go:161`, `apiv1.go:246`,
`apiv1.go:332`, `apps_api.go:121-141`, `cleanup_api.go:172`, `cleanup_api.go:195-218`,
`cleanup_store.go:217`, `geo_update.go:501`, `geo_update.go:539`, `names.go:236`, `quote.go:260`,
`quote.go:632`, `quote.go:986`, `quote.go:1121`, `quote.go:1570`, `quote_cache.go:58-66`,
`quote_cache.go:905`, `report_owner.go:91-93`, `report_owner.go:162`, `routes.go:33`, `routes.go:59`,
`server.go:232-235`, `webhook_api.go:15`, and the four test references `routes_test.go:123`,
`openapi_test.go:39`, `audit_gaps_test.go:59`, `audit_auth_test.go:357`.

**Moved since the documents were written (10)** — the symbol exists, at a new line:

| Document says | On `main` | What it points at |
|---|---|---|
| `store.go:98` | **104** | `db.SetMaxOpenConns(1)` — the single-connection argument |
| `store.go:324-327` | **328-333** | the `reports` `CREATE TABLE` |
| `store.go:1624` | **1633** | `QueryTracking` |
| `store.go:1634` | **1643** | the same function's "tracking_items has no owner_group" comment |
| `store.go:1871` | **1880** | the `sent_at=excluded.sent_at` line in `UpsertReport`'s upsert |
| `store.go:1882` | **1891** | `DeleteReport` |
| `audit.go:187` | **198** | `recordChange` |
| `audit.go:373` | **388** | `auditVocabulary` |
| `server.go:981` | **1002** | `tokenOK` |
| `migrate_steps.go:292` | **361** | `backupTableNames` |

**Wrong when written (3)** — worth their own note, because a plan is built on each:

| Document says | Actually | What it points at |
|---|---|---|
| `apiv1.go:283` | **286** | the `recordChange` call in `v1Ingest`. `apiv1.go` is byte-identical between the documents' base commit and `main`, so this cannot have drifted; it was simply wrong when written. The number that matters beside it — `apiv1.go:246`, the `RegisterType` call the classifier must precede — is correct. |
| `apiv1.go:245-307` | **246-307** | cited as "the order of today's write path". Right in substance — it spans `RegisterType` at 246 through the `SetTracking` call at 307 — but line 245 is a closing brace, so the range starts one line early. Inside the function's `161-331`. |
| `apiui.go:596` | **614** | the `repKind(m) == selKind` comparison in `apiStock`. The plan builds a rule on it: a member row must carry the report's top-level `kind`, because a wrong one silently falls back to the first kind instead of erroring. |

I did **not** rewrite the documents, because the working rule says to bring a mismatch to you rather
than pick a fix. Say the word and I will re-anchor every citation to `main` in one pass, and I would
then bring both documents onto `main` alongside the ADR so the three travel together.

### 3.3 The qfq amendment is a preference, not a certainty

D8's first step is to check whether the dividend element covers all three kinds (cash dividend, 送转,
配股). I confirmed the element **exists** — a captured day row carries a seventh object on an
ex-dividend date — but I did **not** confirm it describes all three kinds, because that needs fixtures
for a 送转 and a 配股, and there are none in `internal/app/testdata/quote/`. PR 5 captures them before
writing any qfq code. If the element covers all three, ADR 0028 needs no amendment at all and §10's
exception is never used.

## 4. What PR 0 delivered

- **ADR 0029 rewritten** — status Accepted, the four overturned rules listed with reasons, the nine
  decisions recorded, and the DDL written out in full.
- **ADR 0028 amended** at §3, where the "never qfq" promise is made, bounding the exception to one
  detection-only function.
- **This decision list**, for sign-off.
- Branch `docs/forecast-adr-0029`, off `main`, no PR opened. Commit messages carry no attribution.

## 5. What I need back

1. **D1, D2, D3, D4, D5, D7, D8, D9** — accept the recommended default, or say which way to go.
2. **Sign-off on the table shape** (ADR 0029 §7), or the changes you want.
3. **A yes/no on the `0004` renumber** (§3.1), so PR 1 can start.
4. **Whether I should fix the drifted line references** in the design and implementation documents
   (§3.2), and whether those two documents should be brought onto `main` alongside the ADR.
