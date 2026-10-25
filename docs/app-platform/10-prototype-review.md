# Interactive application-platform prototype

Status: P01 in progress. This is a local interaction prototype, not a released
application market or the final sandbox/runner integration. It uses two neutral
reference apps and synthetic in-memory state. No Status configuration or business
service is involved.

## Open the prototype

Use the implementation checkout containing `web/app-platform-prototype.html`.
From its `web/` directory, with the repository's supported Node runtime:

```sh
npm ci
npm run dev -- --host 127.0.0.1 --port 5179 --strictPort
```

Open `http://127.0.0.1:5179/app-platform-prototype.html#/market`.
No Go server, login, production cookie, database, Docker or credentials are needed.
The server is loopback-only. If that port is already occupied, select another
unused local port and adjust the URL; do not stop another person's development
server. Stop this preview's Vite process when it is no longer needed.

The HTML entry is separate from the production `index.html`. It is not imported
by `App.tsx` and its entry rejects non-development execution. The ordinary
production build was checked to exclude the prototype HTML/content. Existing
production routes, installed apps, API handlers and server configuration are
unchanged. The prototype must not be added to the normal navigation as a shortcut
to claiming a working managed marketplace.

## What can be reviewed

- My apps, searchable market, app information and a single host-owned page title.
- Static Quick Notes and managed Daily Digest examples.
- Required permissions, optional notification permission and an explicit app-use
  audience choice. Optional publication defaults off and is not delivery setup.
- Simulated install stages, first-use configuration and opening app content.
- Update, disable/enable, retained-data uninstall, separate typed data deletion,
  and restoring an available sample pre-update recovery point.
- Unsaved draft protection, host back/forward navigation and localized controls.
- Administrator/reader, light/dark, runtime availability and download/readiness
  failure scenarios through the collapsed **Preview controls** section.

Preview controls are development tools outside the product/app content. Their
language/theme controls simulate the panel presentation; embedded applications
have no independent language selector. Changing preview roles is not a real login
or a production authorization mechanism.

## Suggested review sequence

1. Open Market. Observe installed Quick Notes and uninstalled Daily Digest.
2. Open app information. Review publisher, version, execution and resource fields.
   All verification/resource indicators are sample declarations, not real admission.
3. Install Daily Digest. Inspect required storage/background permissions and the
   optional notification permission. Cancel once to verify nothing is installed.
4. Review again and confirm. Observe the simulated finite stages. Moving around
   this still-open prototype does not cancel the local operation.
5. Open setup and enter a sample digest name. There is no internal URL, container
   command or signing-key field. The prototype does not need service credentials.
6. Open Quick Notes, type a draft, try navigating away and cancel. Change preview
   language/theme and verify the draft remains. Save it for this preview session.
7. In Manage apps update Quick Notes. Its sample version changes and its previous
   state is available as a recovery point. Restore deliberately and inspect the
   data-loss warning; this is an in-memory demonstration, not a real backup.
8. Disable and enable it. The version/data stay unchanged and disabled content
   cannot be opened through the ordinary UI.
9. Uninstall and retain its data. Use the separate retained-data action and type
   the app name to delete only that sample state.
10. Reset the preview and select a download/readiness failure. Confirm failure is
    visible, installation never becomes usable, and a failed update keeps old data.
11. Select an unavailable runtime. Managed installation is blocked before review;
    static app installation remains possible. Installed managed content cannot
    pretend the unavailable runtime is healthy.
12. Select Reader. Management and installation actions disappear; hidden app deep
    links do not render their content or disclose installation status in the market. Reset affects sample state only.

Refresh resets every synthetic installation, note and operation. Closing the
browser stops the prototype. The real platform must support durable operations
and independent background work; this prototype does not prove those properties.

## Verification recorded

The model and component tests were written and observed failing before their
implementations, including the explicit empty-search result.

```sh
npx vitest run src/prototypes/app-platform/Prototype.test.tsx src/prototypes/app-platform/model.test.ts --maxWorkers=1
npm run typecheck
npm run lint
npm run build
```

Seventeen focused tests pass: managed review/setup, no runtime, reader refusal,
startup failure, empty search, draft preservation/navigation guard, retained-data
deletion, update failure, disable/enable and locale parity. All 127 UI keys and
interpolation placeholders match across en-US/zh-CN/zh-TW. Tests prohibit API calls.
Typecheck/build pass; targeted prototype lint has no findings. Whole-tree lint
retains existing warnings outside the prototype.

Browser review used the Chrome extension only, never native screen control.
The Chinese/dark sample install, setup and app opening were exercised. Final
cold-load market/install checks passed. At 320 CSS pixels the final market had
305 CSS pixels of document scroll width and no horizontal overflow. The temporary
viewport override was reset. Final cold-load browser errors were empty.

The development entry cleans up its root/router on module disposal. An earlier
hot-update failure was corrected during development; source-edit sessions may
still require a fresh page for browser-plugin inspection. Cold-load evidence
does not claim an exhaustive hot-reload or assistive-technology certification.

## Remaining before P01/P06 acceptance

The owner still needs to review the complete visual flow (D5). Broader keyboard,
focus, enlarged-text and failure/recovery browser review remains. This prototype
renders only trusted local sample content directly; it does not load downloaded
React code into the host. The actual v2 sandbox, nonce/channel protocol, production
shell, typed backend API, persistent operations, identity/OU checks, resources,
task execution and runner are separate work packages and remain unimplemented.
