# Descriptor fixtures

`descriptors/static.json` and `descriptors/managed.json` are public, synthetic,
provisional-format fixtures. The repeated digests and example image identify no
verified published code. Never execute or download their declared artifacts.

Go tests load these files and derive hostile mutations without changing the
original fixtures. The parser hashes exact input bytes; test fixture compaction
is only a helper for stable mutation targets. Signature verification must use the
actual descriptor bytes, not that helper's serialization.

The future SDK/schema validator must consume the same corpus before claiming
cross-language compatibility. Successful parsing is not signature verification,
compatibility evaluation, permission admission or platform acceptance.
