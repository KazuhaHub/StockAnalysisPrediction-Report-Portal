# Frontend artifact inspection and local inspector

Status: P02 development tooling. This does not approve D3/D4 or install an application.

## Purpose and executable example

The v2 descriptor names a frontend ZIP and its exact SHA-256. The new inspector checks that artifact, inspects every accepted asset and prints public metadata. It never extracts an archive, starts a service, executes application JavaScript, modifies a database or contacts a network destination.

From the Portal source checkout:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 go run ./cmd/app-inspect \
  -descriptor /path/to/descriptor.json \
  -frontend /path/to/ui.zip
```

Both inputs must be bounded regular local files; symbolic input paths and directories are refused. Relative paths are resolved from the caller's working directory. The command does not obtain files from the market; a future authenticated fetcher/offline-envelope inspector must supply the same bytes to this validation layer.

Success exits 0 with JSON containing the application ID, release version, publisher declaration, exact descriptor digest, static/managed declaration and frontend metadata. Its `verification` field is deliberately `frontend_integrity_only`. Failure exits nonzero and reports a fixed safe diagnostic; validation failures do not print partial success JSON, local input paths or file bodies. Output-device failure also returns nonzero, even if a partial write occurred.

An unsigned release with a self-declared hash can pass integrity inspection. That does not establish publisher trust or installation authority. This command must never be used as the sole admission gate or labelled as a fully verified application installer.

## Bounded archive format

Provisional limits, subject to D3 review:

| Limit | Maximum |
| --- | --- |
| Compressed archive bytes | 16 MiB |
| Uncompressed regular asset | 8 MiB |
| Total uncompressed regular assets | 64 MiB |
| Central directory entries, including directories | 2,048 |
| Relative asset path | Existing descriptor path contract: 256 ASCII characters |

Independent limits matter. The compressed-byte cap alone would still allow a large allocation of metadata for tiny records. Before `archive/zip` allocates per-entry objects, a bounded central-directory pass verifies record count, lengths, offsets and the exact terminal directory structure. Advertised and actual decompressed bytes are checked separately. The aggregate compressed-stream budget cannot exceed the archive bytes, so overlapping stream claims cannot multiply decompression work without bound.

The initial artifact format supports ordinary stored and deflated ZIPs, bounded comments and explicit or implicit directories. Archive-level ZIP64 directories, multidisk archives, executable prefixes and trailing bytes are refused. They are unnecessary within the current size/count bounds. Per-entry extra fields are still handled by the standard ZIP reader and are subject to the same resulting size/path/mode checks. Supporting a future archive format requires new bounded parser/refusal tests rather than removing this preflight.

All paths must already be canonical. Do not repair paths by cleaning, stripping prefixes or URL-decoding before duplicate detection. Refuse traversal, absolute paths, backslashes, encoded separators, non-ASCII paths, duplicate names, case aliases and file/directory ancestry collisions. Explicit directory records may follow previously implicit directories if their spelling and type agree; duplicate explicit records are refused.

Only regular files and directories are accepted. Symlinks, devices, pipes, executable regular-file permission bits, setuid/setgid/sticky modes and contradictory type metadata are rejected, including special modes hidden behind a trailing `/` name. Encryption, unsupported flag bits and unsupported compression methods are refused before opening streams.

The standard ZIP reader treats directory opens as empty. The inspector therefore also validates the raw directory stream, checks zero uncompressed size/CRC and refuses hidden payloads, while permitting a valid compressed empty directory. Regular file streams are read to checksum completion under hard byte limits. An advertised size or CRC is never treated as proof that its data is valid.

## Assets and output

The descriptor entry must resolve to a nonempty packaged `.html` or `.htm` asset. Other regular assets use a fixed extension/content-type allowlist, independent of OS MIME registries and content sniffing:

- HTML, classic JavaScript and CSS.
- JSON/source maps and plain text/Markdown/CSV, served as data.
- SVG, PNG, JPEG, WebP, GIF and icon images.
- WOFF/WOFF2/TTF/OTF fonts and WebAssembly data.

Unknown types and server scripts are refused. This is not a semantic analysis of JavaScript/HTML or proof that an archive contains no secrets; packaging review and publisher trust remain necessary. Adding a media type requires an explicit serving-policy/test change.

The result sorts regular file metadata by canonical path. Each entry contains path, deterministic content type, actual byte count and SHA-256. Directory records and file bodies are excluded. Any validation failure returns the zero inspection result.

Future asset serving must rebind the staged bytes to these verified digests, use `nosniff`, and apply an opaque-origin CSP sandbox to every response, including direct top-level HTML/SVG navigation. Inspection does not make same-origin execution safe. No serving route or change to the legacy `/app-assets` handler is part of this work.

## Tests and remaining work

Tests were added and observed failing before implementation. The first implementation also failed additional adversarial tests for special directory modes, which were then fixed. Coverage includes valid stored/deflated assets, deterministic metadata, digest mismatch, CRC corruption, malformed central/local records, path collisions, empty/missing/wrong-type entry, encrypted/unsupported records, advertised size lies, independent quotas, compressed directory payloads, media types and safe CLI output/failure.

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 go test -race -cover ./internal/appplatform ./cmd/app-inspect
GOWORK=off GOTOOLCHAIN=go1.26.10 go vet ./internal/appplatform ./cmd/app-inspect
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test ./internal/appplatform -run '^$' -fuzz FuzzFrontendInspection -fuzztime=4s -parallel=1
GOWORK=off GOTOOLCHAIN=go1.26.10 go build ./...
```

A single-iteration darwin/arm64 development benchmark with 1,024 small assets
measured about 34.9 MB of cumulative allocations before scratch-buffer reuse and
1.37 MB after it. These are local allocation observations, not peak RSS or a
production throughput guarantee. The benchmark remains available for review:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test ./internal/appplatform -run '^$' -bench BenchmarkFrontendManySmallFiles -benchtime=1x -count=1
```

Remaining work includes the complete signed/offline envelope, OCI artifacts, publisher trust and revocation, generated schemas and packaging-tool parity, verified durable staging, compatibility/capability/data/resource admission, and real installer/runtime integration. The legacy ZIP parser and installed apps are unchanged. No server release/channel configuration changes are required to use this local tool.
