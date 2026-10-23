# ADR 0042 — Separate band and popup dismissal, and a step that renames a baseline column

**Status: Accepted.** 2026-10-23. Amends [ADR 0025](0025-site-announcements.md) (dismissal) and
[ADR 0035](0035-migrations-after-a-frozen-baseline.md) (what a migration step may declare). Follows
the retirement rule of [ADR 0034](0034-calver-baseline-and-database-compatibility-reset.md) §5.

## Context

An announcement had one switch, `dismissible`, labelled "Readers can close it". It only ever governed
the band's close button; the popup's "Got it" and "Don't show again" ignored it. Production showed the
cost of that ambiguity: an operator switched it off, readers could still make the announcement go
away, and it stayed gone after a reload. The band half was fixed in place (PR #156). Gating the popup
on the same switch was not an option: every row defaults to off, so every existing popup would have
started re-firing on every load for every reader.

The owner's decision: two switches, one per surface, and the column renamed so its name says which
surface it governs.

## Decision

### 1. Two columns

- `announcements.strip_dismissible` — the old `dismissible`, renamed. Meaning unchanged: the band
  has a close button, and a stored band dismissal counts.
- `announcements.popup_dismissible` — new, nullable, no default, no back-fill. NULL reads as
  *dismissible*, which is what every popup was before this step, so upgrading changes no reader's
  experience. New announcements are created with it on.

When `popup_dismissible` is off: no "Don't show again"; a stored "don't show again" and a session
"got it" are ignored (not erased); "Got it" still closes the dialog, because a dialog with no exit
locks the reader out of the portal, and is remembered in memory for the page load only. A dialog
offers "Don't show again" only when every row in it may be dismissed.

The API carries `stripDismissible` and `popupDismissible`; the field `dismissible` is gone from every
payload. A reader page cached from the previous release reads the missing field as "not
dismissible", which fails towards showing more, not less.
On input, `dismissible` is still accepted for one release line as the band switch (as `popup` was
for `display`), so a console tab running the previous page does not silently lock a band on save;
`stripDismissible` wins when both are sent. Delete the alias with the next line.

A restore refuses a dump whose header predates the rename but whose content already carries the new
name, alone or beside the old one: the header and content disagree, and a duplicated column in the
insert would be silently resolved by SQLite.

### 2. The rename is migration step 0010, not an edit of the baseline

Editing `baseSchemaStmts()` to say `strip_dismissible` would move the acceptance contract: every
existing database — production included — would be refused at startup for lacking a column it has
under its old name. ADR 0034 §5 permits that only after proving no source inside the compatibility
window loses support, with a tested bridge. So the move is split the way §5 orders it:

1. **Now:** step 0010 renames the column and adds the new one, in one transaction (RENAME COLUMN is
   transactional on both drivers). Every source in the window is accepted as before and carried
   forward by the step, so none loses support, and this release is its own bridge. The baseline
   declaration keeps `dismissible`, with a comment pointing here.
2. **Later:** once this release has been the oldest supported source for a full window, the step
   can be folded into the baseline (declare `strip_dismissible` and `popup_dismissible`, delete
   0010 and the rename plumbing) by the §5 process, verified source → target on both drivers.

The ids 0007–0009 were already taken by work in flight on other branches. Ids are labels; the list
order is the sequence, so whichever lands next appends after 0010.

### 3. A step may rename a baseline column

`migration.renames` declares `{table, from, to}`, and the three places the old name is load-bearing
honour it:

- **The startup shape check** (`checkRenamedBaseColumn`). Step recorded: the new name must exist and
  the old one must be gone. Step not recorded: exactly one of the two — the old name is what every
  earlier release wrote, and the new one alone is a column the step will find and record, the same
  rule as an added column that already exists. Both, or neither, is refused.
- **The step's verification** (`verifyMigrationProducts`): the new name exists and the old one does
  not. It runs on every start, like every other product check.
- **Restore** (`renamesNotCarried`). A dump that predates the step carries the old name; it is loaded
  under the new one. The rename moves no data, so the step declares `olderDumpOK`. A dump that claims
  the step gets no translation, and an old name in it is refused as unknown — otherwise a damaged
  backup could be loaded as an old one.

## Consequences

- **Rollback.** As for every step: v2026.43 and earlier refuse to start on a database that records
  0010, and a backup written after it cannot be restored by them. The way back is the backup taken
  before upgrading. A backup taken before upgrading restores into this release.
- Tests that wind a database back to an earlier release now have to undo 0010 as well, because a
  dump's migration list must be a contiguous prefix.
- The two guards for the rename back each other up, so each has its own test: an end-to-end test
  cannot tell when one of them is broken.
