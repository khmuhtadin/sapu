# Reporting: snapshots, diff, reconciliation, before/after, status

Sapu's job is to show where disk space went. These features answer "what
changed?" and "why does this not match System Settings?". None of them
deletes or moves anything.

## Size snapshots (`internal/trends`)

A snapshot is `~/.sapu/trends/<YYYY-MM-DD>.json`: the size of the home folder
and of every folder up to three levels below it that uses at least 100 MB
(for example `Library/Caches/CapCut`). Sizes are allocated bytes from one
`walk.Tree` of the home folder, about 50 seconds on a large home. Sapu's own
state folder is left out of the folder list; it still counts in the home
total.

- One file per day. A second snapshot on the same day replaces the first,
  with `os.WriteFile`.
- Two ways to take one:
  - `sapu diff --record`: a read-only walk that writes only the snapshot.
  - The background agent, once a day, when scheduled cleanup or alerts are on
    (`sapu alerts on`). It runs after the alerts and the scheduled clean, and
    holds the Sapu lock like they do.
- The walk stops after 5 minutes (`trends.Timeout`) and saves nothing, with
  the error "a folder did not respond (iCloud Drive?)". `walk` cannot be
  cancelled while a system call is blocked, so without this limit a folder
  that never answers would hang `sapu diff --record` and the agent, which holds
  the Sapu lock while it walks.
- Each file is a few KB (100 folders at most about 10 KB), so a year is a few
  MB.

### Old snapshots are not pruned (open item)

The plan was to keep 90 days and delete older files. Only `internal/fsop` may
delete files (`internal/guard` fails the build otherwise), so `trends` has no
`Prune`. Snapshots are kept. If pruning is wanted, add an fsop function that
removes `*.json` older than 90 days from `~/.sapu/trends`, and call it from
`agent.Run`.

## `sapu diff [--since 7d]`

Compares the newest snapshot with the newest one that is at least `--since`
older (`7d`, `30d`, `2w`; default `7d`). It lists the 10 folders that grew most
and the 10 that shrank most, with the change and the size now:

    Biggest growth
      ~/Library/Caches/CapCut                          +38.0 GB  now 43.0 GB

Rules:

- A folder is listed at the deepest level that explains the change: when one
  sub-folder carries at least 75% of its parent's change, only the sub-folder
  is listed, not `~/Library` and `~/Library/Caches` as well. When the change is
  spread over several folders, the parent is listed too.
- Changes under 10 MB are not reported.
- A folder that is missing from one snapshot used less than 100 MB then. Its
  change is marked "about" (`approx` in JSON) because the real change is a
  little smaller.
- With fewer than two snapshots far enough apart, diff says so and explains how
  to get them (`sapu diff --record` now and again later, or `sapu alerts on`).
  That is not an error: the exit code is 0.
- `--record` first saves today's snapshot, then shows the diff.

## Reconciliation in `sapu explain` ("How this adds up")

macOS shows one number for System Data and Sapu measures several. The block
at the end of `sapu explain` (and `reconcile` in `--json`) shows how they fit:

| Line | Where it comes from | Precision |
|---|---|---|
| Used on disk | `statfs` of the home volume: size minus free | Counts purgeable space as used, so it can be higher than System Settings. Estimate |
| Your home folder | The newest snapshot, if under 24 hours old; otherwise "not measured" | Counts only what Sapu may read (no Full Disk Access, less) |
| System Data parts Sapu measured | The `explain` rows outside home: Spotlight, system assets, swap and sleep image | A lower bound; rows that could not be measured are counted separately |
| The rest | Used - home - system parts, never below 0 | Estimate. Apps in /Applications, Homebrew and other tools, macOS itself and its update snapshots, other users, purgeable space, and folders Sapu could not read or measure |

Home and the system parts do not overlap, so home + system + rest equals used.
Without a fresh home snapshot, the rest is "not known" (`-1` in JSON). Sapu
never prints a split it cannot back up.

## Before and after a run

Moving items into quarantine is a `rename()` on the same volume, so free space
does not change until the quarantine is emptied. The result of `clean`,
`purge`, `apply` and the other commands that move items ends with:

    Free space now 12.4 GB; after `sapu quarantine empty <id>`: about 16.1 GB.

"Now" is read from the disk after the run. For a quarantine run it is also the
free space before the run, because nothing was freed. "After" is now plus the
bytes moved. It says "about" because APFS clones and hard links can make the
real change smaller. With `--permanent` the quarantine is emptied right away,
so the line shows the real free space and what the run freed:

    Free space now 16.1 GB; this run freed about 3.7 GB.

## `sapu status`

One read-only screen, with no walks, so it returns at once:

- disk used, total and free;
- quarantine: runs, total size, earliest expiry;
- the last 3 runs;
- scheduled cleanup and alerts (from `internal/agent`), and agent warnings;
- Full Disk Access;
- swap in use (`vm.swapusage`);
- the biggest growth in the last 7 days, from the snapshots, when there are two
  snapshots 7 days apart.

`--json` returns the same data (`schema/v1/status.json`).
