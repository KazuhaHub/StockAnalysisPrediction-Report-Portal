# Browser acceptance through actual Portal HTTP and SDK code

This adds a reproducible development acceptance fixture for the
[authorized launch/document routes](41-authorized-document-http.md). It combines
the actual central Portal route table, signed session verification and temporary
SQLite records with the actual Portal `AppFrameV2` and Apps React/Ant `AppRoot`.
It does not modify production startup, product pages, real credentials or server
beta configuration. It does not complete the installer or its physical receipts.

## Reproduce

Use the Portal checkout containing this fixture and the Apps checkout containing
the experimental SDK root. Install their ordinary documented development
dependencies first. Then run:

```sh
python3 scripts/app-document-browser-fixture.py \
  --apps-dir /absolute/path/to/StockAnalysisPrediction-Report-Portal-Apps \
  --portal-http
```

The script prints `PROBE_URL=http://127.0.0.1:<temporary-port>/`. Open that URL
through the browser plugin. Keep the process running during the checks below.
Finish with Ctrl-C in its terminal. The script writes a stop marker, waits for
the test server to finish, closes the loopback listener and removes the temporary
build/state/log directory. No Docker, existing Portal deployment or PostgreSQL
configuration is needed.

Without `--portal-http`, the original compiler-only fixture remains available.
That mode proves resource compilation/presentation, not actual Portal HTTP auth.
Do not confuse its source or response policy with the new mode's live handlers.

## What is real and what is a fixture

1. Build the real Apps SDK root and an interactive React/Ant child, plus the real
   Portal frame host, as classic bundles in a temporary directory. The child is
   configured with the exact trusted loopback parent origin before packaging.
2. Start the opt-in `TestPlatformHTTPBrowserFixture` in a test binary. It creates
   a fresh isolated SQLite store and stages a real signed descriptor/frontend ZIP
   containing those built child files, with the existing test publisher identity.
3. Admit the ordinary installation review/intent, then explicitly seed coherent
   **test-only** completed operation, activation and namespace records using the
   shared launch fixture. This is not a provider receipt, actual storage setup,
   execution readiness proof or installer completion implementation.
4. Create a normal test reader and audience grant. Its signed synthetic cookie
   and loopback server URL are written to one exclusive mode-0600 temporary state
   file. No real user's session or key is read. Do not print/copy that cookie into
   source, screenshots or release evidence; the script handles it locally.
5. A minimal loopback reverse proxy serves only the development host/assets and
   forwards the actual launch/document requests to the Portal test server. It
   forwards the browser's fixture cookie/Origin and preserves the actual Portal
   response status, CSP, cache, referrer and content-type headers. It does not
   substitute a precompiled HTML success response or perform its own app grant
   decision. The real Portal handlers authorize and compile each document.
6. The host fetches the real launch JSON and uses its returned source/binding in
   `AppFrameV2`. The real signed ZIP is compiled under current authorization;
   browser resources and bridge negotiation then reach the real SDK root.

The synthetic session is issued by fixture code, so this does **not** test the
password/SSO/OTP login ceremony. Cookie verification, persisted session state,
current audience checks and the actual document responses are real code paths.
Only the test binary contains `/__fixture/` controls. They require the configured
parent Origin for mutations and affect only the known temporary account/app.
No production route accepts these controls or the fixture's state-file settings.
Normal Go runs skip the listener unless all explicit fixture settings are supplied.

## Observed browser sequence

The local in-app browser, controlled through the browser plugin, used the Portal
HTTP source at `6e12d0f40c59a70e4756ab76c5463860aa1f297d` plus these test-only
fixture changes and Apps SDK source
`101bdad9143937a4d8ce65e0c19167dc05d3e0ce` (clean checkout). It showed:

| Step | Observed result |
| --- | --- |
| Open printed host URL | `Launch HTTP 200`; real private `/app-documents/...` frame displays `zh-CN / Asia/Shanghai / 0` |
| Click the child's Ant button | Counter becomes `1` |
| Set traditional Chinese in the host | Child becomes `zh-TW / Asia/Taipei / 1`; state remains in the same app |
| Check last document | `Document HTTP 200` from the actual protected endpoint |
| Refresh the host | Fresh authorized launch/document and SDK handshake; child returns to its initial local state |
| Revoke fixture access, then check old document | `Document HTTP 404`; the old source is not a reusable access permit |
| Reopen app after grant revocation | `Launch HTTP 404`; no frame is mounted by this fixture host |
| Restore fixture access and reopen | `Launch HTTP 200` and a fresh live SDK app |
| Revoke fixture session, then check old document | `Document HTTP 401` |
| Reopen and refresh with that revoked cookie | `Launch HTTP 401`; refresh does not recreate/revive the server session |
| Navigate directly to the old document after session revocation | Existing `session_expired` JSON refusal; no application document |

Revoking access does not retract already loaded JavaScript or erase data already
rendered in a browser. It denies the next launch/document read. The fixture's
Reopen deliberately unmounts the previous frame before fetching new metadata;
this is not implementation evidence for a future live invalidation subscription.
The sensitive broker must separately authorize every dispatch when implemented.

## Scope and evidence limits

This closes the previously missing local browser combination of the actual HTTP
handlers and SDK presentation for the listed sequence. It does not close all
UX/security acceptance cases. Still required: a real market install with actual
provider activation, production shell integration, large-counter URL cases in a
browser, failures/timeouts, superseded generations, full sizing/navigation/drafts,
keyboard/accessibility and multiple-browser verification, pixel-level no-flash
checks and D5 visual review. Tests of request binding, cancellation and response
policy remain in the HTTP source suite; this page is not a substitute for them.

The self-navigation/exfiltration limitation in document 40 remains unresolved.
No new sensitive broker capability, public SDK freeze or production deployment
is authorized by a local fixture result.

Changes here are development tooling/tests/docs only. Existing shared launch
fixtures retain their original behavior with no archive argument. The new archive
variant stages a second immutable signed release rather than editing an existing
release or its digest. Targeted reader/HTTP regression, race checks, Go vet,
staticcheck and Python syntax checks accompany the browser run; exact-source CI
must still be checked before accepting this evidence change.

The prior Portal HTTP source passed
[CI 40270414095](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40270414095).
The local fixture exited successfully through its normal stop path, with the
Go test process completing and the temporary directory/listeners removed.
