# Descriptor fixtures

`descriptors/static.json` and `descriptors/managed.json` are public, synthetic,
provisional-format fixtures. The repeated digests and example image identify no
verified published code. Never execute or download their declared artifacts.

Go tests load these files and derive hostile mutations without changing the
original fixtures. The parser hashes exact input bytes; test fixture compaction
is only a helper for stable mutation targets. Signature verification must use the
actual descriptor bytes, not that helper's serialization.

`descriptor-corpus.json` is the shared 44-case Go/TypeScript corpus. Inputs are
base64 bytes so malformed UTF-8 and duplicate raw keys survive JSON transport.
Both repositories pin its exact-byte digest; `.gitattributes` forces LF checkout.
The SDK tests consume the same corpus and acceptance results. Generated schema
validation and broader parity coverage remain open. Successful parsing is not signature verification,
compatibility evaluation, permission admission or platform acceptance.

Publisher verification uses the separately hash-pinned `publisher-signatures.json`
corpus and an RFC 8032 test key. See
`docs/app-platform/24-publisher-signature-verification.md` for regeneration and
trust boundaries. These fixture keys must never be enrolled in production.
