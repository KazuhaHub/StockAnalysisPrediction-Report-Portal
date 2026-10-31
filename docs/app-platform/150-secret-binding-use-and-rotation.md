# Reviewed credential binding, use and rotation

Status: H06 development implementation, 2026-10-30.

## Binding review

Binding admission reuses the existing consumed `app_platform_review_plans`
record and adds no data table. The plan is a closed
`portal.secret-binding-review/1-dev.1` object containing:

- installation/app/publisher/generation/release and exact installation, policy
  and trust revisions;
- binding identity, expected binding revision, stable lowercase name and
  operation;
- credential identity, non-secret label, metadata revision and exact value
  revision;
- the canonical current network-policy SHA-256 and reviewing administrator
  authority.

Preview stores canonical plan bytes and their SHA-256 with a five-minute expiry.
Consume locks the review, installation, policy history and binding before
checking the current administrator, preview digest, network grant, credential
revision, release/trust identity and install state. Any drift refuses without
writing. A new binding is inserted active; a rotated/review-required binding is
reactivated only by the new exact plan.

Only `http_bearer` and `http_api_key` are callable. `raw_read` remains
representable but refused. `secrets.read` is not an installable provider and no
plaintext-read endpoint exists.

## Private use boundary

An app may send the stable binding name with an ordinary `network.http` request.
It never sends a credential value, header name, actor, policy or review identity.
Portal:

1. reconstructs the current viewer/session/installation authority;
2. requires current `network.http` and `secrets.use` grants;
3. loads the active binding by installation and name;
4. checks the canonical binding policy against the current network digest,
   policy revision and consumed binding review;
5. checks the credential is active at the exact reviewed value revision;
6. decrypts only inside the private request transaction; and
7. projects exactly one `Authorization: Bearer ...` or `X-API-Key` header.

The fixed header travels only inside the existing signed, size-bounded command
over the peer-authenticated runner Unix socket. It is not stored, sent to the
browser, put in URLs, logged, included in runner environment/arguments or
returned in ordinary response metadata. Portal and runner clear owned mutable
copies on completion.

The runner refuses unsupported headers and strips all upstream headers, cookies
and redirects. If a response body or content type echoes the injected value, the
result becomes `uncertain` with no body released. Portal reconstructs current
session, grants, policy, binding and credential revision before releasing the
result; revocation or rotation after dispatch withholds the response.

## Rotation and revocation

A bound credential change uses a separate exact
`portal.credential-rotation-review/1-dev.1` plan. Preview lists every affected
binding identity/installation/revision/state sorted by ID. Consume locks
installations in ID order, then the credential, then the bindings:

- replacement increments metadata/value revisions, reseals the new value and
  marks each affected binding `review_required`;
- revocation increments revisions, erases ciphertext and revokes every affected
  binding with one revocation instant;
- zero affected bindings can use the direct unbound custody path;
- any changed affected set, revision or digest refuses the complete transaction.

Binding-only revocation is immediate and audited. Uninstall continues to revoke
all installation bindings at admission. Reinstall and restore cannot inherit
old use authority.

## Administration

The management surface exposes create/list credential metadata, binding
preview/consume/list/revoke and reviewed credential replacement/revocation. It
never returns ciphertext or credential bytes. The application detail page adds a
generic credential/binding page for installed apps with create, binding
reapproval, rotation and revocation controls in all three Portal languages.

Credential secrets cross only the authenticated management request and the
sealed database write. Forms clear replacement values after submission and do
not retain them in localStorage.

## Verification

Focused tests cover:

- canonical binding policy and network digest drift;
- exact review changes, expiry, demotion and replay;
- active/review-required/revoked lifecycle and shared credential isolation;
- value replacement with dependent binding pause and explicit reapproval;
- credential and binding revocation, including uninstall;
- fixed header shapes, command clearing and private runner injection;
- response body/content-type echo denial and no automatic retry;
- management HTTP secret non-disclosure and app-ID installation resolution;
- Portal and Apps SDK credential-name parsing, capability gating and hook
  forwarding; and
- existing restore-pause and revocation-floor tests.

Real native browser acceptance, restored credential inspection and complete CI
remain H06 evidence gates. No real credential or production provider is used.
