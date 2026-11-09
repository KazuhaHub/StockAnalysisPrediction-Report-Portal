# Managed image delivery: implementation plan

Status: accepted 2026-11-09. Turns section 3 of
[doc 167](167-owner-decisions-remaining-acceptance.md) (`PKG-01`, `PKG-08`, `SEC-01`) into ordered,
test-first steps, following the lessons of [doc 168](168-fresh-host-restore-plan.md) and
[doc 169](169-data-migration-plan.md). It also closes the managed half of `DATA-05`, which doc 168
deferred to this section.

No Portal DDL is needed. The blob store is the filesystem; release records already hold the
signed descriptor that binds every digest. Journal v2 already lists `load-image` as a kind, and the
journal stage enum already has `stage`.

## Resolutions of conflicts between doc 167 §3 and the code

1. **Naming.** Doc 167 says `portalapp/1` and `portalapp/2`. The code has the development envelope
   `portal.offline-envelope/1-dev.1` (doc 157). "`portalapp/2`" is implemented as
   `portal.offline-envelope/2-dev.1`. Version 1 stays accepted for frontend-only releases. A
   managed release requires version 2, which replaces today's `ErrManagedOfflineNotReady`.
2. **Descriptor.** `backend.image` (a registry reference) becomes optional, and `backend` gains
   `images`. Exactly one of them is present:
   - `images`: one entry per declared platform, holding
     - `platform`;
     - `manifest_digest` and `config_digest`;
     - `layers`: an ordered list of `{diff_id, uncompressed_bytes}`, at most 32 per platform.
   - The platforms in `images` equal `backend.platforms` exactly.
   - Layer digests, sizes and media types are bound through the manifest digest. The order of
     `diff_id` values is checked against both the descriptor and the config.
3. **Profile.** No development or production profile exists today. The runner enforces the profile,
   because the runner is what resolves an image. A new runner config field,
   `allow_local_reference_images` (default `false`), is the development profile:
   - With it off (production), a descriptor carrying `backend.image` is refused before any claim,
     with reason `reference_image_refused`.
   - With it on, the current inspect-by-reference behaviour is kept for images already present.
   - Portal's own `config.yaml` is unchanged. The CI probes that build images into a local
     registry set the flag in their runner config.
4. **Catalogue.** A managed release carries `package_url`, the `.portalapp` version 2 file. The
   catalogue format becomes `portal.catalogue/2-dev.2`, and `2-dev.1` stays accepted for
   frontend-only releases. Online install downloads the package and then runs exactly the offline
   path. "Identical release, digests and running image" therefore holds by construction:
   - the descriptor and signature from the catalogue must be byte-equal to the package's;
   - otherwise the install refuses.
5. **Envelope layout (version 2).** The archive holds:
   - `descriptor.json`, `descriptor.sig`, and the optional frontend member as in version 1;
   - one flat `blobs/<sha256>` member per image blob, for the manifest, the config and the layers.

   Envelope rules:
   - Image blobs must use Store, not Deflate, so a member's bytes are the blob's bytes.
   - The set of image members equals the set the descriptor and the manifests reference. An extra
     member is refused, and so is a missing one.
   - The names `manifest.json`, `repositories`, `index.json` and `oci-layout` are refused at any
     depth.

   Limits:
   - at most 160 entries;
   - whole package at most 384 MiB;
   - each blob at most 256 MiB;
   - manifest and config blobs at most 1 MiB each.
6. **Accepted media types.**
   - Manifests: OCI image manifest v1, or Docker schema 2. An index, or a manifest list, as the
     platform manifest is refused.
   - Layers: `tar` and `tar+gzip`, in both the OCI and the Docker spellings.
   - zstd and foreign or non-distributable layers are refused in this release.
7. **Image identity.** The runner detects the image store from the daemon's `/info`. The driver
   status `driver-type` of `io.containerd.snapshotter.v1` means the containerd store; anything
   else means the classic store.
   - The expected image ID is the config digest on the classic store, and the manifest digest on
     the containerd store.
   - `InspectBackendImage` and container create address a loaded image by that ID. It never needs
     `RepoDigests` or tags.
   - The reference form is kept only for the development profile in resolution 3.
8. **Runner-side bytes.** Layers must be verified before Docker ever sees them, so that a layer
   cannot expand inside Docker's storage. Blobs therefore arrive over the blob channel into a
   runner-private scratch directory, under a new required runner config field `image_root`
   (root-owned, 0700, validated like the other roots). For each upload:
   - the runner hashes the stream as it writes;
   - for a layer, it also decompresses the stream as it writes, checks the `diff_id`, and stops at
     `uncompressed_bytes`.

   The load archive is then built from scratch blobs, and the runner hashes the exact bytes it
   writes into the archive stream. A mismatch aborts the load before the tar is closed, so there is
   no check-then-use gap. Scratch is per operation and removed after the load settles. Orphans are
   removed on runner start.
9. **Stage and effect.** `load-image` uses journal and Portal stage `stage`, as the next effect
   number free in that stage for the operation. The implementer records the exact number for each
   operation action in this document. No new stage value is added, because a stage value would be
   a journal CHECK change.
10. **Settlement.** A load is convergent: the archive is content-addressed, written by the runner,
    and carries no names. An `unknown` load is therefore completed under the same claim, the
    pattern doc 169 approved for restore. Read-back answers one of:
    - the receipt;
    - `pending`, while the load lock is held;
    - `orphaned`, when the row is `unknown` and the lock is free;
    - a typed absence, given only under the lock;
    - `blobs_missing`, with the digests the scratch no longer holds.

    The Portal re-uploads the missing blobs to the same operation's scratch and resends the same
    effect id. An unknown row is never rerun under a new identity.
11. **Ensuring the image before a start.** Every Portal operation that sends a start runs
    `load-image` at stage `stage` first. These are install-enable, update, roll back and retry from
    doc 169, and restore launch. `load-image` read-back of an image already present answers
    `applied` without an upload. If the image is pruned between the load and the start, the start
    is refused before its claim with the typed reason `image_missing`, and the operation follows
    its existing definite-failure path. This residual is stated, not hidden.

## Contracts

**Blob channel.** A Portal→runner stream on the existing control socket, in the network stream
pattern (`transport/network_stream_session.go`): fresh nonce challenge and proof, a magic switch,
and its own timeout after the 3-second control context is released. A session opens with a signed
`image-blob` command: control format, its own command purpose, the operation id, the blob digest,
its kind (manifest, config or layer), its declared size, the resume offset, and for a layer the
`diff_id`, `uncompressed_bytes` and media type. Then:

- Chunks are at most 1 MiB, each carrying its own SHA-256, with a per-chunk deadline of 10
  seconds. The total is bounded by the declared size.
- The end frame carries the whole-blob SHA-256.
- The runner answers `stored`, `resume_at <offset>`, or a typed refusal (`digest_mismatch`,
  `diff_id_mismatch`, `uncompressed_bound`, `size_bound`, `media_type`, `scratch_full`).
- Uploading is staging only: no effect row and no native Docker call. At most 2 concurrent
  sessions are allowed.

**`load-image` command.** Command, read and effect signature purposes, like `migrate-data`. The
payload is the operation, the installation, the release SHA-256, the platform, and the image entry
from the descriptor. Admission, in one transaction:

- the descriptor's release matches;
- the platform equals the host platform;
- the claim is made `unknown` before any native call.

The run, under the load lock:

1. Compare the daemon's `(ID, RepoTags)` list; the "before" list is taken under the lock.
2. Build the archive. The `index.json` is written by the runner and carries no annotations. The
   OCI layout and blobs come from scratch, re-hashed as written. Post it to `/images/load` with
   `quiet=1`.
3. Compare the list again, ignoring the expected ID. Any other difference refuses with
   `retag_detected`.
4. Inspect the expected ID: linux, the right architecture, no volumes.

Receipts are `applied`, or `reverted` with a typed outcome. Docker's load cannot be undone, so
`reverted` means the image was not accepted. A post-load inspection mismatch is `reverted`, and the
runner removes the image by ID only when the "before" list did not contain it.

**SEC-01 negatives (runner).**

- No code path issues `/images/create` (pull).
- No code reads `~/.docker/config.json`, `DOCKER_CONFIG` or a credential helper.
- Requests to the Docker socket go through one allow-listed method and path set.

A guard test enumerates that set.

## Portal storage (PKG-08)

- `<data>/app-staging/`:
  - Uploads and downloads stream into `<sha256-of-url-or-upload-id>.partial`.
  - A finished package is renamed to `<package-sha256>`.
  - Catalogue downloads resume with `Range` and `If-Range` across restarts, reusing
    `fetchResumable` with an on-disk prefix.
  - The cap is 2 GiB, with least-recently-used eviction of unpinned entries.
  - Orphan `.partial` files older than 24 hours are removed on start.
  - Any name that is not a hex digest or a `.partial` is removed.
- `<data>/app-blobs/<sha256>`:
  - Image blobs are extracted from a verified package after staging.
  - The cap is 8 GiB. Staging refuses when the release's blobs that are not yet present would not
    fit, before any release row is written.
  - Bytes are re-hashed on every use.
  - A blob in use by an upload is locked against removal.
- **Pinned set** (computed, no table): blobs referenced by a release that
  - is active;
  - is referenced by a retained recovery set;
  - is the target of a non-terminal operation; or
  - is the target of an open review plan.

  A sweep after each terminal operation and on start removes unpinned blobs.
- A corrupt cached blob (hash mismatch on use) is deleted. The release then needs its package
  again: an offline re-upload, or a catalogue re-download. The release stays installed, and the
  operation that needed the blob fails definitely before any runner effect. PKG-08 proves the
  re-download.
- The offline upload route stops reading the whole body into memory. It streams to a staging
  `.partial` file under the 384 MiB bound.

## Fresh-host restore, managed half (DATA-05)

- The recovery bundle writer includes `app-blobs/<sha256>` for the pinned set. That set is the
  doc 167 §1 rule 6 set: active releases, and releases pinned by a retained recovery set.
- Restore verifies every blob's hash before placing it.
- After recovery ends, a managed app is resumed through `load-image`, followed by its start.

## Ordered steps

The work lands as two pull requests into `integration/app-platform`. The second is stacked on the
first, and each must be CI-green on its own.

### PR 3a: contract and runner

1. Descriptor contract: `backend.images`, the exactly-one rule, the platform equality, the limits.
   Regenerate the Go and TS contract and update the digest pins.
2. Envelope version 2 parser in `internal/appplatform/offline.go`, with resolution 5's rules and a
   crafted-package corpus:
   - a tag annotation;
   - `manifest.json`, `index.json`, `oci-layout` and `repositories`;
   - an extra member and a missing member;
   - a Deflate layer;
   - oversize entries.
3. Catalogue `2-dev.2` with `package_url`.
4. Runner config: `image_root` and `allow_local_reference_images`.
5. Control contracts: `image-blob` and `load-image`, with their purposes. Regenerate the command
   vectors.
6. Driver:
   - image store detection;
   - the expected ID;
   - the revised `InspectBackendImage`;
   - container create by ID;
   - `LoadImage` (the archive writer, the before/after comparison, the post-load inspect);
   - the Docker request allow-list and its guard test.
7. Runner scratch store and blob channel server: hashing, gzip `diff_id`, the uncompressed bound,
   resume, the concurrency bound, orphan removal.
8. Journal `load-image`: admission, claim, the lock, receipts, read-back states, completion under
   the same claim.
9. Transport actions and replies for `load-image`, and the `image_missing` refusal on every start
   path.
10. Native Linux tests: load a real two-layer image built in the test, the containerd or classic
    store CI happens to use, the retag refusal, the bomb refusal, and the SEC-01 negatives.

### PR 3b: Portal and acceptance

11. Staging and blob directories, caps, eviction, the pinned set, the sweep, orphan removal.
12. Offline route streaming to staging; staging extracts blobs; the cap refusal leaves no release
    row.
13. Catalogue package download with resume across restart, and the descriptor and signature
    byte-equality check.
14. Portal blob sender (the client side of the channel) and the `load-image` sender. Insert the
    `load-image` effect before every start (resolution 11), with read-back, `blobs_missing`
    re-upload and the absence handling.
15. Recovery bundle and restore of pinned blobs (the managed half of `DATA-05`).
16. Tooling and acceptance:
    - export the Scheduled Digest image as OCI content (buildx `type=oci`);
    - the Apps repo's `build-app-v2.py` emits `backend.images`, and the packer writes version 2
      envelopes (an Apps repo PR);
    - the CI probe that installs from offline and from catalogue;
    - the PKG-08 interrupt, restart, resume, corrupt and cap scenarios;
    - SEC-01 evidence;
    - update `06-acceptance.md`, the checklist rows and the evidence JSON.

The named failing tests for each step are listed in the review record of each pull request.

## Risks

- The containerd store's treatment of untagged loaded images must be proven natively. If CI's
  daemon uses the classic store, the containerd branch is covered by unit tests only. This is
  stated in the evidence.
- A prune between the load and the start produces a definite start failure, not a silent
  re-pull.
- The 8 GiB pinned cap can refuse an update while old recovery sets are retained. The refusal names
  the cap; the operator frees space by retention (section 5).
- Packages are bounded at 384 MiB, which limits image size. A registry pull is deferred to its own
  ADR.
