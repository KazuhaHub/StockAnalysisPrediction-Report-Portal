# Proposal: PostgreSQL liveness probe as a restricted stream capability

Status: accepted 2026-11-17; implementation pending. Depends on
[174](174-background-broker-egress.md), [175](175-reviewed-multi-origin-and-private-network.md)
and [176](176-background-credential-use.md). Index and order: doc 174.

## 1. Problem and evidence

- Doc [01](01-architecture.md) §4 (lines 92-95) reserves a "restricted stream
  mode" for databases: fixed destination and port, byte/time/concurrency
  limits, validated DNS, "not a generic CONNECT proxy", with a fixture protocol
  and refusal tests defined first. It does not exist.
- The only stream capability, `network.stream`, is HTTPS response streaming
  with a closed media-type set (doc [158](158-reviewed-network-stream.md);
  `internal/appplatform/http_stream_policy.go:16-23`).
- The backend has no network (`internal/apprunner/driver/container.go:384-388`).
- Portal already depends on `github.com/jackc/pgx/v5` (`go.mod:15`).

Status checks PostgreSQL by connecting with `sslmode=verify-full`, a guarded
dial to the configured host and port, the monitor's username and password, and
`SELECT 1`, returning only reachable/failed
(Apps `apps/status/service/internal/status/probes.go:361-379`, `:381-406`,
`:418-463`). It also supports a Unix-socket transport under
`/run/status-postgres` (`postgres_socket.go:16-34`, `probes.go:363-369`).

## 2. Decision in brief

The broker terminates the PostgreSQL protocol itself and performs one fixed
liveness exchange. The app never receives a byte stream, never sends SQL and
never sees the password. This is the restricted stream mode of doc 01 reduced
to the only operation the reviewed use needs.

Rejected: a TCP relay with protocol filtering. To keep the password from the
app, the relay would have to intercept SCRAM authentication and then parse and
filter every subsequent frame to hold queries to a liveness check; that is a
SQL firewall, not a bounded capability, and it fails open on parser gaps.

## 3. Threat model

| Threat | Control |
| --- | --- |
| Arbitrary SQL or data exfiltration | No query text crosses the API; the statement is a compiled-in constant |
| Lateral movement to other ports or hosts | Reviewed targets only (host, port, database); doc 175 address classes and private allowlist |
| Password disclosure | Credential bound by name; value only in the signed runner command, as for Bearer values |
| TLS downgrade or MITM | TLS required, full certificate and host-name verification; no `prefer`/`allow` modes |
| Connection exhaustion of the target | Per-installation concurrency ≤ 2, the doc 174 rate budget, one connection per check, closed after the check |
| Slow-loris from the server | Connect, TLS, auth and query share one deadline ≤ 10 s; bounded close |
| Server messages leaking data to the app | Only an outcome class and latency are returned |

## 4. Contract

### 4.1 Capability and descriptor

New capability `network.postgres` (descriptor contract revision: added to the
closed capability enum, `internal/appplatform/contract/descriptor-v2.schema.json:397-409`,
`:425-437`, and `descriptor_contract_generated.go:142`). It requires a backend,
like `network.http`, and is optional by default.

### 4.2 Policy `portal.postgres-probe-policy/1-dev.1`

```json
{"format":"portal.postgres-probe-policy/1-dev.1",
 "targets":[{"id":"orders-primary","host":"db.example.com","port":5432,"database":"orders","network":"public"}],
 "deadline_ms":10000,"concurrency":2}
```

1..64 targets sorted by `id`; `host` follows doc 175 rules for its `network`
class (DNS name for `public`; DNS name or IP literal inside the deployment
allowlist for `private`); `port` 1..65535 and, for private targets, listed in
the allowlist entry; `database` 1..63 bytes of `[A-Za-z0-9_-]`;
`deadline_ms` 1..10,000; `concurrency` 1..2. Canonical, ≤ 16,384 bytes,
stored in the `network.postgres` grant row. Changes use the doc 175 capability
policy review.

### 4.3 Credential binding

New binding operation `pg_password`. Credential value:

```json
{"username":"status_probe","password":"..."}
```

`username` 1..63 bytes, `password` 1..1,024 bytes. Binding policy v2 (doc 176)
with `network_target_sha256` = SHA-256 of the canonical Postgres target object.

### 4.4 Broker request

```text
POST /v1/postgres/liveness
{"requestId":"<uuid>","target":"orders-primary","credential":"orders-probe"}
```

Reply:

```json
{"outcome":"reachable","latencyMs":42}
```

`outcome` is one of `reachable`, `connect_failed`, `tls_failed`,
`auth_failed`, `query_failed`, `timeout`. Errors use the doc 174 codes
(`denied`, `unavailable`, `busy`, `invalid`). There is no `uncertain`: the
check has no external effect.

Portal signs a `portal.postgres-liveness-command/1-dev.1` carrying the same
authority tuple as `NetworkHTTPRequestCommand`
(`internal/apprunner/control/network_http.go:24-49`), the canonical target,
policy digest, and the username/password. The runner:

1. resolves the host, applies the target's address class rules to every
   address, pins the first;
2. connects with TLS required (`ServerName` = host; system roots), minimum TLS
   1.2, no fallback;
3. sends a startup message with only `user`, `database` and
   `application_name=portal-app-probe`;
4. authenticates (SCRAM-SHA-256 or password-over-TLS as the server requests;
   `trust` is accepted and reported as `reachable`);
5. executes the constant `SELECT 1` and requires exactly one row with the
   integer 1;
6. sends `Terminate` and closes; re-checks online authority before replying.

Bytes read from the server are bounded to 64 KiB per check.

## 5. Unix-socket targets: out of scope

Status's `/run/status-postgres` transport is not supported. A socket target
would require the runner to mount or open a host filesystem path for an app,
which doc 01 §4 forbids ("no arbitrary host paths"), and there is no reviewed
way to bind a host socket path to an installation. Status v2 reports such
monitors as `unsupported`; operators move them to TLS TCP.

## 6. Review and consent

Install or capability policy review lists each target as
`postgres://host:port/database` with its network class, and states that only a
connect-and-`SELECT 1` check is possible. The binding review shows the username
(not the password) and the target. Administrators are told to use a role with
no table privileges.

## 7. Failure and audit

Outcome classes above; `denied` for an unknown target, wrong binding principal
or target digest mismatch, before any dial. The doc 174 summary counts checks
per target and outcome. No server error text is logged or returned.

## 8. Migration and compatibility

Additive capability and operation. The runner network profile advertises
`postgres_probe_format`; Portal refuses the capability against a runner
without it. Status replaces `postgresCheck` with the broker call and drops the
`pgx` dependency from its backend.

## 9. Test plan (these must fail first)

1. Descriptor parsing refuses `network.postgres` today; it must accept it only
   with a backend.
2. Policy parser: unsorted targets, IP literal in a public target, port outside
   the allowlist entry, oversize database name: refused.
3. A fixture PostgreSQL server with a test CA: `reachable`; wrong password:
   `auth_failed`; self-signed or wrong-name certificate: `tls_failed`; server
   that offers no TLS: `tls_failed`; server that stalls after startup:
   `timeout` within the deadline.
4. The fixture records every statement it receives: exactly one `SELECT 1`
   per check, and only the three startup parameters.
5. A third concurrent check with `concurrency=2`: `busy`.
6. Password absent from reply, logs, journal and audit.
7. Revoking `network.postgres`, the binding or `background.run` mid-check: the
   check ends and returns `denied`/`unavailable`.
8. SQLite and PostgreSQL control stores; native run with the fixture server.

## 10. Schema change (approved by the owner on 2026-11-17)

Approved by the owner on 2026-11-17, together with the schema changes in docs
177, 178 and 180, as one migration, `0022`.

Yes: widen the `operation` CHECK of `app_platform_secret_bindings` with
`pg_password`. Same mechanism, constraint names and caveats as doc
[177](177-provider-request-signing.md) §8 (PostgreSQL `_ck_15`, SQLite
`_ck_29`); shipped in migration `0022`, the same step as `acs3_hmac_sha256` and
`ingress_hmac_sha256`.

PostgreSQL (shown alone; the combined step lists every approved value):

```sql
ALTER TABLE app_platform_secret_bindings DROP CONSTRAINT app_platform_secret_bindings_ck_15;
ALTER TABLE app_platform_secret_bindings ADD CONSTRAINT app_platform_secret_bindings_ck_15
  CHECK ((operation='http_bearer') OR (operation='http_api_key') OR (operation='raw_read')
      OR (operation='pg_password'));
```

SQLite:

```sql
PRAGMA writable_schema=ON;
UPDATE sqlite_master
   SET sql=replace(sql,
     'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read''))',
     'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read'') OR (operation=''pg_password''))')
 WHERE type='table' AND name='app_platform_secret_bindings'
   AND instr(sql,'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read''))')>0;
PRAGMA writable_schema=OFF;
PRAGMA schema_version=<cookie+1>;
PRAGMA integrity_check(app_platform_secret_bindings);
PRAGMA foreign_key_check(app_platform_secret_bindings);
```

The grant and its policy need no change: the `network.postgres` row fits
`app_platform_capability_grants` (`capability` T96, `policy_json` J16384,
`internal/app/app_platform_schema.go:45`).

## 11. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. Private CAs. Decided: allow an optional reviewed CA certificate (PEM
   <= 4 KiB) per target in the policy, pinned by the review; never a "skip
   verification" switch.
2. Return the server version. Decided: no; it is fingerprinting data with no
   liveness value.
3. Other databases (MySQL, Redis). Decided: separate capabilities with their
   own fixed exchanges, only on demand.
