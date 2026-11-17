# Proposal: provider request signing, Alibaba ACS3 first

Status: proposal, 2026-11-17. Not owner-approved. Depends on
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

## 8. Schema change (needs owner approval)

Yes: widen the `operation` CHECK of `app_platform_secret_bindings` by one value.
No column, table or row changes; the new list is a strict superset, so every
stored row still satisfies it. Implement as the next free migration ID after
`0021`, following the `0021` pattern
(`internal/app/app_platform_import_grant_schema.go:91-180`): migration `0016`
keeps creating the frozen three-value list, and the new step widens it for
fresh and upgraded databases alike. If docs 178 and 180 are approved, their
values are added in the same step.

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

## 9. Open questions

1. Sign in Portal (proposed) or in the runner. Recommended: Portal, so the
   AccessKey secret never crosses the runner socket; the runner only needs the
   derived header values.
2. Accept STS security tokens. Recommended: yes; they are shorter-lived than
   AccessKeys and cost nothing extra.
3. Next schemes (AWS SigV4, Tencent TC3). Recommended: not now; add each as
   its own operation value and review when an app needs it.
