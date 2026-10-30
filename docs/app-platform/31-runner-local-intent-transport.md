# Experimental local runner intent transport

Status: internal P03/P04 development under approved D1/D3/D4 candidate A.
This is a Linux-only, same-OS-user development transport, not a production
controller deployment or the completed application framework. The separate-UID
internal profile is specified in [document 32](32-runner-controller-identity.md). No command-line flag,
Portal startup hook, HTTP route or deployment configuration enables it. The
production controller UID/group provisioning, physical providers and dispatch
remain future work. No database shape or server beta setting changes.

## Source and use

`internal/apprunner/transport` provides:

1. `Listen(path, controllerUID, journalStore)` binds a new filesystem Unix socket
   in an **existing canonical 0700 directory owned by the runner's effective UID**.
   It never creates/chmods a parent, changes process umask, deletes a stale socket,
   enrolls a key or replaces an existing file. The socket becomes 0600 before any
   acceptance. The private directory prevents access during its creation.
2. `Server.Serve(ctx)` accepts at most eight concurrent sessions. Each checks
   Linux `SO_PEERCRED` against the configured controller UID **before reading**.
   Session I/O and the journal operation share a three-second deadline. Cancellation
   closes acceptance and active sockets and cancels database work; Serve waits for
   handlers. Call Serve once. `Close` is also available for an unserved listener.
3. `Exchange(ctx, path, runnerUID, rawCommand, rawSignature)` pins the socket owner,
   mode and inode before/after connecting, checks the kernel server UID, then sends
   one bounded request. It has the same three-second maximum and no automatic retry.
4. The service invokes the existing `journal.Store.AdmitControlCommand`; that layer
   obtains the enrolled public key itself, verifies the exact signed bytes, and
   commits only a prepared intent or observes a stable historical replay. There is
   no resource creation, execution permission, receipt or stage completion.

These helpers are internal, experimental and not a stable SDK. All app processes
must eventually have separate OS identities and no control-directory mount. A
same-UID process can impersonate a local endpoint; SO_PEERCRED alone does not solve
that. The signature still prevents it from manufacturing controller commands.
The supplied same-user directory/socket profile cannot connect a separately
provisioned Portal service account to a root runner. Do not weaken its 0700/0600
checks; use the explicitly configured, independently verified separate-UID profile
in document 32 after the operator provisions its required identity/directory.

## Endpoint checks and ownership

Only absolute canonical filesystem socket paths of at most 107 bytes are accepted.
Abstract sockets, NUL/newline paths, socket symlinks, unsafe writable ancestors,
foreign directory ownership, public sockets and multiple hard links are refused.
Root-owned sticky ancestors such as `/tmp` are allowed above the private parent;
root itself and trusted owner directories must still be real directories.

Listen refuses every existing pathname, including an old socket. After shutdown,
cleanup removes the pathname only if its current inode is the socket this Server
created. It does not unlink a replacement file. Directory mutation by the trusted
owner/root is outside this development profile's adversary boundary. This is not
an arbitrary remote endpoint or TCP service, nor an enrollment or recovery API.

## Closed wire format

One request per stream; all lengths are bytes:

| Offset | Length | Value |
| --- | --- | --- |
| 0 | 8 | ASCII `PRCI0001` |
| 8 | 4 | Unsigned big-endian exact command length, 1..65536 |
| 12 | declared | Exact raw signed command JSON |
| following | 64 | Raw Ed25519 signature |
| following | 0 | Client write-half EOF required |

The server requires EOF **before** journal admission, so truncated bodies, appended
bytes, a second request or a missing half-close never admit an intent. It allocates
only after validating the bounded header. JSON and logical identity checks remain
in the command verifier and journal, not a second permissive transport parser.

The response is exactly 41 bytes followed by EOF:

| Offset | Length | Value |
| --- | --- | --- |
| 0 | 8 | ASCII `PRCA0001` |
| 8 | 32 | SHA-256 of the complete request frame, including signature |
| 40 | 1 | 1 = journal admission returned successfully; 0 = refused |

This digest binds the response to this request on the authenticated local stream;
it is **not** a signature or portable physical receipt. Unknown status, wrong hash,
extra response bytes or truncation refuse. Internal database errors are never sent.
There are no generic method names, shell strings, paths or enrollment actions.

A successful response means only **intent acknowledged**. A replayed prepared or
unknown effect is not permission to dispatch again. Any error, timeout or lost
response can follow a committed intent: retain the stable effect identity and
reconcile. Never allocate a fresh effect or assume rollback from transport failure.
The existing higher-fence reconciliation refusal still applies.

## Verification and remaining gates

Tests first failed Linux compilation against missing transport entry points. The
Linux tests cover both peer-UID refusals before admission, positive admission and
closed refusal, oversized/malformed/truncated/extended/no-EOF frames, cancellation,
unsafe directories, symlink/public sockets, refusing existing files, replacement
cleanup, and malformed or request-mismatched responses. An integration test enrolls
an explicitly disposable public fixture, submits an invalid signature then a real
signed command over the socket, and observes the prepared identity through the
journal's replay API. It never provisions real credentials or a native resource.

The existing `app runner daemon probe` workflow runs all runner packages with race
instrumentation on disposable Linux. macOS can cross-compile these tests but cannot
supply Linux peer-credential evidence; no native-screen or production host check is
substituted for that run. Record the actual CI outcome in the PR before claiming
Linux acceptance.

Next: separate-user operator bootstrap and isolated negative UID tests, executable
provider admission/resource inspection, typed reconciliation and receipt verification,
Portal-side signed-command production/lease integration, full sandbox/quota
integration, and application lifecycle/UI acceptance. The production and public
freeze decisions remain separate.
