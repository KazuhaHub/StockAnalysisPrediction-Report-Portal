# Application platform implementation progress

Reviewed: 2026-10-30, America/Los_Angeles.

During this pass, Portal main was `d0158f19`
and Apps main was `54239874c3e8c7af7071cfc6d8de669449440925`. Portal PRs
352, 354 and 356 remained open drafts at their recorded heads. The verified
candidate and SDK WIP commits in the table are the starting points. An integration
tree now also contains H01-H03, the current-main regression fixes and the
conditional CodeQL corrections.

| Task | Owner role | Candidate work | Local verification | Remaining gap |
| --- | --- | --- | --- | --- |
| H00 | repository owner; reviewers not yet named | Portal baseline `8a11f1d4`; Apps baseline `e7d12a60`; WIP `5374def0` / `f50eee22` | PR 352, 354 and 356 are still open drafts with the recorded heads; no production action | Name development, schema, visual and deployment approvers |
| H01 | integration reviewer required | Portal `e622c7f3` combines current main, the verified network SDK and H03; unpublished app-platform migrations remain `0015` and `0016` after main claimed `0012`-`0014` | Portal test `41049324619`, quota/browser `41049324669`, runner daemon `41049324581`, full CodeQL and web suites passed; Apps `ba6c6b78` is the paired integration commit | Authorized review, draft release and integration decision; no technical CI gap |
| H02 | platform developer | Reviewed network transport, launch hint, shared hook, source pins and browser fixture path | Portal `a57aaf83`: test `41004183082` and quota/browser `41004183946` passed; Apps `415c66d1`: check `41002023666` passed; shared contract drift checks passed | None for H02; integration remains H01 |
| H03 | platform developer | Explicit public-key bootstrap, private service config, long-running runner service, systemd restart bounds, Portal environment endpoint and static-app fail-open behavior | Integrated candidate `e622c7f3`: test `41049324619`, quota/browser `41049324669` and runner daemon `41049324581` passed. The quota/browser workflow started `service.Serve`, completed the Portal managed installation and SDK browser path, verified registration/RPC/receipts, then stopped the service and performed owned IPC/container cleanup | None for H03; production remains separately gated |
| H04-H18 | not yet started in this pass | None | None | Follow the handoff order and required approvals |

Production remains out of scope for this record. No real credential was imported,
no legacy application was migrated, no market release was published, and the
server beta channel was not pinned or changed.
