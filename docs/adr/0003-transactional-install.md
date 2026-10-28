# ADR 0003: Replace directories transactionally with sibling backups

- Status: accepted
- Date: 2026-10-28

## Context

Replacing application files in place can leave a half-updated application after
a crash. On Windows, running binaries cannot be renamed or deleted. Users need a
rollback path.

## Decision

Stage the complete new tree in a sibling directory. Journal the transaction in
state. Rename the old target to a sibling backup, rename the stage into place,
then record success. On failure or next start, restore the backup. Retain the
most recent backups.

For Windows, allow a detached helper copied to the state directory to wait for
the application PID and perform the same journaled apply.

Treat a directory install as a unit, not as a merge. Add explicit `preserve`
globs for data that must survive. Use a separate single-file strategy where
appropriate.

## Consequences

- Swaps are atomic on a filesystem and recoverable after crashes.
- The target must be a dedicated application-managed directory; user content is
  removed unless preserved.
- Backups consume disk equal to the previous installation; retention is bounded.
- A running Windows app must opt into `--defer`; direct replacement fails with a
  clear message.
- Command installers cannot be rolled back by the updater.
