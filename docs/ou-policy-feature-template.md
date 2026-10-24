# OU policy feature template

Copy this document into the feature's design or ADR and replace every bracketed item. A policy is
not ready for implementation while a bracketed item remains or while two states in the value model
share the same storage representation.

## Identity

| Field | Decision |
| --- | --- |
| Feature | `[reader-facing feature name]` |
| Policy key | `[stable developer-facing name]` |
| Owner ADR/design | `[link]` |
| Enforcement points | `[every read, write, submit, export, token or background path]` |
| Applicable population | `[all accounts / restricted OUs / internal OUs / another rule]` |

## Semantics

| Question | Decision |
| --- | --- |
| Typed value | `[Go type and closed enum values, if any]` |
| Resolution mode | `[nearest / sticky floor / atomic bundle / replacement collection / principal grant / legacy fallback]` |
| Restrictive direction | `[required for sticky policies]` |
| Unset meaning | `[inherit / deny / built-in fallback / not applicable]` |
| Explicit zero value | `[meaning of false, 0, empty string or empty list]` |
| Parent inheritance | `Unset inherits from the immediate parent, continuing to the nearest explicit ancestor` |
| Top-level Default OU | `[concrete baseline; it cannot inherit]` |
| Unassigned account | `[Default OU / no policy / denied]` |
| Global floor | `[none or the global rule that bounds the OU value]` |
| Actual tree versus fallback | `[whether legacy synthetic Default fallback is used; it must not change actual subtree or identity membership]` |
| Admin behavior | `[same policy / explicit exemption and rationale]` |
| Corrupt value | `[narrow fallback plus diagnostic]` |

For a replacement collection, fill all three rows:

| State | Runtime meaning | Storage representation |
| --- | --- | --- |
| Unset | `[inherit]` | `[must be distinct]` |
| Configured empty | `[select/allow nothing]` | `[must be distinct]` |
| Configured non-empty | `[replacement entries]` | `[typed rows/value]` |

## Failure policy

| Failure | Required behavior |
| --- | --- |
| OU query fails | `[fail closed / unavailable display / justified safe fallback]` |
| Parent is missing | `[normally fail closed]` |
| Cycle or depth bound | `[normally fail closed]` |
| Feature value query fails | `[fail closed / unavailable display / justified safe fallback]` |
| Unknown enum value | `[narrowest value, never broader access]` |

Explain any permissive fallback: `[why it cannot disclose data, grant privilege or bypass a mandate]`.

## Storage proposal

Record the owner's approval before writing a schema change. An existing approval covering the
exact shape remains valid; request a new decision only when the proposed shape or scope changes.

| Question | Decision |
| --- | --- |
| Existing storage reused | `[yes/no and why its semantics match]` |
| Owner approval | `[decision/date and exactly which shape it covers, or pending]` |
| Proposed column/table | `[typed shape; no generic JSON/meta namespace]` |
| Null/presence semantics | `[exactly one meaning per representation]` |
| Constraints/indexes | `[driver-neutral declaration]` |
| Ordered migration | `[id, products, verification]` |
| Older dump behavior | `[olderDumpOK and why, or explicit refusal]` |
| Rollback consequence | `[which enforcement an older binary loses]` |

## API

Admin read shape:

```json
{
  "own": "[stored override or null]",
  "effective": "[resolved value]",
  "source_ou": 0,
  "inherited": false,
  "available": true
}
```

For collections add `configured`; never use an empty array for both unset and explicitly empty.

- Write DTO: `[presence-aware typed request]`
- Proposed-state preview: `[parent plus overrides; same pure resolver as enforcement, no writes]`
- Validation errors: `[stable error codes]`
- Authorization to inspect/edit: `[permissions]`
- Compatibility fields and removal plan: `[if migrating an existing API]`

## UI

- Section and placement: `[OU detail section]`
- Reusable control: `[inherited scalar / sticky floor / atomic bundle / collection]`
- Effective source shown as: `[OU name or system fallback]`
- Unavailable state: `[visible error and save disabled]`
- Impact preview: `[required for moves/deletes/broad access changes, or why not]`
- Locale keys: `[en-US, zh-CN, zh-TW]`

The browser consumes the server's effective value. Any client-side calculation is presentation-only
and must have parity tests against the server contract.

## Lifecycle

| Event | Decision |
| --- | --- |
| OU moved | `[effective-value impact and audit]` |
| OU deleted | `[cascade / reattribute / block / explicit choice]` |
| Parent policy changed | `[descendant impact and whether preview is required]` |
| Account moved between OUs | `[when the new policy becomes effective]` |
| Restore | `[policy and ledger behavior]` |

## Audit

- Action and target: `[existing or new typed action]`
- Before/after payload: `[bounded non-secret fields]`
- No-op behavior: `no audit row`
- Bulk descendant impact: `[summary, count or deliberate omission]`

## Test plan

Check every applicable item before implementation is complete:

- [ ] Default OU and unassigned account
- [ ] Top-level Default OU has a concrete baseline and cannot inherit
- [ ] One-level and multi-level resolution uses the nearest explicit value on the parent chain
- [ ] Leaf override
- [ ] Explicit zero/false/empty value
- [ ] Sticky parent cannot be relaxed
- [ ] Atomic fields come from one OU
- [ ] Collection unset differs from configured empty
- [ ] Missing parent, cycle and depth bound
- [ ] OU and feature-store read failures
- [ ] Corrupt stored value narrows safely
- [ ] Move and delete behavior
- [ ] Rejected create/update leaves all fields, grants and audit unchanged
- [ ] Legacy root conversion does not silently widen Default-own population or external identity grants
- [ ] Creation-time identity facts and account/OU reuse boundaries are preserved
- [ ] API own/effective/source agrees with enforcement
- [ ] Unauthorized admin reads and writes are refused
- [ ] All enforcement paths are covered
- [ ] Audit real-change and no-op behavior
- [ ] SQLite and Postgres parity
- [ ] Backup/restore across the migration boundary
- [ ] Lifecycle coverage discovers tables introduced by migrations, not just baseline tables
- [ ] Three-locale UI parity, loading and unavailable states

## Rollout

1. `[dormant storage/read path]`
2. `[admin configuration and impact preview]`
3. `[enforcement behind the intended boundary]`
4. `[pilot OU and observation period]`
5. `[wider rollout and rollback trigger]`

Do not combine a change in feature semantics with migration to the OU policy framework. First lock
the existing behavior in tests, then perform the mechanical migration, then propose behavior changes
separately.
