# Proposal: background credential use

Status: accepted 2026-11-17; implemented for background broker calls (section
10). Depends on [174](174-background-broker-egress.md); uses the target digest
from [175](175-reviewed-multi-origin-and-private-network.md) once that lands
(section 10 says what stands in for it now). Index and order: doc 174.

## 1. Problem and evidence

- The H07 service resource set omits `secrets.use`
  (`internal/app/app_platform_service_identity.go:25-32`). Doc
  [151](151-background-service-identity.md):43 records this as deliberate
  until "an explicit service-bound credential decision"; doc
  [153](153-app-task-schema-proposal.md):100 forbids it for tasks.
- Binding use exists only on the viewer path: `preparePlatformNetworkIntent`
  resolves the binding inside the current-viewer transaction
  (`internal/app/app_platform_network_request.go:198`), after the session,
  launch binding and grants are rebuilt.
- The binding policy `portal.secret-binding-policy/1-dev.1` holds only the
  operation and the whole network-policy digest
  (`internal/app/app_platform_secret_policy.go:9-21`); nothing says who may
  use it.
- Injection is already value-free for the app: Portal decrypts inside the
  request transaction and places exactly one `Authorization: Bearer` or
  `X-API-Key` header in the signed command
  (`app_platform_secret_bindings.go:35-50`, `:458-477`;
  `internal/apprunner/control/network_http.go:44-46`); the runner refuses any
  other header (`internal/apprunner/broker/http.go:96-103`) and withholds a
  response that echoes the value (`http.go:193-200`, `:210-221`).

Status's Dify, DeepSeek and authenticated HTTP probes send a Bearer key on
every background check (Apps `apps/status/service/internal/status/probes.go:287-289`),
and its draft descriptor already requires `secrets.use`
(Apps `apps/status/app.v2.json`). Without this proposal they can only run while
an administrator has the page open.

## 2. Threat model

| Threat | Control |
| --- | --- |
| A foreground-reviewed credential silently becomes usable with nobody present | Use by the service principal is a separate, explicit binding attribute; v1 bindings are viewer-only |
| Backend obtains the secret | The value exists only in Portal's transaction memory and the signed runner command; the broker reply, logs, journal and session command never contain it |
| Credential sent to a different destination | The binding is bound to one reviewed target digest (doc 175) and the runner re-validates the target |
| Upstream reflects the secret | Existing echo refusal; result `uncertain`, no body |
| Use after revocation or rotation | Each `admit` reloads binding, credential revision and review; release re-checks; in-flight calls become `uncertain` |
| Background traffic hides credential misuse | Per-binding use counts in the doc 174 summary; administrator can revoke one binding immediately |

## 3. Contract

### 3.1 Binding policy `portal.secret-binding-policy/2-dev.1`

```json
{"format":"portal.secret-binding-policy/2-dev.1","operation":"http_bearer",
 "network_target_sha256":"<64 hex>","principals":["service","viewer"]}
```

- `principals`: non-empty sorted subset of `service`, `viewer`.
- `network_target_sha256`: the canonical target object digest from doc 175
  (required for HTTP operations, absent for `raw_read`).
- Exact-bytes canonical parsing as for v1. Fits `policy_json` (J8192).

v1 policies parse as `principals:["viewer"]` with whole-policy scope.

### 3.2 Service resource

`secrets.use` joins the H07 resource list. An egress session (doc 174 §3.2)
includes it only when `secrets.use` is granted and the session also carries
`network.http`. Holding the resource does not select a binding.

### 3.3 Broker request

`POST /v1/network/http` (doc 174) gains one optional field:

```json
{"requestId":"...","method":"GET","url":"https://api.deepseek.com/user/balance",
 "accept":"application/json","contentType":"","bodyBase64":"","credential":"deepseek"}
```

`credential` matches `^[a-z][a-z0-9-]{0,63}$`
(`app_platform_secret_bindings.go:21`). At `admit`, Portal additionally:

1. requires `secrets.use` in the session and current grants;
2. loads the active binding by installation and name, requires `service` in
   its principals and an operation this route supports (`http_bearer`,
   `http_api_key`; doc 177 and 178 add more);
3. requires its target digest to match the target the URL selects;
4. checks the consumed binding review, credential state and exact value
   revision exactly as `loadPlatformSecretBindingUse` does
   (`app_platform_secret_bindings.go:420-456`), with the doc 175 target check
   replacing the policy-revision equality for v2;
5. decrypts and places the header in the signed command, then clears its copy.

A viewer request naming a binding without `viewer` is refused the same way.

## 4. Review and consent

The binding review plan (`portal.secret-binding-review`) gains `principals`.
The console shows, per binding: target origin, operation, and either
"Only while a user is using the app", "Also by background work, with no user
present", or "Only by background work". Changing principals is a new binding
review (the existing reapproval path). The install review lists
`secrets.use` together with `background.run` as "may use reviewed credentials
in the background"; this is informational, since no binding exists yet.

## 5. Failure and audit

- Unknown, revoked, `review_required`, wrong-principal or wrong-target binding:
  `denied`, before decrypt. Credential custody unavailable: `unavailable`.
- Rotation (doc 150) sets dependent bindings `review_required`; background
  calls are then `denied` until reapproval. An app must report a `denied`
  credential call as unknown, not as a provider failure.
- The doc 174 summary row gains `{binding, count, outcomes}` entries.
  Binding reviews already audit; the audit detail gains `principals`.

## 6. Migration and compatibility

No data migration. Existing bindings stay viewer-only. An administrator
re-reviews a binding to add `service`. Status's setup flow asks for the
`service` principal on the bindings it uses for probes; its foreground "test
now" button continues to use the viewer path.

## 7. Test plan (these must fail first)

1. `issuePlatformServiceIdentity` with `secrets.use` is refused today; it must
   be accepted only when the grant exists.
2. A broker request with `credential` against a v1 (viewer-only) binding:
   `denied`, and the credential is never decrypted (custody spy).
3. A viewer request against a service-only binding: `denied`.
4. Successful background call: the upstream fixture sees the exact header; the
   broker reply, runner journal, audit rows and both processes' captured logs
   do not contain the value.
5. Echo of the value or the bare Bearer token in body or content type:
   `uncertain`, no body.
6. Revoke the binding, rotate the credential, revoke `secrets.use` or remove
   `background.run` between `admit` and release: `uncertain`, no body.
7. Binding bound to target A used for target B: `denied`.
8. SQLite and PostgreSQL, plus the native Scheduled Digest fixture with a
   service-principal binding.

## 8. Schema change

None. Principals and target scope live in the binding's `policy_json`
(J8192), which is canonical, reviewed and already part of the binding review.
`app_platform_secret_bindings` columns and CHECKs are unchanged. The earlier
gap table expected a column; it is not needed because no query filters on the
principal and every use parses the policy anyway. Contract changes needing
sign-off: binding policy v2, the H07 resource list, the broker `credential`
field and the binding review plan field.

## 9. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. May one binding carry both principals? Decided: yes; Status uses the same
   key for background probes and the foreground test button.
2. Should tasks (doc 153) also use credentials through the broker? Decided:
   yes, through the same egress session, with doc 153's rule narrowed to "no
   credential in task payload or result"; needed by no Status feature now.

## 10. Implementation notes (2026-11-17)

What shipped, and where it differs from sections 3 to 5.

- **Target scope.** Doc 175 has not landed: a policy still has exactly one
  origin and the binding binds the whole network policy digest
  (`network_policy_sha256`) plus the installation policy revision, as v1 does.
  Policy v2 therefore carries `network_policy_sha256` where section 3.1 shows
  `network_target_sha256`, in the same position. A single-origin policy is one
  target, so "the target the URL selects" is the policy the call is signed for.
  Doc 175 renames the field and narrows the scope; that change does not alter
  the principals list.
- **Spelling.** A viewer-only binding keeps the v1 policy bytes and its review
  has no `principals` member, so every binding and review made before this
  change reads back unchanged. v2 is written only when `service` is a principal,
  and `["viewer"]` has no v2 spelling (a v2 policy naming only the viewer is
  refused as non-canonical).
- **Tasks stay out.** Decision 2 of section 9 is not implemented here: a task
  handler cannot ask for `secrets.use` and a pinned test keeps it so. Only the
  egress session of doc 174 carries the resource.
- **Admit.** The app's `credential` is a binding name only. It travels in the
  admit frame as a closed optional member, Portal resolves and opens the value in
  the same read that checks the session, and the runner accepts credential
  fields in a command only when the admit named exactly that binding and the
  session carries `secrets.use`. Every authority challenge resolves the binding
  again without opening the value and refuses unless it is the one the admit
  resolved (revoked or reviewed binding, rotated or revoked credential, changed
  principals or policy all differ).
- **Audit.** The binding review audit detail carries `Principals`. The summary
  row gains `bindings: [{binding, count, outcomes}]` only when a call named one.
- **Console.** The binding form has a "who may use it" field (viewer only,
  also background, background only; never offered for setup-only credentials)
  with a disclosure in the approval step, and an active binding can be reviewed
  again to change it. The install review adds one informational sentence when
  `background.run` and `secrets.use` are granted.
