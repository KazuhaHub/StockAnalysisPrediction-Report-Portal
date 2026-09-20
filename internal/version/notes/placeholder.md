This file exists so `//go:embed notes/*.md` always matches something.

A release build writes the tagged commit's own note to `notes/release.md` next to this file, from
`docs/releases/<YYYY>/<tag>.md`, and that is what the binary serves. Nothing writes here: a build
without that step carries no release note, and says so, rather than presenting this file as one.

The injected file is gitignored for the same reason `internal/web/dist` is — `go build` stamps a
binary built from a dirty worktree with `vcs.modified=true`, and `scripts/check-release-build.sh`
refuses a release whose provenance it cannot vouch for. An injected build input therefore has to live
somewhere git does not track.
