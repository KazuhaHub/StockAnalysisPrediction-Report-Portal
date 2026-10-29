# Portal writable readiness, refresh and current-source refusal

This follows [108](108-portal-writable-start.md). Portal can confirm a historical
writable process start, but cannot yet decide that its actual backend is ready.
This change consumes a fresh root runner probe and advances `probe` to `activate`.
It also permits a fresh readiness refresh while waiting at `activate`. Neither
operation activates an app or grants viewer/broker access. Registration/key
possession and current boot/viewer/activation remain subsequent requirements.

## Source states remain separate

The original writer and start readers retain their exact empty-readiness source
contracts. New private readiness-specific source/preparation paths are used only
for this continuation. They independently verify the existing exact applied
start effect and original canonical running receipt, not a browser readiness flag.
The source still has exactly preparation + writer + start: no fourth effect,
registration, activation or access grant is accepted.

At `probe`, the candidate row must have empty readiness, no observed timestamp
and unchanged creation/update times. At `activate`, it must have a separately
parsed canonical writable readiness record whose command payload, original
running receipt SHA, native ID, backend protocol and data format match the exact
applied process/source. Its observed timestamp must equal the row's timestamp,
follow the original process observation, respect the one-second DB/native clock
ordering bound and not lie in the host clock future. Unknown fields, partial
history, foreign source bindings and changed runtime/review/lease/trust refuse.

Writer intent snapshots now pin candidate update time, readiness bytes and
nullable observation time as well as original creation/source metadata. Existing
writer/start behavior keeps its original scope. A new readiness continuation may
read the exact historically observed row, but cannot treat it as fresh authority.

## Freshness and authenticated probing

`preparePlatformWritableProbe` revalidates the current install review/publisher/
runtime/lease and derives renewable signed nested references from the original
source, using the existing lease-bounded control-key flow. It cannot create a
writer/start intent or replace a resource. Freshly compare the configured managed
root target before native IO. Request only the closed `WritableCandidateProbe`
action over the existing protected root/non-root-controller endpoint.

The runner's existing concrete operation holds execution exclusion, verifies
current applied original/recovered start/source history and exact actual native
policy/process/private socket around its fixed readiness HTTP request. It checks
protocol/schema and current postflight before returning correlated canonical
metadata. This Portal change does not add an app URL, native callback, network
capability, new socket service or untrusted success boolean.

The Portal observation validator requires:

1. Opaque lifecycle proof of the exact probe action, received at most three
   seconds ago using its local monotonic receipt time.
2. The exact configured root peer/endpoint and non-root controller UID/GID,
   current authority/key/fingerprint and request command SHA.
3. Canonical writable readiness matching the exact current signed start payload,
   canonical original running receipt SHA, native ID, backend protocol and schema.
4. Native observation within the receipt's fresh three-second window and not
   before the original process observation. Transport additionally enforces its
   exact request/receive interval and signed reference deadline.

A parsed stored record cannot mint that opaque observation. The three-second
rule governs a current readiness decision; it does not erase source history.
After a pause at registration, re-read the same historical source and perform
another native probe. A fresh observation can therefore refresh `activate`
without creating/restarting a container or replacing source receipts.

## Atomic commit and refresh

`commitPlatformWritableReadiness` clones source snapshots and checks fresh proof
before opening a Portal transaction. Lock/re-read current OU, publisher/key/review,
runtime binding and exact operation lease, then reconstruct and compare the
entire original writer/start/candidate source after IO. Reverify the current
signed reference and exact proof binding.

CAS the candidate's readiness JSON, native observed time and update time using
its exact ID, installation/release/namespace, generation/policy, candidate state,
previous readiness/nullable timestamp and creation/update times. For `probe`,
atomically advance the exact operation state/row revision to `activate`. For an
`activate` refresh, keep the current operation revision, lease expiry, owner,
fence and stage unchanged. Write one audit event for each successful fresh
observation. Source effects and original running receipt bytes remain untouched.

Before commit, reconstruct the complete resulting source and require precisely
the expected candidate observation update with all other state unchanged.
Recheck current lease/trust/signature/freshness. Audit or current-state failure
rolls back candidate and operation updates together. An expired proof, stale
lease or current runtime disabled after native IO cannot settle readiness.

The result remains a pending installation with candidate state. Readiness is not
protected registration delivery, MAC/key possession, current boot identity,
active generation, task authority or a viewer access grant.

## Verification

SQL fixtures construct only historical writer/running/readiness metadata to test
source/reader validation. They do not produce opaque native or transport proof.
Tests cover stable source, empty/observed state separation, historical binding,
zero-proof refusal with unchanged dump, readonly/initial-start refusal, foreign
native/payload/receipt/schema, malformed/unknown JSON, mismatched timestamps,
changed writer/start/namespace/runtime/publisher, restore pause, stale lease and
extra history. Both SQLite and PostgreSQL cases run explicitly before dependency
scanning in the storage workflow; local PostgreSQL is not started.

Disposable actual Linux acceptance extends the existing non-root Portal/root
runner fixture after real writable start:

- Wait for the controlled backend to finish its real 256MiB/65536 inode quota
  probe and open its fixed readiness socket; no native start repeats while waiting.
- Receive actual opaque readiness, force a Portal audit failure and prove both
  operation/candidate updates roll back.
- Perform another actual probe and atomically advance the original operation to
  `activate`, retaining source/native identity and no grants.
- Refuse the stale original probe lease. Obtain another actual proof, wait beyond
  its three-second freshness interval and refuse it with unchanged storage.
- Keep the historical source after expiration; perform a fresh `activate` probe
  and require newer observation time with unchanged operation revision/fence/
  lease/source/native identity.
- Obtain another actual fresh proof, disable the current Portal runtime binding,
  and refuse its commit with unchanged storage. Restore only the disposable
  binding so existing independent readonly acceptance continues.
- Require exactly two successful readiness audits, three original effects,
  candidate/pending state and no access grants. Existing root witness rechecks
  exact native image, ownership, start time, completed quota proof and owned cleanup.

Required new markers/report flags are:

| Marker | Report flag |
| --- | --- |
| `PORTAL_WRITABLE_READINESS_ADVANCED` | `native_portal_writable_readiness_advanced` |
| `PORTAL_WRITABLE_READINESS_REFRESHED` | `native_portal_writable_readiness_refreshed` |
| `PORTAL_WRITABLE_READINESS_STALE_SOURCE_REFUSED` | `native_portal_writable_readiness_stale_source_refused` |

Fresh Linux acceptance is pending. Passing a synthetic fixture or an older
start test is not evidence for these composed readiness cases.

## Remaining work

Add protected writable registration and independent host MAC/boot verification,
eventually validating four exact effects. Connect current viewer/activation and
the automatic managed install worker only after those gates. Positive expired-
unknown runner recovery through Portal, physical writer revocation/stop/update/
retained-data/restore lifecycle, broker, SDK/shared React/Ant shell, neutral
reference apps/marketplace and full P00–P14 acceptance remain incomplete. No
schema/dependencies/toolchain, production/real credentials/Status adoption,
public SDK freeze or beta/version configuration changes are included.
