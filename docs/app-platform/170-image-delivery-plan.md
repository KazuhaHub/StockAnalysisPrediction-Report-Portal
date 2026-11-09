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

   Recorded numbers. No effect is issued in stage `stage` today (the kinds `stage` and `verify`
   are allowed but never sent), and each operation loads exactly one image, so the next free
   number is `1` for every action. The runner does not hard-code it: it accepts any number from
   1 to 32 and enforces only the journal's uniqueness per operation and stage.

   | Operation action | Effect number | Note |
   |---|---|---|
   | install | 1 | the stage list already has `stage` |
   | update (also retry, and roll back as a new update plan) | 1 | loads the target release's image |
   | repair | 1 | the stage list already has `stage` |
   | enable | 1 | the stage list has no `stage`; see implementation note 3 |
   | restore (launch) | 1 | the stage list has no `stage`; see implementation note 3 |
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

## Implementation notes (PR 3a)

Where the plan was silent, or the code made a different choice the safer one, the choice is
recorded here.

1. **Descriptor bounds the plan left open.** `uncompressed_bytes` is at most 1 GiB per layer, and
   the sum over one platform at most 4 GiB. `images` has one entry per declared platform, so at
   most 2.
2. **Layers travel as a string in the `load-image` payload.** The command parser accepts objects
   and strings only, so the list is `diff_id:uncompressed_bytes` pairs joined by commas, the same
   encoding the `recovers_chain` field already uses. The runner binds the string to the descriptor
   entry in the descriptor the frame carries, which it hashes against `release_sha256`.
3. **Operations without a `stage` stage.** `enable` and `restore` have no `stage` stage in
   `operationStages`. The runner accepts the command whatever the operation; Portal (PR 3b) must
   either add the stage to those actions or issue the load from an existing stage. The runner
   needs no change either way. PR 3a also does not add `load-image` to Portal's
   `platformEffectKindAllowed`, which is Portal code.
4. **"Read-back of a present image answers `applied`" is implemented on execute.** A read never
   writes a row, so it cannot create an applied effect. A `load-image` execute under a new claim
   checks the daemon first and settles `applied` with state `present` without any blob in
   scratch. A read-back of an unknown effect whose blobs are lost answers `orphaned` (not
   `blobs_missing`) when the image is already present, so the sender resends and gets that
   receipt with no upload.
5. **`image_missing` and `reference_image_refused` are typed on the runner, not on the wire.**
   Every start path (`DispatchCandidate`, `DispatchDataWriter`, `DispatchWritableCandidate`,
   `DispatchUpdateStart`, `DispatchMigrateData`) now calls a read-only image gate before it claims
   or reserves anything. The lifecycle protocol has no typed failure reply, so the sender sees an
   uncertain error, reads the effect back and finds a typed absence: the existing definite-failure
   path. The runner logs the typed reason. The update-start reservation action carries no
   descriptor, so its gate sits at the claim, after the reservation.
6. **The production profile refuses a registry reference in the inspection every start performs,**
   not in a separate admission step, and a `load-image` for a registry-reference descriptor is
   refused before its claim.
7. **Uncompressed size is an exact match, not only a bound.** The layer sink stops at the declared
   size and also requires the decompressed length to equal it, which is stricter than the plan.
8. **Layers are verified while the upload arrives,** through a pipe into the gzip reader, as the
   plan says. A resumed upload rebuilds every hasher by reading the stored prefix back; nothing
   about a prefix is trusted.
9. **The end frame carries the declared digest.** A resumed sender has not seen the prefix, so it
   cannot hash the whole blob; the runner compares the end frame with its own computation and with
   the signed command.
10. **A separate load lock.** Loads are serialized by a gate of their own, not the data execution
    gate, so a large load does not block data effects.
11. **Scratch.** The scratch cap is 1 GiB in total, at most 2 concurrent sessions and one session
    per blob; orphans are removed when the store is opened, which the service does first at start.
12. **Docker allow-list.** The set includes `GET /containers/{id}/logs`, which the runner never
    issues; the native container tests read container logs through the same client.
13. **Portal is unchanged in 3a.** The offline route refuses a managed package
    (`ErrManagedOfflineNotReady`) until Portal stores blobs, and the catalogue's `package_url` is
    parsed but unused. `cmd/app-inspect` reads up to 384 MiB so the tool can inspect a version 2
    package.
14. **Registry-reference descriptors need the development profile.** Every CI probe that runs an
    image by reference sets `allow_local_reference_images`, as do the in-process native tests
    through `driver.WithLocalReferenceImages()`.

Not verified here (no Docker on the development host): the loaded image's ID on the classic and
containerd stores, the daemon's handling of an OCI layout with Docker schema 2 media types, and
every native Linux path. The native tests in `image_load_native_linux_test.go` state what they
prove, and the store the daemon reports is logged.

## PR 3a review record

Named tests per step. Each failure direction is in the test name or its comment.

1. **Descriptor contract:** `TestDescriptorBackendImagesAcceptedAndBoundToPlatforms`,
   `TestDescriptorBackendImagesRefusesLooseForms` (both forms, none, missing or extra or duplicate
   platform, bad digests, layer count and size bounds, unknown fields),
   `TestDescriptorContractSourceAndTypes` (digest pin and generated types).
2. **Envelope version 2:** `TestOfflineEnvelopeV2BindsImageContentToDescriptor`,
   `TestOfflineEnvelopeV2RefusesCraftedPackages` (extra and missing member, Deflate layer, changed
   or renamed layer, diff_id and order mismatch, wrong architecture, zstd and foreign layers, an
   index or manifest list, layer `urls`, case-aliased keys, `index.json` with a tag annotation,
   `manifest.json`, `repositories`, `oci-layout` at any depth and case, too many entries, oversize
   metadata), `TestOfflineEnvelopeV2AcceptsDockerSpellingsAndSharedBlobs`,
   `TestOfflineEnvelopeV1RefusesImageMembersWithoutDeclaration`,
   `TestPlatformOfflineRefusesManagedPackageUntilBlobsAreStored`.
3. **Catalogue:** `TestCatalogueV2Dev2CarriesPackageURL`,
   `TestCatalogueV2Dev2RefusesUnsafePackageURLAndOldFormatPackage`.
4. **Runner config:** `TestConfigRefusesExposureAndOverlappingRoots` (image root),
   `TestConfigDevelopmentProfileDefaultsToProduction`.
5. **Control contracts:** `TestLoadImage*` and `TestImageBlob*` in `internal/apprunner/control`
   (canonical bytes, disjoint purposes, malformed payloads, every field bound by the digest, the
   nonce proof), plus `TestRunnerCommandVectors` over the regenerated vectors.
6. **Driver:** `TestInspectManagedImage*`, `TestProductionProfileRefusesRegistryReferenceBeforeAnyDaemonRequest`,
   `TestExpectedImageIDPerStore`, `TestPlanCandidateContainerBindsManagedImageByManifestIdentity`,
   `TestLoadImage*` (runner-owned layout, present, typed rejections, corrupt scratch,
   uncertain errors, allow-listed requests), `TestLoadedImagePresentIsReadOnly`,
   `TestDockerRequestAllowListIsClosed`, `TestAllowListTransportStopsAPullBeforeTheSocket`,
   `TestRunnerSourceNeverPullsOrReadsDockerCredentials`.
7. **Scratch and blob channel:** `internal/apprunner/imagestore` tests (verification, bomb stop,
   resume, caps, concurrency, orphans, link refusal) and the `TestBlobChannel*` tests (multi-chunk
   store, resume after a lost connection, typed refusals, oversize traffic, damaged chunk, malformed
   frames and stalls, unauthenticated sessions, session bounds, `TestBlobProviderHoldsNoJournalOrNative`).
8. **Journal:** `TestLoadImageClaimsUnknownBeforeAnyNativeCall`,
   `TestLoadImageUnknownIsCompletedUnderTheSameClaim`,
   `TestLoadImageMissingBlobsKeepTheClaimAndNameTheDigests`, `TestLoadImageRefusesBeforeAnyClaim`,
   `TestLoadImageRefusesStaleAndConflictingIdentities`,
   `TestLoadImageRefusesASnapshotThatDoesNotBindTheClaim`, `TestLoadImageSerializesLoads`,
   `TestLoadImageReadBackStates`, the receipt and status parser test, and the replay and rejection
   tests.
9. **Transport and start paths:** `TestLoadImageTransportActionsAndSignaturePurposes`,
   `TestLoadImageTransportReplyBinding`, `TestLoadImageTransportStatusReplies`,
   `TestMigrateDataRefusesBeforeItsClaimWhenTheTargetImageIsMissing`. The other start paths call
   the same gate on a concrete native runtime, so they compile here and run natively.
10. **Native Linux:** `TestNativeLoadImageTwoLayerImageOnTheDaemonStore`,
    `TestNativeRetagByAPublisherLayoutIsDetected`,
    `TestNativeLoadImageRefusesACorruptLayerAndAddsNothing`, `TestNativeNoPullIsPossible`.

## PR 3a first-round review follow-ups

1. **Blob roles (major).** Scratch writes a verification record (kind, media type, `diff_id`, uncompressed size, sha256) atomically next to each blob, before the blob takes its final name. The load and `Missing()` accept a blob only when its record equals its role in the plan (manifest, config, each layer with the descriptor's `diff_id` and size); a blob stored under another kind is missing for that role and replaced by a re-upload. The driver's `ImageBlobSource.Verified` enforces it again while the archive is written; the per-platform total is re-checked at load. Tests: `TestALayerUploadedAsConfigIsNotAcceptedForTheLayerRole`, `TestALayerRecordMustEqualTheDescriptorsClaims`, `TestABombUploadedAsConfigNeverCountsAsALayer`, `TestLoadImageNeverSendsABlobStoredUnderAnotherKind`, `TestLoadImageTreatsABlobOfAnotherRoleAsMissing`.
2. **No definite end (major).** (a) The envelope parser decompresses every layer and checks the `diff_id` and exact size: `TestOfflineEnvelopeV2RefusesALayerWhoseContentBreaksItsDeclaration`. (b) A definite refusal (`digest_mismatch` on a complete stream, `diff_id_mismatch`, `uncompressed_bound`, `media_type`) is recorded in a `.refusals` area of scratch that is exempt from eviction and kept across a restart, so no schema change is needed; the next execute or read-back settles `reverted` with outcome `blob_refused` (no image, no store, no Docker call): `TestLoadImageExecuteAfterARefusedUploadSettlesBlobRefusedWithoutDocker`, `TestLoadImageReadBackAfterARefusedUploadSettlesBlobRefused`, `TestADefiniteRefusalIsRecordedAndSurvivesEviction`, `TestLoadImageTransportAcceptsABlobRefusedReceiptWithNoImage`. The layer verdict is read once the manifest is back in scratch, since the layer digests come from it.
3. **Abandoned scratch.** A reservation over the cap evicts whole operation directories that have no upload session and are not held by a load, least recently used first: `TestAbandonedScratchDoesNotBlockANewOperation`, `TestEvictionSparesHeldOperationsAndActiveSessions`.
4. **Accounting.** One owner (`opBytes`, keyed by operation); an upload refunds only its own unwritten reservation; `RemoveOperation` refuses an operation with an upload in flight: `TestRemoveOperationDuringAnUploadIsRefusedAndAccountingHolds`, `TestAFailedPrefixReadRefundsExactlyWhatWasCharged`.
5. **`all=1`** on the before and after image list (`TestLoadImageBuildsARunnerOwnedLayoutAndAcceptsTheExpectedImage` asserts the query).
6. **Read-back with an unbound descriptor** returns `ErrLoadImageRefused`, as execute does: `TestLoadImageReadBackRefusesADescriptorThatDoesNotBindTheCommand`.
7. **One Docker client constructor** (`newDockerClient`) wraps the allow-list; `TestOnlyTheDockerClientConstructorDialsTheSocket` fails if the dialer is referenced elsewhere. The dead `allowed` map is gone.
8. **Timeout.** `LoadImageExecute` has its own connection bound, `driver.ImageLoadTimeout` plus 2 minutes, on both sides: `TestLoadImageExecuteTimeoutCoversTheLoadBound`.
9. **Classic store (from CI).** The daemon's own HTTP status and error text from `/images/load` is kept, bounded to 512 bytes, in the snapshot for logs and tests only. For the classic store the runner now also writes its own legacy `manifest.json` (`Config` and `Layers` by exact blob paths, `RepoTags` null, so no tag is ever created) next to the OCI files; the containerd store gets the OCI files only. The publisher package is still refused if it carries `manifest.json`. The native test logs the daemon version, store and driver: `TestLoadImageKeepsTheDaemonsErrorText`. The Apps checkout ref in `app-project-quota-probe.yml` moved to the contract sync commit.

## PR 3a second-round review follow-ups

1. **Refusals are bound to their declaration (major).** A refusal record keeps the whole declaration it was made under (kind, media type, `diff_id`, uncompressed size, size, digest) and is stored per declaration. `Refused()` counts one only when it equals the plan's role exactly (for the manifest and config, kind and digest). An upload that declared a wrong `diff_id` or size is refused but never poisons the role, and a correct re-upload clears it and proceeds. Tests: `TestARefusalUnderAWrongDeclarationDoesNotCountAgainstTheRole`, `TestADefiniteRefusalIsRecordedAndSurvivesEviction`, `TestLoadImageAppliesAfterAWrongDeclarationWasRefusedAndTheUploadWasCorrected`.
2. **Present beats refused.** The execute checks whether the image is already in the daemon before it settles a refusal; a present image settles `applied`. Test: `TestLoadImagePresentBeatsARefusal`.
3. **A read-back never writes a row.** It answers the new typed state `blob_refused` and leaves the effect `unknown`; the Portal then sends the execute under the same effect id and that settles it `reverted` with outcome `blob_refused`. Note 4 above stays true. Tests: `TestLoadImageReadBackAfterARefusedUploadAnswersBlobRefusedAndWritesNothing`, `TestLoadImageTransportStatusReplies` (new state).
4. **Structural SEC-01 guard.** `TestOnlyTheDockerClientConstructorDialsTheSocket` now parses the non-test runner code with `go/ast`: no reference to `dockerDialer` outside the constructor, no unix-network dial outside it (the app-socket readiness dialer is named), no `http.Transport` or `http.Client` literal that is not the constructor or an app-socket client. The unix-dial and literal rules are scoped to the driver and journal packages, the only ones that can reach Docker; the broker's outbound clients and the transport's control-socket dial are not subject to them.
5. **Eviction.** Under the lock an evicted operation directory is only renamed to `.trash-<n>`; `RemoveAll` runs after the lock is released, and start removes leftovers. Only operations with no upload session and no load hold for at least 10 minutes (`EvictIdle`) are evicted; otherwise the answer is the retryable `scratch_full`. The Portal serializes its uploads (PR 3b), so a retry finds the room. Tests: `TestEvictionNeverTakesAnActiveOrRecentlyIdleOperation`, `TestOpenRemovesLeftoverTrash`.

## PR 3a third-round review follow-ups

1. **Full declaration for every role.** A refusal counts only when kind, media type, size, diff_id, uncompressed size and digest all equal the role. The config's media type and size come from the manifest, so no refusal counts until the manifest is stored and verified (the Portal's envelope parser has already verified it); a manifest declared too long never poisons anything. Tests: `TestAMetadataRefusalUnderAWrongSizeDoesNotCountAgainstTheRole`, `TestAManifestDeclaredTooLongNeverPoisonsAnything`.
2. **Fail closed on the presence check.** If Docker cannot answer, an existing refusal settles nothing, on execute or read-back: the answer is an error and the effect stays `unknown`. Test: `TestLoadImageRefusalDoesNotSettleWhenThePresenceCheckFails`.
3. **Wider SEC-01 guard.** The `go/ast` guard walks every non-test file under `internal/apprunner/...` and `cmd/app-runner`, package-level declarations included, and fails on any `net.Dial*`, `net.Dialer`, `.Dial`/`.DialContext`, `http.Transport` or `http.Client` literal, `new(http.Transport)`, `http.DefaultTransport`/`DefaultClient`, or `dockerDialer`, outside an explicit allow-list named in the test comment (the constructor and its dialer, `daemonClient`, the broker's egress files, the app-socket clients, the control-socket dial). Allow-listed non-Docker files may not name the Docker host. `TestDockerGuardRulesDetectViolations` shows each rule fires.
4. **Idle clock.** Only upload sessions and load holds refresh it; `Open` and `Missing` no longer do, and the clock is injectable. Tests: `TestReadsNeverRefreshTheIdleClock`, `TestThreeSerializedOperationsEachLoadUnderAnInjectedClock`.

## CI findings on the classic store (Docker 28.0.4, API 1.48, overlay2)

- The daemon accepts the runner's archive with the runner-written legacy `manifest.json` (no tag) next to the OCI files, and reports the classic expected ID (the config digest).
- The post-load check failed with `inspect_mismatch` because the test image's config had no `config` section, so the daemon's inspect returned `Config: null`, which the production check rightly treats as an unprojectable config. The fixture was wrong, not the check: every image from a real builder carries `config`. The test fixture now writes `"config": {}`; the check is unchanged (ID equality, platform and no volumes all stay).
- `inspect_mismatch` now carries a bounded detail naming the failed check (not found under the expected ID, ID differs, platform, projection, volumes), in the snapshot for logs and tests only.
- This daemon ignored a name annotation in a publisher-supplied `index.json`, so the retag test logs that there was nothing to detect; the runner still writes its own index and compares the image list before and after.
- The two web failures seen on this branch (`AdminOverviewPage.test.tsx`, `site.test.tsx`) are in code this branch does not touch; `AdminOverviewPage.test.tsx` also failed in CI on `integration/app-platform`'s base at `c3840350` (run 42169362721), so they are pre-existing flakes and were left alone.

## PR 3a final review: carried into PR 3b

The fourth review round passed. These minor findings are carried into the first steps of PR 3b:

- **SEC-01 guard gaps.**
  - Allow-listed files are skipped entirely, except for a check on the host string.
  - `driver/preflight.go` is not walked.
  - An aliased import of `net/http` or `net` passes the rules.
  - A raw `unix.Connect` plus `net.FileConn` dial is not detected.
  - The self-test re-implements looser rules instead of exercising the guard's own matcher.
- **No terminal outcome when the manifest itself is refused.** The effect answers `blobs_missing` until the package is staged again. That is the intended recovery, but it never ends as `blob_refused`.
- **Config without a `config` section.** The envelope parser accepts it, and the runner refuses it only after loading. Staging should refuse it, because a backend start uses the image's own entrypoint and command.
- **`DaemonDetail` is not logged.** The runner never logs it, despite the note above. Either log it, bounded, or correct the note.
- **Retag detection is unproven on Docker 28.0.4.** The daemon ignored the name annotation, so `TestNativeRetagByAPublisherLayoutIsDetected` could only log that there was nothing to detect. Retag protection rests on the runner writing its own archive with no names, and on the before/after comparison.

## PR 3b implementation notes

Where the plan was silent, or the code made a different choice the safer one, the choice is recorded here.

1. **The load-image effect has no Portal row (deviation).** The plan asks for effect number 1 in stage `stage`
   for every operation. Portal keeps no `app_platform_operation_effects` row for it. Four existing guards
   count the effect rows of an operation and would refuse the extra row: the candidate and data intents
   (`effects == 1`), the managed history read (`count != 3`), the install admission (`resources == 0`) and the
   removal advance rule (`f.stage<>o.stage AND f.state<>'applied'`). Relaxing them would loosen evidence checks
   that exist to refuse exactly an unexpected row. The effect identity is instead derived, so it is the same on
   every attempt, restart and lease takeover:
   `platformDerivedUUID("load-image", <operation id> "/" <release sha256>)`. The runner's journal is the record
   of the effect, under journal stage `stage`, number 1; Portal writes an audit entry when a load settles
   applied. The runner's `sameLoadCommand` does not bind the fence, so a command signed again at a later lease
   fence is the same command. An unknown effect is therefore settled under its own id by the next attempt, and
   no attempt can mint another one.
2. **`enable` and `restore` have no `stage` stage (resolves 3a note 3).** Because there is no Portal row, no
   stage is added to the action's stage list and the journal enum is untouched. The command's journal stage is
   `stage` whatever stage the Portal operation is in, which the runner accepts for any operation. The fail-closed
   part is the gate order, not the stage name: the load runs before the start's own effect row exists.
3. **Where the load runs.** The gate `ensurePlatformReleaseImage` is the first statement of
   `startPlatformWritableInstall` (install writer and writable start), `dispatchPlatformInstallCandidate`,
   `dispatchPlatformMigrateData` (the migration job runs the target image), `executePlatformUpdateStart` (update,
   enable, repair, retry and roll back, which are all updates or same-release lifecycles) and
   `executePlatformRestoreStart` (restore launch and the restore resume of a paused Portal). It is idempotent:
   an applied effect answers a receipt and uploads nothing. `TestEveryStartPathEnsuresTheImageBeforeAnythingElse`
   fails if a start path loses its gate.
4. **The release and platform.** The target release is the plan's `target_release_sha256` (every plan carries
   it). The host platform is the one the review recorded in the managed runtime profile. A release with no
   `backend.images` (a static release, or the development form that names a registry reference) is skipped and
   the runner's inspection handles it.
5. **Every blob is hashed again before anything is sent.** `loadPlatformImageTarget` re-hashes the manifest, the
   config and each layer. A failure deletes the blob and fails the call with `errPlatformImageBlobs` before any
   runner call, which is how "no runner effect for a release whose blobs failed re-hash" holds. The cost is one
   pass over at most 384 MiB per start; a cheaper check would put a corrupt blob into an uncertain effect.
6. **Failure semantics reuse the scheduler.** The gate returns an error; the scheduler already counts an error
   as an attempt and ends the operation for review after three. A load the runner still holds, a `scratch_full`
   or `busy` refusal, and a wait that ran out are `errPlatformImageBusy`, which waits without counting (the
   migration pattern). A start stage of a managed operation now gets the long attempt timeout, because a load
   uploads blobs and the daemon may take minutes. A typed rejection (`errPlatformImageRejected`) and a blob
   failure are definite, but the operation ends through the existing `finish...ForReview` path, whose outcome
   depends on the stage (effects none before `prepare_data`, unknown after). No new failure kind is added.
7. **Storage layout.** `<data>/app-staging/` and `<data>/app-blobs/`. Every name is a 64-hex digest or ends in
   `.partial`; anything else is removed on start. Two deviations, both to keep the rule "the filesystem is the
   index" workable:
   - A download's validator (ETag or Last-Modified, for `If-Range`) is kept in `<hex>.validator.partial`, so a
     restart resumes with `If-Range` as a retry does. It follows the `.partial` rules, so orphans are removed
     with the partial.
   - Unpinned blobs younger than 24 hours survive a sweep. A package is uploaded before the review plan that
     pins it exists, and a sweep after an unrelated operation must not take it. Blobs the failed staging wrote
     itself are removed at once when nothing pins them.
8. **Order of staging.** The offline route streams the body to a staging `.partial` under the 384 MiB bound,
   inspects it in place (`InspectOfflineEnvelopeAt`, a random-access variant of the parser), checks that the
   blobs not yet stored would fit under the 8 GiB cap, writes the blobs, and only then writes the release row.
   A cap refusal leaves no row and no blob; a stored blob that no longer hashes is replaced from the package
   being staged, which is how a re-upload heals PKG-08's corrupt cache. A finished package stays in staging
   under its digest as least-recently-used cache.
9. **Pinned set.** Releases of active and candidate activations, of every recovery set that is not deleted, of
   non-terminal operations (source and target) and of open review plans, expanded to blob digests through the
   stored manifests. It is computed on each sweep; no table exists. A sweep runs on start and when an operation
   ends.
10. **Catalogue.** `fetchToFile` streams into the staging partial with `Range` and `If-Range`. A `200` to a
    ranged request truncates and starts again; a `416` or an oversize file discards the prefix. The package is
    fetched under the request's context with a 10 minute bound, not the 45 second one of the small documents.
    The descriptor and signature of the package must be byte-equal to the catalogue's, or the install refuses
    before any blob is stored. A prefix of another version of the file is caught by that verification and the
    download restarts once from nothing. A catalogue release with `backend.images` and no `package_url`, or the
    reverse, is refused.
11. **Recovery bundle.** The manifest gains `image_blobs` (one file per digest under `app-blobs/<sha256>`) and a
    release's `backend` becomes `in_bundle` (with the digests it needs) or `reference` (a development release
    that names no content). The inventory still reports `pending` until the writer resolves it, and a bundle
    that lists `pending` does not verify. The writer refuses, and removes what it wrote, when a pinned managed
    release is missing a blob or has one that fails its hash. `restorePlatformBundleBlobs` writes each blob to a
    partial and gives it its name only when it hashes to that name. The repository has no production command
    that restores a bundle yet, so this is a library function exercised by tests, like the writer and verifier.
12. **Carried items from the PR 3a final review.**
    - The SEC-01 guard walks every file including the allow-listed ones and `driver/preflight.go`, resolves
      aliased and dot imports, detects `unix.Connect` and `net.FileConn`, and the self-test calls the guard's
      own matcher.
    - A refused manifest now settles `blob_refused`. The signed command binds only the manifest digest, so a
      manifest refusal counts when it matches the digest, the kind, a manifest media type and a size within 1
      MiB. One behaviour changed: a real manifest declared one byte too long used to count for nothing and now
      counts, because the runner cannot tell it from any other wrong bytes without the true size. Portal always
      declares the true size, so only a defective sender is affected.
    - `PlanImage` refuses a config without a `config` object (`ErrImageConfigSectionMissing`), so staging
      refuses it instead of the runner after loading.
    - The runner logs `DaemonDetail`, bounded to 512 bytes and printable ASCII, when it settles a load. It never
      enters a receipt, a row or a reply.
    - Retag detection is unproven on Docker 28.0.4: the daemon ignored the name annotation in a
      publisher-supplied `index.json`, so `TestNativeRetagByAPublisherLayoutIsDetected` could only log that there
      was nothing to detect. Retag protection therefore rests on the runner writing its own archive with no
      names, plus the before/after image-list comparison.
13. **Apps repository.** Branch `feat/offline-envelope-v2-images` (stacked on the contract sync of Apps PR 38):
    `export-image-oci.py` derives `backend.images` and the flat blobs from an OCI layout, `build-app-v2.py
    --images` switches a descriptor to the managed form, `pack-offline-envelope.py --image-blobs` writes version
    2 envelopes, and the descriptor validator accepts `backend.images`. The workflow pin moved to that branch's
    head.
14. **CI.** A new job `deliver-managed-image-package` in `app-project-quota-probe.yml` builds the exact Scheduled
    Digest image with the daemon's default builder, runs `docker save <image-id>` (Docker 25 and later writes an
    OCI layout into the tar), exports it with the Apps tooling (which resolves a manifest named directly by
    `index.json` or reached through a nested index, and gzips layers deterministically when a blob or the package
    would be too large), packs it, runs the real-image acceptance test, diffs the daemon's image list before
    and after, and runs the SEC-01 negatives. No BuildKit builder is created, so there is no new registry pull.

Not verified here (no Docker on the development host, and no CI run yet): the CI job itself, the real `docker save`
output shape through the Apps exporter, every native Linux path, the new Portal tests on PostgreSQL (they
use the SQLite test server and standard SQL through `bind`), and a start of the exact Scheduled Digest image
through the real runner from blobs. The managed image is therefore proven loadable on the real daemon only by the
3a native test of a two-layer image built in the test, not by the exact image.

## PR 3b review record

Named tests per step. Each failure direction is in the test name or its comment.

11. **Stores:** `TestImageStoreRecoverRemovesEveryNameThatIsNotADigestOrPartial`,
    `TestImageStoreRecoverKeepsAFreshPartialAndRemovesAnOldOne`,
    `TestImageStoreBlobThatDoesNotHashToItsNameIsNeverPlaced`,
    `TestImageStoreCorruptBlobIsRefusedAndDeletedOnUse`,
    `TestImageStoreStagingEvictsLeastRecentlyUsedAndSparesHeldAndPartials`,
    `TestImageStoreBlobCapRefusesWhatWouldNotFitAndCountsStoredBlobsOnce`,
    `TestImageStoreSweepKeepsPinnedHeldAndYoungBlobsOnly`,
    `TestPinnedBlobsOfActiveAndRetainedReleasesSurviveEverySweepAndOthersDoNot`.
12. **Offline route:** `TestImageStoreUploadStreamsToDiskAndDiscardsWhatExceedsItsBound`,
    `TestOfflineEnvelopeAtOnAFileMatchesTheBufferedInspectionAndRefusesTheSameDamage`,
    `TestOfflineManagedPackageStoresEveryBlobAndLeavesNoPartialFile`,
    `TestOfflineManagedPackageOverTheBlobCapLeavesNoReleaseRowAndNoBlob`,
    `TestOfflineManagedPackageCapRefusalNeverEvictsAnotherReleasesBlobs`,
    `TestOfflineManagedPackagesWithCraftedLayoutsAreRefusedAndStoreNothing` (tag annotation, `manifest.json`,
    `oci-layout`, `repositories`, an extra member, a missing member),
    `TestOfflineManagedPackageFromAnUntrustedPublisherLeavesNoBlobs`,
    `TestOfflineReuploadReplacesACorruptedBlobAndTheReleaseIsReused`. The old
    `TestPlatformOfflineRefusesManagedPackageUntilBlobsAreStored` was removed: its premise is what this step
    replaces.
13. **Catalogue:** `TestCatalogueInstallOfAManagedPackageIsTheOfflineInstall`,
    `TestCataloguePackageDownloadResumesFromThePartialAfterARestartWithRangeAndIfRange`,
    `TestCataloguePackageStartsAgainWhenTheFileChangedUpstreamSinceThePartial`,
    `TestCataloguePackageWithAnotherDescriptorIsRefusedAndStoresNothing`,
    `TestCataloguePackageOverTheStagingCapIsRefusedBeforeAnyDownload`,
    `TestCatalogueManagedReleaseNeedsItsPackageURL`.
14. **Sender and start gate:** `TestEnsureImageLoadsAMissingImageByUploadingExactlyTheBlobsTheRunnerNamedUnderOneEffect`,
    `TestEnsureImageUploadsNothingWhenTheImageIsAlreadyOnTheHost`,
    `TestEnsureImageSendsTheSameEffectIdOnEveryAttemptAndNeverUploadsAgainOnceApplied`,
    `TestEnsureImageReadsBackAnUncertainExecuteUnderItsOwnIdentityAndTakesItsReceipt`,
    `TestEnsureImageReportsABusyRunnerAsBusyNotAsAFailure`,
    `TestEnsureImageSurfacesARunnerRejectionAsADefiniteFailure`,
    `TestEnsureImageStopsOnTheRunnersTypedRefusalOfABlob`,
    `TestEnsureImageResumesALostUploadWithoutANewEffect`,
    `TestEnsureImageSendsNothingToTheRunnerWhenAStoredBlobFailsItsHashCheck`,
    `TestEnsureImageDoesNothingForAReleaseThatCarriesNoManagedImage`,
    `TestEnsureImageDoesNotBelieveAReceiptForAnotherEffect`,
    `TestMigrateDataIsNotSentWhenTheReleaseImageCannotBeLoaded`, `TestMigrateDataRunsAfterTheImageIsLoaded`,
    `TestUpdateStartIsNotReservedWhenTheReleaseImageCannotBeLoaded`,
    `TestEveryStartPathEnsuresTheImageBeforeAnythingElse`. The gates of the restore start, the writable install
    start and the candidate start are covered by the structural test only; their behaviour is the same call.
15. **Recovery:** `TestRecoveryBundleCarriesTheBlobsOfEveryActiveAndPinnedManagedRelease`,
    `TestRecoveryBundleIsRefusedAndRemovedWhenAPinnedManagedReleaseHasLostABlob`,
    `TestRecoveryBundleVerificationRefusesDamagedRenamedAndUnresolvedBlobs`,
    `TestRestoreOfBundleBlobsPlacesOnlyBlobsThatHashToTheirNames`,
    `TestRestoredManagedReleaseIsLoadedFromThePlacedBlobsBeforeItsStart`.
16. **Acceptance:** `TestRealImagePackageDeliveryOfflineCatalogueAndStorageScenarios` (CI only, skips without an
    exported image) and the `deliver-managed-image-package` job.
    Carried items: `TestDockerGuard*` and `TestOnlyTheDockerClientConstructorDialsTheSocket`,
    `TestAManifestRefusedUnderTheExactRoleCountsAgainstTheImage` and the other manifest refusal tests,
    `TestPlanImageRefusesAConfigWithoutAConfigSection`,
    `TestLoadImageLogsTheBoundedSanitizedDaemonDetailAndKeepsItOutOfTheReceipt`.
