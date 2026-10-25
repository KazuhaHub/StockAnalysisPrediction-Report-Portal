# Downloadable apps

The proposed next-generation platform is specified in the
[application platform v2 delivery plan](../docs/app-platform/README.md).
It covers complete packages, managed runtimes, seamless presentation and full
lifecycle management. It is a proposal, not a replacement for the implemented
legacy contracts documented below. No existing installation is changed by it.

Runtime-installable, sandboxed iframe apps for the portal. See the design in
[docs/adr/0003-downloadable-apps.md](../docs/adr/0003-downloadable-apps.md).

An **app** is a `.zip` bundle: an `app.json` manifest plus self-contained frontend
files. An admin installs it under **Manage → Apps** — either by uploading the `.zip`
or one-click from the **App market** (the shared repository's
[`index.json`](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/blob/main/index.json)) —
and it then appears as a card in the **Apps** hub for every user and opens inside a
sandboxed `<iframe>`.

The source/distribution repository is
[StockAnalysisPrediction-Report-Portal-Apps](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps).
This directory retains the legacy catalogue and demo archive for older binaries
and explicit overrides. It is not removed when the compiled default changes.
See [the migration runbook](../docs/app-market-migration.md) for upgrade behavior,
explicit legacy export and rollback.

## Maintained application development standard

New and maintained applications are authored in the shared Apps repository using
React, TypeScript, Ant Design and `@portal-app/sdk`. Follow its
[development standard](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/blob/main/docs/plugin-development-standard.md)
and start from the tested generator/template rather than copying this directory's
historical single-file demo or handwritten bridge messages.

From that repository's root:

```sh
python3 scripts/create-app.py my-app --name "My app"
npm install
python3 scripts/plugin-standard.py
npm run typecheck
npm test --workspace=apps/my-app/frontend
python3 scripts/build-app-frontend.py my-app
python3 scripts/catalogue.py package my-app
```

The SDK supplies trusted parent-window request handling and the host theme snapshot.
The host includes its selected panel language as a top-level `locale` field in
`rp:init` and `rp:theme`. Maintained apps follow it by default and receive later
language changes without a frame reload or loss of unsaved drafts. The SDK maps
Portal's `en` code to its supported `en-US` locale; older hosts omit this optional
field and retain the documented browser-language fallback. App-specific explicit
language overrides remain an app UI choice rather than a second required setup.
Use its `PortalAppProvider` for Ant Design tokens/locales. Keep the initial page
focused on the reader's task, configuration behind an administrator entry, advanced
fields collapsed, and validation errors/drafts inside the active dialog. Include
light/dark, narrow-layout, keyboard, locale, authorization and lifecycle checks.

Each ZIP bundles its own compiled dependencies. Sharing a UI foundation does not
load code into Portal's React tree or relax the null-origin iframe sandbox. Optional
backends retain independent deployment, storage and keys and use the reviewed
viewer-bound extension connection. No app may read host cookies/DOM or patch host
business pages. Existing third-party ZIP protocol compatibility is unchanged;
the shared repository enforces its maintained-source standard before publication.

## Manifest (`app.json`)

```json
{
  "id": "demo-symbols",     // slug: a-z 0-9 _ - (also the URL + asset path)
  "name": "Symbols demo",
  "icon": "🔎",             // optional emoji shown on the card
  "version": "1.0.0",
  "entry": "index.html",    // HTML entry point inside the bundle
  "scopes": ["query"]       // Capabilities: query (read), ingest (report writes), notify (publication)
}
```

An admin sees the requested scopes and approves them at install (the permission
prompt). `query` grants `GET /api/v1/*`; `ingest` additionally grants `POST`/`DELETE`
report writes. The catch-all `all` scope is never granted to an app.

## Bridge wire protocol reference

Use the shared SDK when authoring an app. The messages below document the wire
contract for host compatibility; they are not a complete client implementation.
In particular, a client must validate the parent source, bind replies, bound
pending work/timeouts and dispose its listeners correctly, as the SDK does.

The iframe runs with `sandbox="allow-scripts"` — a null origin, so it cannot read
the host DOM, the session cookie, or localStorage. It reaches the portal only by
posting a message to its parent; the host validates the request against the app's
granted scopes and performs the `/api/v1` call with a short-lived scoped token the
iframe never sees.

```js
// request  →  host   (a write needs the "ingest" scope + a body)
parent.postMessage({ type: 'rp:api', reqId: 1, method: 'GET', path: '/api/v1/symbols?limit=20' }, '*')
parent.postMessage({ type: 'rp:api', reqId: 2, method: 'POST', path: '/api/v1/reports', body: { /* … */ } }, '*')

// host  →  app
window.addEventListener('message', (e) => {
  const m = e.data
  if (m.type === 'rp:api:result') { /* m.reqId, m.ok, m.status, m.data */ }
  // rp:init arrives once on load; rp:theme re-arrives on every theme change, so the
  // app can follow the host's light/dark live. m.theme = { dark, colorPrimary, colorBg,
  // colorText, colorBorder, colorBgLayout, borderRadius }
  if (m.type === 'rp:init' || m.type === 'rp:theme') { applyTheme(m.theme) }
})
```

## The demo

[`demo-symbols/`](demo-symbols) is a one-file app that lists stocks with reports.
Install `demo-symbols.zip` under **Manage → Apps** to try the whole chain:
empty hub → install → a card appears → open it → it reads the API through the bridge.

The archive is retained for older binaries and explicit legacy catalogue overrides.
Do not rebuild or replace its published bytes to migrate styling. The maintained
React demo source lives in the Apps repository; publish a distinct immutable
version there using its normal release and catalogue checks.

### Notification publication capability

An app may declare `notify` to request source-owned notification publication when
opened by an administrator. It uses the existing `rp:api` bridge with
`POST /api/v1/notifications`; query or ingest permission does not imply this
capability. The host token is bound to the app and issuer, and the server checks
installation, current declaration and issuer eligibility on each publication.
The app cannot choose recipients, destination URLs or a native event name.
See [the publication contract](../docs/notification-publishing-api.md) for setup,
request bounds, idempotency and background-service guidance.
