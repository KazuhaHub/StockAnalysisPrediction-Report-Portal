# Application platform implementation progress

Reviewed: 2026-10-29, America/Los_Angeles.

During this pass, Portal main was `7ae587edd6199c5f207583c58083f41612bc4bac`
and Apps main was `54239874c3e8c7af7071cfc6d8de669449440925`. Portal PRs
352, 354 and 356 remained open drafts at their recorded heads. The verified
candidate and SDK WIP commits in the table are the starting points. An integration
tree now also contains the H02 candidate and the current-main regression fixes.
The H02 network SDK and H03 runner-service candidates have now passed their
paired pushed CI; integration review remains separate.

| Task | Owner role | Candidate work | Local verification | Remaining gap |
| --- | --- | --- | --- | --- |
| H00 | repository owner; reviewers not yet named | Portal baseline `8a11f1d4`; Apps baseline `e7d12a60`; WIP `5374def0` / `f50eee22` | PR 352, 354 and 356 are still open drafts with the recorded heads; no production action | Name development, schema, visual and deployment approvers |
| H01 | integration reviewer required | Current main plus the SDK candidate; app-platform migrations moved to unused `0015` and `0016` after main claimed `0012`-`0014` | Portal 2218 web tests, full Go suite, build, vet, lint, Linux test-binary compilation and contract checks; Apps typecheck, 279 SDK tests, template/root/plugin/script suites and contract checks | Commit, push, and verify the exact integration SHA in full CI |
| H02 | platform developer | Reviewed network transport, launch hint, shared hook, source pins and browser fixture path | Portal `a57aaf83`: test `41004183082` and quota/browser `41004183946` passed; Apps `415c66d1`: check `41002023666` passed; shared contract drift checks passed | None for H02; integration remains H01 |
| H03 | platform developer | Explicit public-key bootstrap, private service config, long-running runner service, systemd restart bounds, Portal environment endpoint and static-app fail-open behavior | Portal source candidate `cf4084b3`: test `41010542804` and app-runner daemon `41010542824` passed; the daemon workflow started the real service on owned XFS/Docker, stopped it, restarted it and observed the controller profile again | Connect this service to the Portal browser managed-installation flow and record one complete UI installation; then integrate under H01 |
| H04-H18 | not yet started in this pass | None | None | Follow the handoff order and required approvals |

Production remains out of scope for this record. No real credential was imported,
no legacy application was migrated, no market release was published, and the
server beta channel was not pinned or changed.
