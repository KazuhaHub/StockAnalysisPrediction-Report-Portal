# Development static offline envelope

Status: H15 development implementation, 2026-11-02. This format is not yet a
public package freeze and does not approve managed offline image payloads.

## Format

The initial `.portalapp` file is a bounded ZIP with exactly three regular files:

```text
descriptor.json
descriptor.sig
blobs/<frontend-sha256>/<declared-frontend-file-name>
```

The frontend directory and file name must match the signed descriptor. The
archive contains no top-level directory records, executable modes, symlinks,
devices, unknown members, case aliases, duplicate names, trailing data, ZIP64 or
multi-disk archives. The envelope may exceed the descriptor, detached signature
and frontend limits combined by at most 1 MiB, with at most 64
central-directory records.

The detached signature is parsed with the same closed development schema used by
Portal publisher verification. Inspection alone does not establish publisher
trust. Portal staging repeats the current trust, key validity, namespace,
signature, descriptor digest, frontend digest and immutable release checks before
persisting anything.

## Authoring and inspection

The Apps packer accepts already signed bytes and never reads a private key:

```sh
cd Portal-Apps
python3 scripts/pack-offline-envelope.py \
  --descriptor dist/notes/descriptor.json \
  --signature dist/notes/descriptor.sig \
  --frontend dist/notes/frontend.zip \
  --output dist/notes/notes.portalapp
```

The output is deterministic and is never replaced after it exists. Portal can
inspect it without executing code or writing state:

```sh
cd Portal
GOWORK=off GOTOOLCHAIN=go1.26.10 go run ./cmd/app-inspect \
  -envelope dist/notes/notes.portalapp
```

The verification label is
`offline_envelope_frontend_integrity_only`. It intentionally does not claim
publisher trust, compatibility admission or permission review.

## Portal staging

An authenticated administrator posts the raw ZIP to:

```text
POST /api/admin/app-platform/market/offline
Content-Type: application/zip
```

The request body is bounded before parsing. The endpoint uses the same current
actor, management-work budget and `stagePlatformReleaseContext` transaction as
the online market. It creates only immutable release/frontend rows; installation
still requires the ordinary exact audience/permission review and commit.

Successful static offline bytes therefore use the same review, operation,
activation, launch and uninstall paths as the market. Managed descriptors are
refused with `ErrManagedOfflineNotReady` until the signed OCI image layout is
implemented and verified.

## Verification

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -p 2 ./internal/appplatform -run '^TestOfflineEnvelope' -count=1
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -p 2 ./cmd/app-inspect -run '^TestOfflineInspector' -count=1
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -p 2 ./internal/app -run '^TestPlatformOfflineMarketHTTP' -count=1
python3 -m unittest scripts.tests.test_offline_envelope -v
```

Tests cover descriptor/frontend binding, invalid and revoked trust at staging,
duplicate/extra/traversal/symlink/executable members, wrong blob digest, changed
frontend bytes, managed refusal, content-type and anonymous refusal, idempotent
replay, complete static install and PostgreSQL transaction execution.

## Remaining

- Define and verify the signed OCI image layout and platform digests required for
  a genuinely self-contained managed envelope.
- Freeze the public envelope format, distinguish development and release format
  names, and add cross-version compatibility fixtures.
- Add an administrator upload control if the product workflow requires browser
  upload rather than the reviewed management API.
