# OU policy framework

**Status:** Draft design. No runtime or schema change is implied by this document.

**Integration review:** 2026-10-24, against main `ebd08fac`. Forecast access (PR 145),
notifications (PR 141), and the external backend connection are integrated. The former dependency
wait is complete. See [the integration review](ou-policy-review-2026-10-24.md) for verified defects,
compatibility boundaries, storage alternatives, and the proposed first implementation phase.

The owner approved the target-policy presence header on 2026-10-24 and requested all OU consumers
be unified in one iteration. The [iteration plan](ou-policy-iteration-plan.md) is the implementation
coverage ledger and dependency order. The framework remains a draft contract until implemented.

## Purpose

Organizational units are now a recurring policy boundary. They govern tenancy, run limits,
workflow availability, second-factor enrolment, report sharing and forecast access. Each feature
currently implements some combination of tree traversal, inheritance, API serialization, admin UI
state and contract tests. Repeating that work makes the semantics drift even when every individual
implementation looks reasonable.

This framework defines the shared part once. It is deliberately not a generic policy engine:
feature storage, validation, authorization and enforcement remain typed, reviewable code. The
framework owns the OU graph, the small closed set of inheritance operations, provenance, and the
cross-layer contract used to display an effective value.

The goals are:

- make a feature author choose the inheritance and failure semantics before implementation;
- use one strict representation of the OU tree for security decisions;
- keep the server authoritative for effective policy;
- preserve compile-time types and feature-specific validation;
- provide reusable admin controls and contract tests; and
- make deletion, movement, audit and restore behavior part of the feature design rather than a
  follow-up.

## Non-goals

- A dynamic registry that executes policies from string keys or reflection.
- A JSON or `meta` namespace that becomes an alternate store for structured per-OU data.
- One inheritance rule for every existing field. Existing fields intentionally have different
  semantics and must keep them until explicitly migrated.
- Hiding schema design. A new column or side table still requires owner approval, an ordered
  migration, restore behavior and a compatibility plan under ADR 0035.
- Moving authorization decisions into the browser.

## Foundational model

The OU model follows Google Workspace's organizational-unit hierarchy:

- there is one top-level OU, represented by the portal's Default OU;
- every other OU sits beneath another OU in the same tree;
- when an OU has no explicit value for a policy, it inherits from its immediate parent;
- inheritance continues one level at a time until the nearest explicit ancestor value is found;
- the top-level OU supplies the final baseline and cannot itself inherit from another OU; and
- moving an OU changes the inherited policy of that OU and its descendants immediately.

"Default" therefore means the root of the OU hierarchy and the final inheritance baseline. It does
not mean that a child with an unset value skips its parent and reads the root directly. New OU
features must use this tree model.

The backend already resolves weight, urgent eligibility, queue caps, run windows and enrolment
allowances through the parent chain. The current browser still resolves several of these directly
from Default; that is a display defect, not a backend compatibility contract to preserve. Base run
priority does retain a primary-OU-to-system fallback and must remain labelled as legacy until a
separate behavior change is approved.

ADR 0039 currently also permits non-default roots: they inherit policy through a synthetic Default
fallback without joining Default's actual subtree. Moving those roots beneath Default changes
forecast `own` populations and external backend identities. Adopting the single-root target requires
an explicit amendment and impact review; it must not be hidden inside a graph extraction or an
automatic data repair.

## Current seams

The repository already has useful pieces, but they do not yet form one contract:

- `groupChain`, `groupChainErr` and `ouIndex.chain` traverse the same graph with different error
  behavior.
- `EffectiveGroupSettings`, `SecuritySettings`, `ouIndex.effectiveScope` and
  `resolveGroupTargets` each implement policy resolution.
- `userGroupsJSON` exposes mostly stored values, while the browser's `resolveOU` reconstructs
  effective values and their inheritance state. Forecast has already added server-resolved own,
  effective and source fields through `adminGroupsWithAccess`; extend that precedent.
- `extensionViewerIdentity` independently validates a live ancestry inside a repeatable-read
  transaction and derives opaque identities from OU id plus creation time. Its actual-parent chain
  must not acquire synthetic Default membership during extraction.
- `InheritField` is a reusable presentation primitive, but each caller still builds draft state,
  clears fields to calculate the hypothetical inherited value, and assembles nullable save
  payloads itself.
- Replacement collections cannot use row count alone to distinguish "inherit" from an explicit
  empty override. In particular, the run-target save contract describes an empty list as "allow
  nothing", while `resolveGroupTargets` treats an OU with no rows as unset and continues to an
  ancestor. This ambiguity must be resolved before that feature is migrated to the framework.

## Policy taxonomy

Every OU-controlled feature must select one of these resolution modes. A new mode requires a design
amendment rather than an ad-hoc traversal in the feature.

### Nearest value

The closest explicit value on the leaf-to-root chain wins. An unset value inherits from the
immediate parent, continuing upward until a value is found. The Default OU is the top-level OU and
must have a documented concrete baseline; it does not inherit.

Examples: a scalar limit, an access scope enum, or whether members may enrol a particular factor.

### Sticky floor

Any ancestor can impose a constraint and no descendant can relax it. Resolution is a fold over the
whole chain, normally logical OR for a requirement or logical AND for an allowance.

Examples: restricted tenancy and a mandatory second factor.

The policy declaration must say which value is restrictive. Naming the mode `sticky` without that
direction is insufficient.

### Atomic bundle

The closest explicit bundle wins as one value. Members of the bundle never resolve independently.

Example: run quota `{limit, period}`. Combining a limit from one OU with a period from another
creates a policy nobody configured.

### Replacement collection

The closest configured collection replaces the ancestor's collection; collections do not union by
default. The storage model must represent all three states:

1. unset: inherit;
2. explicitly configured empty: allow or select nothing; and
3. explicitly configured non-empty: use these entries.

A feature must not infer "configured" from `len(rows) > 0`, because that collapses the first two
states. Whether a presence marker belongs in a parent row, a dedicated policy row, or another typed
shape is a schema decision and requires approval.

### Principal grants

Principal grants such as `g:<OU>` and `u:<username>` answer "who is included", not "which value does
this OU inherit". They may use the OU index for ancestry and subtree operations, but they are not a
nearest-value policy and must not be forced into that API.

The grant contract must separately state whether a grant addresses direct members, the whole
subtree, an effectively restricted tenant, or another population.

### Legacy fallback

Some run-governance fields predate the OU tree and inherit directly from the Default group or the
system default. They remain supported as an explicit legacy mode only while compatibility requires
it. New OU features must use the actual OU chain. A migration may preserve a legacy field's current
behavior first, but direct-to-Default fallback is not a valid design choice for a new OU policy.

## Authoritative OU graph

The target implementation is one immutable OU snapshot loaded in one query for an operation. It
contains only graph identity and policy-independent facts:

```go
type OUNode struct {
    ID        int64
    ParentID  int64
    IsDefault bool
}

type OUIndex struct {
    DefaultID int64
    Nodes     map[int64]OUNode
}
```

The public behavior is:

- `Chain(leaf)` returns leaf to root and includes the Default OU according to the established
  fallback rule;
- `Ancestry(leaf)` returns root to leaf when a fold needs that direction;
- `Subtree(root)` returns the root and descendants deterministically;
- a missing non-null parent is an error;
- a cycle or depth beyond the bounded maximum is an error; and
- a failed query never becomes an empty index.

The normal persisted topology is one tree rooted at the Default OU. A non-default root, missing
parent, or ambiguous Default/root relationship is invalid for new policy enforcement and must not
silently become a second inheritance baseline. Compatibility reads that currently tolerate such a
shape remain visibly lenient until the product supplies a safe validation and repair path.

The integrated forecast work supplies `ouIndex`, but it is not yet policy-independent: its row
contains forecast scope and restriction, it accepts legacy non-default roots, and duplicate Default
selection depends on row iteration order. Extract its reusable graph and transaction-aware loading
without silently strengthening topology validation in every caller at once. Preserve a separately
named legacy fallback chain while actual-ancestry consumers move to the shared graph.

Graph validation must distinguish stored topology from the effective policy chain. Actual subtree
membership, external identities and inherited policy can share node facts without sharing synthetic
edges. A missing Default, duplicate Defaults, Default with a parent, non-default root, cycle and
dangling parent should have explicit diagnostics rather than one generic empty result. The loader
must keep creation-time facts available to the identity adapter without changing identity encoding.

Lenient traversal may remain for non-security display or compatibility paths, but it must be named
as lenient at the call site. Authorization, visibility, entitlements, quotas and mandates use the
strict index and return an error to the enforcement layer.

## Typed resolution

Resolution helpers receive a chain and a typed reader; they do not construct SQL identifiers from
policy names and do not know feature storage.

The common result shape is:

```go
type OUResolution[T any] struct {
    Own       *T
    Effective T
    SourceOU  int64
    Inherited bool
}
```

For a collection, `Own` needs an outer presence signal so an explicit empty collection remains
different from no override. A feature can use a typed value such as:

```go
type Override[T any] struct {
    Configured bool
    Value      T
}
```

The framework should provide small typed operations, not a feature registry:

- nearest explicit value;
- sticky restrictive boolean;
- nearest atomic bundle;
- nearest configured replacement collection; and
- provenance lookup for admin display.

Feature code continues to own normalization, enum validation, cross-field validation, global policy
floors, administrator exemptions and the final enforcement decision.

## Failure direction

Failure behavior is part of the policy declaration and must be tested.

- Access, visibility, data disclosure and privilege grants fail closed.
- A mandatory security gate fails closed when it cannot determine whether the mandate applies.
- An admin display reports the policy as unavailable rather than silently manufacturing an
  effective value.
- A capability whose established safe behavior is permissive may preserve that behavior only when
  the feature design says why it cannot disclose data, grant privilege or bypass a mandate.
- Corrupt stored enum values resolve to the narrowest documented value and produce a diagnostic;
  they never widen access.

Returning a zero value from a store helper without an error is not acceptable for a new enforcement
path.

## API contract

The server is the source of truth for effective policy. Admin reads expose both the stored override
and the resolved result:

```json
{
  "own": null,
  "effective": "none",
  "source_ou": 1,
  "inherited": true,
  "available": true
}
```

Rules:

- `own: null` means unset/inherit only for policy types where null has that meaning.
- Replacement collections use an explicit `configured` field; `[]` is a real value.
- `source_ou` is the OU that supplied the effective value. A system fallback uses no OU and names
  its source explicitly.
- An unavailable effective value is not encoded as the permissive zero value.
- Write DTOs remain feature-specific and presence-aware. Omitting a field, setting it to null and
  sending its zero value must not collapse accidentally.
- The API returns no policy data a caller is not authorized to inspect.

Existing clients may continue receiving legacy fields during migration. The structured policy
result is additive until all callers move to it.

## Admin UI contract

The browser renders server-provided effective values and provenance. It does not reimplement OU
tree resolution for security-relevant policy.

Reusable UI pieces should cover:

- an inherited-versus-own choice that names the source and effective value;
- sticky constraints that explain which ancestor imposes the floor;
- atomic bundles edited as one control;
- replacement collections with distinct "inherit" and "override with none" states; and
- unavailable policy state that disables saving rather than guessing.

`InheritField` is the starting presentation primitive. A future `OUPolicyField` should accept the
structured API result, own the inherit/override draft state, and serialize the nullable write value.
Feature pages continue to supply the actual input control, labels, hints and validation.

All visible copy remains in the three locale files and must pass locale parity tests.

## Writes, audit and concurrency

- Normalize and validate the complete feature value before the first write.
- An atomic bundle or replacement collection is written in one transaction.
- Audit only a real change and record bounded, typed before/after values.
- A write that changes access or enforcement records the target OU and policy kind.
- The response is calculated from the committed state; the browser does not assume its submitted
  value became effective when a parent floor or global floor can alter it.
- A hypothetical preview accepts the proposed parent and overrides and resolves them with the same
  pure functions as enforcement. This replaces browser-side calculations that clear fields to guess
  what inheritance would do, and lets a move be checked against its proposed topology.
- A feature that needs optimistic concurrency must expose a revision or equivalent precondition;
  last-write-wins is not assumed by the framework.

## OU lifecycle

Every feature design must answer these cases:

- **Move:** effective descendants can change without any feature row changing. State whether one
  audit entry for the move is sufficient or whether an impact summary is required.
- **Delete:** declare whether feature rows cascade, are re-attributed, block deletion, or require a
  user choice. Rows containing OU ids must not survive for future id reuse.
- **Default OU:** declare whether it must hold a concrete value, may use a built-in fallback, or is
  excluded from the feature.
- **Unassigned account:** declare whether it resolves through the Default OU.
- **Restore:** declare how older dumps represent absence and whether `olderDumpOK` is honest.
- **Rollback:** state what enforcement disappears when code predating the feature is run.

## Contract test matrix

The framework should provide fixtures and assertions for the graph behavior, while each feature
supplies its typed read/write callbacks. At minimum every feature covers:

| Case | Required assertion |
| --- | --- |
| Default OU | Concrete fallback and provenance are correct |
| Unset leaf | Parent or documented fallback wins |
| Leaf override | Leaf value and source win |
| Multi-level chain | Nearest value or whole-chain fold is correct |
| Explicit zero/false/empty string | It remains an override when the type permits it |
| Explicit empty collection | It does not become inherit |
| Sticky floor | A descendant cannot relax it |
| Atomic bundle | All members come from the same source OU |
| Missing parent | Enforcement returns an error and fails closed |
| Cycle/depth bound | Terminates and returns an error |
| Store read failure | Does not become an empty chain or permissive value |
| OU move | Effective policy changes as designed |
| OU delete | References follow the declared lifecycle rule |
| API | Own/effective/source match runtime enforcement |
| Audit | Real changes log once; no-op saves do not log |
| Drivers | SQLite and Postgres produce the same result |
| Restore | Older and current dump behavior matches the migration declaration |

Feature enforcement tests remain mandatory. Resolver tests alone do not prove that every read,
submit, export or app-token path applies the result.

## Implementation sequence

The integration dependency is satisfied. The next work is:

1. Lock the verified save-atomicity and target-deletion defects in failing regression tests and fix
   them using existing storage. Keep forecast ownership/purge locking and identity behavior intact.
2. Extract the landed index into a shared graph with transaction-aware loading, preserving actual
   ancestry separately from the legacy Default fallback. Add validation diagnostics and typed
   resolution/provenance tests.
3. Pilot nearest/atomic resolution with run governance and quota, and sticky resolution with the
   second-factor mandate. Server admin reads and a non-persistent preview return the same results as
   enforcement; retain legacy API fields.
4. Update the OU editor to consume those server results, including inherited source and unavailable
   states. Add cross-layer tests for a root/parent/child with deliberately different values.
5. Amend ADR 0039 for the owner-directed unique-root target. Validate new writes accordingly and
   preview existing non-default roots before any operator-selected reparenting. Do not implicitly
   add their historical forecast records to Default's own population.
6. Implement the approved target collection-presence schema and its migration/restore contract, and
   distinguish inherited and explicitly empty target allow-lists in the API and editor.
7. Migrate other policies one feature at a time. Principal grants, notification audiences and
   external opaque identities retain their feature-specific meaning.

The first implementation phase intentionally requires no schema change. Any later feature storage
is proposed separately using the accompanying
[OU policy feature template](ou-policy-feature-template.md).
