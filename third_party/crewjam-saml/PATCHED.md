# Patched copy of github.com/crewjam/saml

This directory replaces `github.com/crewjam/saml` through the `replace` directive in the
repository's `go.mod`. It exists only because the latest release carries a reachable
vulnerability with no fixed version.

## Provenance

- Base: `github.com/crewjam/saml` v0.5.1, copied byte for byte from the Go module cache.
  Only what this repository compiles is kept: the root package, `logger` and `xmlenc`,
  without tests, plus `go.mod` and `LICENSE` (BSD 2-Clause, retained as required).
- Patch, part 1: the source hunks of upstream pull request crewjam/saml#674 (head commit
  `2b3cdad337cd`), applied to `schema.go` and `service_provider.go` and reviewed line by line.
- Patch, part 2 (ours, beyond #674): `Assertion.AuthnStatements`,
  `Assertion.AttributeStatements`, `AttributeStatement.Attributes` and `Attribute.Values` are
  bound to the SAML assertion namespace in `schema.go`. #674 left them bare-tagged, so a
  foreign-namespace attribute was still read as a real one, and attributes drive the portal's
  SSO role and organizational-unit rules. Offer this upstream alongside #674.
- Nothing else differs from v0.5.1.

## What the patch fixes (GO-2026-6909)

1. `ServiceProvider.validateAssertion` dereferenced `Subject`, `Conditions` and
   `SubjectConfirmationData` without nil checks, so a signed assertion that omitted them
   panicked after signature verification. They are now refused with an error, as is a
   `LogoutResponse` without an `Issuer`.
2. `Subject.NameID` and several other assertion elements used bare XML tags, so an element
   with the same local name in any namespace was accepted. A signed assertion carrying a
   foreign-namespace `<NameID>` therefore set the subject the portal keys accounts on. Those
   fields are now bound to the SAML assertion namespace.
3. Part 2 above applies the same namespace binding to attribute and authentication
   statements.

`internal/app/saml_assertion_shape_test.go` drives a genuinely signed response through the
service provider for each case and fails on unpatched v0.5.1.

Upstream's own suite, run against this copy, passes except for three serialization tests:
`TestLogoutRequestXMLRoundTrip` and `TestParseXMLArtifactResponse` expect the namespace now
written on `NameID` and `Conditions` (#674 updates both), and `TestAttributeXMLRoundTrip`
fails because `Attribute.Element()` emits an undeclared `saml:` prefix, so its output no longer
round-trips through `xml.Unmarshal`. The service-provider path is unaffected: it parses
IdP-authored XML, which declares its prefixes.

## Keeping the vulnerability gate meaningful

The `replace` makes crewjam an unversioned directory that no advisory matches, so the main
govulncheck step no longer sees it. `scripts/govulncheck-released-saml.sh`, run in CI next to
that step, scans the released module instead and fails on any other advisory reaching our code
and once GO-2026-6909 lists a fixed version.

## Open question for upstream

`elementToBytes` in `service_provider.go` gathers every namespace prefix used inside the
verified element into one map and redeclares them on the copy's root, so a prefix bound to
different URIs in different scopes keeps only one binding. This is unverified as an exploit and,
like the advisory, would need content the IdP signed; report it upstream rather than patching it
here.

## Removing this copy

Delete this directory and the `replace` line once crewjam/saml publishes a release containing
the fix (GO-2026-6909 lists a fixed version), bump the `require` to it, and keep the
regression test. If that release lacks part 2, `TestSAMLForeignNamespaceStatementsAreIgnored`
fails and the copy stays until it is upstream too. Do not edit files here for any other reason; a further fix belongs upstream
first and is recorded in this file if it must be carried.
