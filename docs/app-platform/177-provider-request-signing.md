# Proposal: provider request signing, Alibaba ACS3 first

Status: accepted 2026-11-17; implemented 2026-11-19 (section 10). Depends on
[176](176-background-credential-use.md) (and therefore 174, 175). Index and
order: doc 174.

## 1. Problem and evidence

Some provider APIs authenticate by signing each request with a long-lived
secret rather than sending a bearer value. The platform can inject only one
fixed header shape:

- Binding operations are `http_bearer`, `http_api_key` and the refused
  `raw_read` (`internal/app/app_platform_credential_schema.go:17`); only the
  first two project a header (`internal/app/app_platform_secret_bindings.go:35-50`).
- The signed runner command carries one `credential_header`/`credential_value`
  pair (`internal/apprunner/control/network_http.go:44-46`) and the executor
  refuses any header other than `Authorization` or `X-API-Key`
  (`internal/apprunner/broker/http.go:96-103`).
- `raw_read` is not callable and no plaintext-read endpoint exists (doc
  [150](150-secret-binding-use-and-rotation.md)). Giving the app the secret to
  sign locally is therefore not available, and doc [01](01-architecture.md) §6
  makes broker use the default.

Status's Qwen billing probe signs a fixed `QueryAccountBalance` call
(version `2017-12-14`) to `business.aliyuncs.com` or
`business.ap-southeast-1.aliyuncs.com` with ACS3-HMAC-SHA256, using an
AccessKey ID, AccessKey secret and optional security token
(Apps `apps/status/service/internal/status/probes.go:267-272`, `:290-292`,
`:466-503`).

## 2. Threat model

| Threat | Control |
| --- | --- |
| App learns the AccessKey secret | The secret never leaves Portal: Portal computes the signature at admission; the runner receives only per-request header values |
| App turns a billing read key into a general cloud key | The binding policy allowlists exact `(action, version)` pairs; the reviewed target pins the endpoint origin; the console asks for a least-privilege identity |
| App signs a request it then alters | The signature covers method, path, query, host, body SHA-256 and every `x-acs-*` header; the runner sends exactly the signed bytes and headers |
| Replay of a captured signed request | Fresh nonce and date per request; the command expires within seconds; nothing signed is returned to the app |
| Security token or signature reflected | Echo refusal extended to the security token |
| Clock skew causes silent failures | Portal clock is used; provider 4xx is returned to the app as-is |

## 3. Contract

### 3.1 Operation and credential value

New binding operation `acs3_hmac_sha256`. The credential value is canonical
JSON, validated inside custody at binding review and at every use:

```json
{"access_key_id":"LTAI...","access_key_secret":"...","security_token":""}
```

`access_key_id` 1..128 printable ASCII; `access_key_secret` 1..256;
`security_token` 0..4,096 (empty means absent). Unknown fields refuse.

### 3.2 Binding policy

Binding policy v2 (doc 176) with an operation block:

```json
{"format":"portal.secret-binding-policy/2-dev.1","operation":"acs3_hmac_sha256",
 "network_target_sha256":"<64 hex>","principals":["service","viewer"],
 "acs3":{"actions":[{"action":"QueryAccountBalance","version":"2017-12-14"}]}}
```

1..16 sorted, unique pairs; `action` `^[A-Za-z][A-Za-z0-9]{0,63}$`;
`version` `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`.

### 3.3 Request

Broker (doc 174) and foreground network requests gain a `signing` object,
mutually exclusive with `credential`:

```json
{"requestId":"...","method":"POST","url":"https://business.aliyuncs.com/",
 "accept":"application/json","contentType":"","bodyBase64":"",
 "signing":{"credential":"qwen-billing","scheme":"acs3","action":"QueryAccountBalance","version":"2017-12-14"}}
```

At admission Portal performs the doc 176 binding checks, requires the
`(action, version)` pair in the policy, then builds the request exactly:

1. Headers set by Portal only: `host` (from the URL), `x-acs-action`,
   `x-acs-version`, `x-acs-date` (UTC `YYYY-MM-DDTHH:MM:SSZ`, Portal clock),
   `x-acs-signature-nonce` (32 random hex), `x-acs-content-sha256`
   (hex SHA-256 of the exact body), `x-acs-security-token` when present, and
   `content-type` when the request has one.
2. Canonical request: method, `\n`, the URL path (`/` when empty), `\n`, the
   query parameters sorted by name with RFC 3986 encoding, `\n`, each signed
   header as `lowercase-name:trimmed-value\n` sorted by name, `\n`, the signed
   header names joined by `;`, `\n`, the body hash.
3. String to sign: `ACS3-HMAC-SHA256\n` + hex SHA-256 of the canonical request;
   signature: hex HMAC-SHA256 with the AccessKey secret.
4. `Authorization: ACS3-HMAC-SHA256 Credential=<id>,SignedHeaders=<names>,Signature=<hex>`.

The signed command replaces the single credential pair with
`signed_headers`: a sorted list of at most 9 `(name, value)` pairs, names
from the closed set above plus `authorization`, total ≤ 16 KiB, and
`signing_scheme: "acs3"`. The runner accepts `signed_headers` only with that
scheme, sets them verbatim, sets no other header except `Accept`,
`Accept-Encoding: identity` and `User-Agent`, and refuses if the request body
digest differs from `x-acs-content-sha256`. The secret is cleared from Portal
memory before the command is sent.

Responses are released as for any network call. The echo check additionally
covers the security token.

## 4. Review and consent

The binding review page shows the operation as "Alibaba Cloud signed request",
the endpoint origin, and the allowed actions with versions, and states that the
key should be a RAM identity limited to those actions. Adding an action is a
new binding review. Credential entry validates the JSON shape without echoing
it back.

## 5. Failure and audit

- Disallowed action, malformed credential value, target mismatch: `denied`
  before signing.
- Custody unavailable: `unavailable`. Provider 401/403 is a normal released
  status (Status maps it to `billing_permission_required`, `probes.go:323-325`).
- The doc 174 summary counts uses per binding and action. The binding review
  audit records the allowed actions.

## 6. Migration and compatibility

Additive operation; existing bindings unaffected. The runner network profile
advertises `signing_schemes: ["acs3"]`; Portal refuses signed requests to a
runner that lacks it. Status deletes its `signAlibaba` code path from the
backend and sends the `signing` object instead.

## 7. Test plan (these must fail first)

1. Creating an `acs3_hmac_sha256` binding fails today at the CHECK; after the
   migration it succeeds on SQLite and PostgreSQL, and an unknown operation
   still fails.
2. Signature vector: a fixed date, nonce, body and key produce the same
   `Authorization` value as the Status implementation (`probes.go:479-503`)
   and as a second, independently written test implementation.
3. Action or version outside the policy: `denied`; no custody decrypt (spy).
4. The runner refuses `signed_headers` without the scheme, an extra header
   name, a body whose digest differs from `x-acs-content-sha256`, and more
   than 9 pairs.
5. Neither the broker reply, logs, journal nor audit contain the secret,
   security token or signature.
6. Upstream echo of the security token: `uncertain`.
7. Rotation and revocation between admission and release: `uncertain`.
8. Native: a disposable TLS upstream verifies the ACS3 signature with the
   fixture key and returns a fixed balance document.

## 8. Schema change (approved by the owner on 2026-11-17)

Approved by the owner on 2026-11-17, together with the schema changes in docs
177, 178 and 180, as one migration, `0022`.

Yes: widen the `operation` CHECK of `app_platform_secret_bindings` by one value.
No column, table or row changes; the new list is a strict superset, so every
stored row still satisfies it. Implement as migration `0022` (the next free ID after
`0021`), following the `0021` pattern
(`internal/app/app_platform_import_grant_schema.go:91-180`): migration `0016`
keeps creating the frozen three-value list, and the new step widens it for
fresh and upgraded databases alike. Docs 178 and 180 are approved too; their
values (`pg_password`, `ingress_hmac_sha256`) are added in the same step, so
the `0022` CHECK carries all three new values.

The constraint carrying this check is `app_platform_secret_bindings_ck_15` on
PostgreSQL and `app_platform_secret_bindings_ck_29` on SQLite (rendered from
`platformCredentialTables()` with the existing `checkExpressions`).

PostgreSQL:

```sql
ALTER TABLE app_platform_secret_bindings DROP CONSTRAINT app_platform_secret_bindings_ck_15;
ALTER TABLE app_platform_secret_bindings ADD CONSTRAINT app_platform_secret_bindings_ck_15
  CHECK ((operation='http_bearer') OR (operation='http_api_key') OR (operation='raw_read')
      OR (operation='acs3_hmac_sha256'));
```

SQLite (in the migration transaction; the schema cookie value is read first):

```sql
PRAGMA writable_schema=ON;
UPDATE sqlite_master
   SET sql=replace(sql,
     'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read''))',
     'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read'') OR (operation=''acs3_hmac_sha256''))')
 WHERE type='table' AND name='app_platform_secret_bindings'
   AND instr(sql,'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read''))')>0;
PRAGMA writable_schema=OFF;
PRAGMA schema_version=<cookie+1>;
PRAGMA integrity_check(app_platform_secret_bindings);
PRAGMA foreign_key_check(app_platform_secret_bindings);
```

The update must change exactly one row, or find the new text already present
(idempotent rerun); anything else aborts the migration. Backup/restore: the
step declares `restoreRule{olderDumpOK: true}`, as `0021` does
(`app_platform_import_grant_schema.go:203-205`).

## 9. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. Sign in Portal or in the runner. Decided: Portal, so the AccessKey secret
   never crosses the runner socket; the runner only needs the derived header
   values.
2. Accept STS security tokens. Decided: yes; they are shorter-lived than
   AccessKeys and cost nothing extra.
3. Next schemes (AWS SigV4, Tencent TC3). Decided: not now; add each as its own
   operation value and review when an app needs it.

## 10. Implementation notes (2026-11-19)

What shipped, and where it differs from sections 3 to 8.

- **Where the secret lives.** `ParseACS3Credential` decodes the credential JSON by hand
  from the opened plaintext into byte slices, so the AccessKey secret and the token are
  never Go strings; Portal wipes the plaintext and the parsed slices as soon as
  `ACS3Sign` has run. What leaves Portal is the closed header set of
  section 3.3 (the Authorization value carries the signature; the security token is
  sent as `x-acs-security-token`, because the provider needs it there). The HMAC key is
  never in any command, frame, log or audit row. The signed command copy is wiped in
  Portal once the command is sent, and in the runner when the call ends.
- **Policy field.** The policy carries `network_policy_sha256`, not
  `network_target_sha256`, as doc 176 section 10 records. The block is
  `"acs3":{"actions":[{"action","version"}]}` after `principals`, in the `2-dev.1`
  format. An ACS3 policy is always written as v2, with `["viewer"]` as its principals for a
  viewer-only binding (a viewer-only HTTP policy keeps its v1 spelling).
- **Review.** A binding review for `acs3_hmac_sha256` carries `acs3_actions`; the
  credential is opened in custody and validated at review creation and again at commit;
  a value that is not an AccessKey never produces a plan. The review response also shows
  the reviewed origin (`network_origin`). The consumed review stores the actions; the binding
  read at use compares them with the policy, so a policy edited behind the review is
  refused.
- **Canonical target.** The signed bytes are the sent bytes: the runner sends the command's
  URL, so Portal refuses (`denied`) a target whose query is not already in canonical form
  (pairs sorted by name then value, RFC 3986 escapes, no `+`, `;` or empty pair) or whose
  path is not canonically escaped. Section 3.3 step 2 sorts the query for the canonical
  request; because the URL must be sent as signed, the application sends it sorted. A
  request with no query (the Qwen balance probe) is unaffected.
- **Wire.** The application's `signing` object is `{credential, scheme, action, version}`
  on the broker (`portal.broker/1`) and the foreground gateway, never beside a
  `credential`, and never on a stream. The admit frame carries the binding name in
  `credential` and `signingScheme`, `signingAction`, `signingVersion` (all or none). The
  signed command carries `signing_scheme` and the headers as two parallel scalar arrays
  (`signed_header_names`, `signed_header_values`), because a command is a flat object whose
  arrays hold only scalars. The runner accepts them only with `acs3`, only as the closed
  set (at most 9 pairs, 16 KiB, sorted, `host` and `content-type` equal to the request's,
  `x-acs-content-sha256` equal to the command's body digest and to the digest of the body it
  holds, an Authorization whose `SignedHeaders` is exactly the other names), and in a
  background call only when they carry exactly the admit's action and version.
- **What the runner cannot do, and what it can.** It holds no key, so it cannot check the
  signature; it checks everything that is not secret, and the command is itself signed by
  Portal's Ed25519 key, so it cannot change method, URL, headers or body hash without
  failing verification. The provider refuses a request whose bytes differ from what was
  signed.
- **Checks.** Every authority challenge of a background call resolves the binding again
  without opening it and refuses unless it is the binding the admit resolved and its
  policy still lists the command's action and version (rotation, revocation, a withdrawn
  principal, `secrets.use`, `background.run` and a re-review with other actions all
  differ). The call keeps stand-ins for the Authorization value and the token, not the
  values.
- **Order and errors.** A pair outside the policy, a binding that is not an ACS3 one, a
  plain credential naming an ACS3 binding, a target that cannot be signed as sent and a
  value that is not an AccessKey are `denied` before any signature (the first four before
  the credential is opened; the last needs the plaintext to be known). A store or custody
  failure is `unavailable`.
- **Echo.** The runner withholds a response (body or content type) that gives back the
  Authorization value, the signature in it or the security token, in the forms of doc 176
  section 10. The AccessKey secret is never in the runner, so it has nothing to match.
- **Audit.** The binding review audit detail carries `Actions`. The summary row's
  `bindings` entries gain `actions` (`"Action@Version"` to count) for calls whose binding
  resolved, which implies the policy listed the pair.
- **Migration.** `0022` widens the operation CHECK by `acs3_hmac_sha256`, `pg_password`
  and `ingress_hmac_sha256` together; `0016` keeps creating the frozen three-value list.
  Its verifier accepts either list before `0022` has run; `0022` declares
  `olderDumpOK`, as `0021` does. PostgreSQL constraint `app_platform_secret_bindings_ck_15`,
  SQLite check 29, as in section 8.
- **Console.** The credential form has a type (single secret, or Alibaba Cloud
  AccessKey with ID, secret and optional token, sent as the JSON of section 3.1); the
  binding form has the "Alibaba Cloud signed request" operation with a list of allowed
  (action, version) pairs, sorted before the review is requested; the approval step shows
  the origin, the actions and the RAM advice.
- **Not done here.** The runner network profile does not yet advertise
  `signing_schemes: ["acs3"]` (section 6). The profile is compared for equality with the one
  stored at install, so adding a member would make every installed runtime mismatch until
  it is reinstalled; a runner built before this change refuses a signed command at decode
  (it knows no `signing_scheme` member), so the call fails closed. Gating on the profile
  needs that comparison to become a capability check and is left for a separate change.
  The native test of section 7 item 8 is covered by a disposable TLS upstream in the
  broker package that rebuilds the canonical request independently and verifies the
  signature; the cross-repo Status change (deleting `signAlibaba`) is not part of this
  repository.
