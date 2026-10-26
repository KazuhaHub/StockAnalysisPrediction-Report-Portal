# Separate Portal and runner OS identities

Status: internal Linux implementation under approved D1/D3/D4 candidate A, for
isolated development acceptance. It extends the private transport in document 31.
It does not enable a production service, change users/groups on a real host,
enroll production trust, dispatch resources or change beta configuration.

## Why the second endpoint profile exists

A 0700 directory and 0600 socket are suitable for same-user development but cannot
connect a non-root Portal account to a root runtime controller. Giving Portal the
Docker socket would erase the intended boundary. The additional profile keeps
filesystem ownership with root, grants only narrow Unix-socket access to a
configured dedicated group, and independently authenticates the peer UID.

| Element | Required identity/mode | Reason |
| --- | --- | --- |
| Runner process | effective UID 0 | Initial approved rootful runtime authority |
| Portal process | Explicit nonzero UID | Stable controller identity, independent of group |
| Endpoint group | Explicit nonzero dedicated GID | Traverse directory/connect socket only |
| Existing control directory | root:configured-GID, exactly 0750 | Portal can traverse; cannot create, replace or unlink endpoints |
| Socket created by runner | root:configured-GID, exactly 0660 | Group can connect; cannot change inode ownership or parent contents |
| Other ancestors | Canonical root-owned directories without group/world write, except root sticky ancestors | Prevent path substitution above the control directory |
| Request | Exact enrolled Ed25519 controller signature | OS membership alone cannot authorize journal intent |

UID/GID 0 and uint32(-1) are refused as controller identities. Names, catalogue
content, request JSON and caller-selected keys cannot choose server trust. A
trusted host configuration supplies numeric UID/GID once; per-app installers must
not add app processes to this group. Root compromise or root deliberately handing
an untrusted process its listening socket/key remains outside this boundary.

## Internal integration steps

1. Runtime operator selects the **existing** Portal service account and a dedicated
   non-root endpoint group. Verify the account's UID is nonzero and remains stable.
   Give Portal membership in that group, without any Docker/control-device group.
   Keep application UIDs and broker identities out of it. No library code creates
   or edits system accounts, group membership or `/etc` files.
2. Operator provisions a canonical root-owned control directory with the exact
   group and mode above, beneath trusted root-owned ancestors. This must be
   separate from the root-only journal directory and app-writable data/socket
   directories. The library refuses an absent directory or incorrect ownership;
   it never silently repairs it. There is no released bootstrap CLI yet.
3. A root runtime service explicitly calls
   `transport.ListenController(path, portalUID, endpointGID, journalStore)` and
   `Serve(ctx)`. It binds only a new socket, sets only that socket's group/mode,
   and retains the pre-existing-path refusal and inode-checked cleanup. It does
   not auto-enroll, accept TCP, execute a generic method or start a resource.
4. Portal running as that configured UID calls
   `transport.ExchangeController(ctx, path, portalUID, endpointGID, raw, signature)`.
   The client first checks its own effective UID, then root ownership, exact group,
   modes and stable inode, and finally the **kernel server UID** before sending
   signed bytes. A root-owned socket inode alone does not prove a root server.
5. The root listener checks the **kernel controller UID** before reading a frame.
   Another member of the endpoint group can open a connection but cannot submit
   a command. The ordinary journal signature/epoch/fence/expiry checks still run.
6. Preserve the transport's stable-effect reconciliation rules on all errors.
   Admission means prepared intent only. Physical dispatch, completion receipts,
   service supervision, UID reservation and production enrollment are separate
   integration work; do not infer readiness from these helpers.

The original same-user `Listen`/`Exchange` entry points keep their exact private
0700/0600 profile. Neither function detects a permission error and falls back to
looser modes. Group access is enabled only through the explicit controller API.
No environment variable or server setting activates it in this change.

## Disposable Linux acceptance

The `app runner daemon probe` workflow first runs ordinary runner race tests. A
second step compiles the transport race-test executable as the ordinary hosted
worker user, then invokes **only** `TestControllerIdentityAcceptance` with sudo and
an empty environment containing explicit disposable-worker markers. This test
refuses unless UID is root, `GITHUB_ACTIONS=true`, `RUNNER_ENVIRONMENT=github-hosted`
and `PORTAL_APP_RUNNER_IDENTITY_ACCEPTANCE=1`. Other Go test runs skip it.

All filesystem state is in one exclusively created `/tmp/runner-identity-acceptance-*`
fixture. The test copies its executable there and creates a disposable enrolled
journal using a labelled public fixture seed. It starts child processes with
numeric UID/GID credentials via `exec.Cmd.SysProcAttr`, clears inherited groups and
passes only public command/signature fixture bytes in a minimal environment.
It does not create system users or touch Docker, an existing directory/journal,
real credentials or production configuration. The root fixture is removed after
all child processes and the listener terminate.

The test proves:

- Incorrect directory mode, owner or group refuses without creating an endpoint.
- A real non-root controller process can submit a correctly signed intent to the
  root listener; the durable journal observes the prepared effect as a replay.
- Another UID in the **same** group, even holding the real valid signed request,
  is refused by kernel peer identity before journal admission.
- An unrelated user and the real controller UID without the endpoint group are
  refused by actual filesystem permissions, before any stream is opened.
- The right UID/group with an invalid signature is refused by the enrolled journal.
- A non-root impostor listener is created elsewhere, then root moves/chowns only
  its socket inode into the protected fixture directory. The inode has perfectly
  matching root/group/mode metadata, but the controller rejects the kernel server
  UID and the impostor observes EOF **without receiving request bytes**. This
  isolates server peer authentication from metadata checks.

The dedicated numeric identities 41001..41003 are test credentials only, not
production allocation guidance. The acceptance process has a bounded test timeout
and child timeouts. Acceptance must be checked against the final source CI before
claiming these OS behaviors; macOS cross-compilation is not Linux proof.

Remaining work: complete operator/service bootstrap and restart recovery, bind
Portal reviewed intents and sealed signer to this channel, implement and reconcile
physical storage/container providers, and pass full managed/static app lifecycle
and UI acceptance before any production rollout decision.
