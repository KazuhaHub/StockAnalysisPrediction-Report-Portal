# Authenticated data receipt channel

The controller can now retrieve an applied data-preparation receipt through the
existing protected local runner endpoint. This is separate from command admission:
registering a command still returns only admission acknowledgement, never proof
that preparation ran. No Portal stage writer or automatic dispatch is connected
in this slice; the complete installation workflow remains in progress.

## Journal read boundary

`Store.ReadDataPreparationReceipt(ctx, command, signature)` verifies a currently
valid signed envelope against durable controller identity after taking journal
write intent. The original exact logical effect, original fence, generation and
resource floors must still agree. Only applied commands with no ack marker and
canonical normal/recovered receipts can be returned.

The shared resource identity checks compare namespace, installation, release,
project, owner digest, controller epoch and writer/control fences. The observation
must remain present/revision 2 with the exact recorded observation/update time;
its timestamp cannot be in the future. Retained physical-root history must agree.
Prepared/unknown intent, stale/newer resource state, changed trust, ack, corrupt
metadata or expired authority refuses. No execution, native repair, ack mutation,
new receipt or writer grant occurs here. A resource changed by a later lifecycle
stage conservatively prevents this initial reader from returning old evidence.

`ParseDataPreparationReceipt` is a bounded canonical structural decoder, and
`MatchesCommand` checks the typed logical/ownership projection. Neither helper
turns caller JSON into authenticated evidence. Both ordinary and recovered
receipts remain historical observations; retrieval time is not a fresh physical
health/readiness observation.

## Wire protocol

The existing listener authenticates the kernel peer UID before reading any magic
or payload. Existing private development and root/non-root-controller profiles
retain their path, inode, UID/group/mode and server SO_PEERCRED checks.

| Message | Framing |
| --- | --- |
| Admission request | Existing PRCI0001; unchanged |
| Admission reply | Existing PRCA0001; still admission only |
| Data receipt request | PRCR0001 + fresh 32-byte random nonce + uint32 big-endian command size + exact command bytes + 64-byte signature, then EOF |
| Data receipt reply | PRCP0001 + SHA-256 of the entire request frame + uint32 big-endian receipt size + exact canonical receipt bytes, then EOF |

Command size remains 1..65,536 bytes; receipt size is 1..16,384 bytes. Excess,
truncated, trailing, wrong-magic and mismatched-request replies refuse. A new
nonce distinguishes repeated reads of identical signed bytes. The ordinary
admission acknowledgement cannot be interpreted as a receipt. Failures close
without private DB error text or arbitrary status objects.

The existing eight-session bound, three-second context and socket deadlines,
cancellation and listener lifecycle apply to this message. Receiving a reply is
not an independently signed portable receipt; its provenance is the pinned local
process and exact authenticated request.

## Client authority object

`ReadControllerDataReceipt` requires the configured non-root controller identity
and root-owned group-protected endpoint. `ReadDataReceipt` remains a separate
private same-user development helper. Both require independently trusted
controller authority, verify the outgoing signature before connection and again
before returning, and compare the response to that exact verified command.

Success mints `transport.DataReceiptObservation` with private fields. It exposes
copied receipt/controller values, the exact request and command digests, received
time and authenticated endpoint profile. The endpoint value records path,
runner/controller UID, controller GID and whether the root/controller profile was
used. A later Portal writer must require its configured root/controller profile;
a same-user test socket must not satisfy production receipt authority.

JSON encoding refuses, and decoding clears the receiver and refuses. Zero values
carry no authority. Controller public-key slices are cloned, so changing a getter
result cannot mutate the stored observation. Public receipt/endpoint values alone
cannot construct this proof. `MaxDataReceiptAge` is three seconds for the future
stage writer; that writer still must verify age/current runtime/lease at commit.

## Native acceptance

The project-quota workflow now builds a third binary for transport and runs the
Linux transport unit/race suite. Its opt-in root fixture reopens the *same* single
private journal and physical root prepared by the native journal test; it never
creates another deployment journal or reserves overlapping XFS projects.

A root runner exposes a group-protected socket. A subprocess with synthetic
non-root controller UID receives the actual normal receipt and, after real
expiry/recovery/remount, the recovered receipt. The returned namespace inode is
compared with independent native inspection. The child cannot open the private
journal directly. A different UID with the endpoint group and valid signature,
a controller without the endpoint group, a raw unsigned request that bypasses
client-side verification, and an unsettled command must all receive no proof.

Ordinary Linux transport tests cover fresh nonce binding, opaque proof behavior,
public-key copying, canonical fixed-vector agreement, cross-command replies,
wrong magic, replayed digest, oversized/truncated/trailing replies and admission
ack substitution. Shared journal tests cover prepared/unknown refusal and changed
resource/authority/receipt tuples. Fixed/synthetic vectors test contracts and SQL
logic only; the disposable fixture provides actual native/OS identity evidence.

## Reproduce and next steps

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -race -p 2 ./internal/apprunner/journal
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 GOOS=linux go vet ./internal/apprunner/transport
python3 -B -m unittest discover -s scripts -p project_quota_probe_test.py
```

Use this PR's Linux workflows for transport execution and native cross-UID proof.
Source `f3e77b540565d6fc678ec1be29f184bd4440d0ba` passed
[native run 40304423554](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40304423554),
including the Linux transport race suite. The downloaded report confirms normal
and recovered cross-UID receipt transport, peer/signature/direct-journal denials,
existing native preparation/recovery/quota checks and owned cleanup. Full local
Go tests, journal race tests (72.243 seconds), cross-compilation, vet/staticcheck,
shared vector/opaque JSON tests and twelve Python refusal tests pass. A negative
overlay disabling exact command matching fails at the cross-command assertion.
Main and daemon CI have independent status. Cross-compilation is not native arm64
acceptance, and disposable native evidence is not production acceptance.

The next controller integration must construct/sign an exact reviewed effect
under the current lease, retrieve this opaque root-profile observation, recheck
runtime enrollment, publisher/sponsor/lease/resource authority and freshness in a
Portal transaction, and atomically persist the effect/namespace/stage transition.
Do not accept arbitrary receipt JSON, an admission ack or a client-ready flag as
that evidence. The stage still must not activate an app before later readiness
and writer gates. Production startup/deployment, real credentials, Status,
server beta settings, schema/baselines and public SDK freeze are unchanged.
