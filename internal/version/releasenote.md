<!--rp-release-note-placeholder-->

This file is a placeholder for development and pre-release builds, where it means "this build
carries no release note". The release pipeline overwrites it with the tagged commit's own note,
`docs/releases/<YYYY>/<tag>.md`, before compiling the binary, and refuses to build a release whose
note is missing. See .github/workflows/release.yml.
