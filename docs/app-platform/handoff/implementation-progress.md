# Application platform implementation progress

Reviewed: 2026-10-29, America/Los_Angeles.

During this pass, Portal main was `7ae587edd6199c5f207583c58083f41612bc4bac`
and Apps main was `54239874c3e8c7af7071cfc6d8de669449440925`. Portal PRs
352, 354 and 356 remained open drafts at their recorded heads. The verified
candidate and SDK WIP commits in the table are the starting points; this working
tree has not yet been committed or pushed.

| Task | Owner role | Candidate work | Local verification | Remaining gap |
| --- | --- | --- | --- | --- |
| H00 | repository owner; reviewers not yet named | Portal baseline `8a11f1d4`; Apps baseline `e7d12a60`; WIP `5374def0` / `f50eee22` | PR 352, 354 and 356 are still open drafts with the recorded heads; no production action | Name development, schema, visual and deployment approvers |
| H01 | integration reviewer required | SDK branch based on the verified candidate | Apps merges cleanly with current main; Portal merge preflight conflicts in migrations/storage, client, locales and Apps admin pages | Choose integration strategy without treating conflict sides as interchangeable |
| H02 | platform developer | Reviewed network transport, launch hint, shared hook, source pins and browser fixture path | Portal 1810 tests, Apps 279 tests, full Portal Go suite, focused race suite, builds, lint/typecheck and drift checks | Push and run the Linux native/browser workflow on one exact candidate SHA |
| H03-H18 | not yet started in this pass | None | None | Follow the handoff order and required approvals |

Production remains out of scope for this record. No real credential was imported,
no legacy application was migrated, no market release was published, and the
server beta channel was not pinned or changed.
