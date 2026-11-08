# Data-loss regression suite

These tests are the specification for `engine`, `fsop`, `protect`, `inuse`
and `scan`. They were written before the engine (task W1-D). The engine
must make them pass **without editing them** (AGENTS.md rule 6).

The suite runs with every `make check`. To run it alone:

```sh
GOTOOLCHAIN=local go test ./test/regression/
```

Each test drives the real pipeline (`scan.Scan` -> `engine.Plan` ->
`engine.Apply` -> `fsop.Restore`) against a fake home built in `t.TempDir()`
with `env.New`. Fixtures are built in Go, so each test shows its own tree.

## Rule sets

- **Aggressive synthetic rules** (`aggressiveRules`): broad `rules.Rule`
  literals that aim straight at `~/Library/Caches/**`, `~/.cache/**`,
  `~/.claude/**`, `~/.claude/projects/*/memory`, Application Support, Group
  Containers, Containers, Mail, `~/.npm/_npx/**` and `~/.Trash/**`, as a
  buggy rule would. Protection must hold anyway.
- **Real embedded rules** (`embeddedRules`): `rules.Load(fs.Sub(ruledata.FS,
  "cleaners"))`, filtered to `action: quarantine` and paths starting with
  `~/`. A test must never touch system folders or other volumes, or run a
  real tool's cleanup command on the owner's Mac, even if the engine has a
  bug. Official commands are tested with a harmless `touch`.

## How the suite reads the contracts

The engine agent relies on these readings. Where a contract allowed more than
one reading, the choice is marked.

**Plan**

1. A blocked item (protected, whitelisted, in use) is **removed from
   `p.Items`** and appears once in `p.Skipped`. "Numbers the remaining items"
   in the engine contract implies this.
2. `Skip.Path` is the path of the item scan produced, not the protection
   entry. A `~/Library/Caches/**` rule produces `~/Library/Caches/JetBrains`,
   and that exact path is skipped as `protected`.
3. `Skip.Reason` is one of the `plan.Reason*` constants. `Detail` is not
   checked.
4. Risky items stay in `p.Items` with `Selected=false`. Safe and moderate
   items are selected.
5. Plan writes nothing: not the state folder, not the whitelist, nothing in
   the home. The home folder's own mtime must stay the same too.

**Apply**

6. Problems with single items go into the `Result`, and Apply returns a nil
   error: an item that changed or is gone is in `Result.Skipped` (`changed`,
   `gone`), and `Result.Failed` stays empty.
7. Protection, the whitelist and open files are checked again right before
   each move. A hit goes into `Result.Skipped` with the matching reason
   (`protected`, `whitelisted`, `in_use`). The tests cover a tampered plan
   file, a whitelist line added after planning, and a process that starts
   after planning.
8. A plan whose `Home` differs from `env.Home` returns an error **before
   anything is written**: no quarantine expiry, no run folder, no state
   folder.
9. An item outside the home (another folder, or an unclean path with `..`)
   is never moved. *Either* Apply returns an error for the whole plan, *or*
   the item appears in `Result.Failed` or `Result.Skipped`. Both are
   accepted.
10. `official_command` items are never run without `Options.RunCommands`
    (the M1 behaviour, still the default). A selected one appears in
    `Result.Suggested` and its path is untouched. With `RunCommands` (M2,
    `--run-commands`, `sapu packages`, `sapu optimize`) Apply runs them after
    the moves; `internal/engine/command_test.go` covers that with temp
    scripts, never a real tool.
11. With `Options.Permanent`, the run's quarantine folder is gone or empty
    afterwards, and `Result.Freed > 0`.
12. Identity covers the inode. A file swapped for a copy with the same
    bytes, mode and mtime counts as `changed`.

**fsop**

13. A moved item sits at `<StateDir>/quarantine/<run_id>/<item_id>-<base
    name>` (fsop invariant 1). The property test looks for it there and
    compares it with the original, including mode, mtime, xattrs and hard
    links.
14. `Restore` returns one `Restored` per moved item, with `To` equal to the
    original path and `Conflict=false` when nothing is in the way. After
    undo the home is identical, except the mtimes of the **folders the items
    were moved out of**. A rename always changes those, so they are not
    compared. The moved items' own mtimes are compared, and APFS keeps them
    across a rename.
15. `Expire` (and `Empty`) delete inside quarantine only. They never follow
    symlinks inside quarantined items, and they keep
    `runs/<run_id>/plan.json`, `runs/<run_id>/journal.jsonl` and
    `whitelist.txt`. `Expire` returns `freed > 0` when it empties a run.

**Symlinks and paths**

16. A rule path that runs through a symlinked folder
    (`~/Library/Caches/link/**`, where `link` points at `~/.claude` or outside
    home) must not move anything behind the link. Scan may skip such paths, or
    produce them and let protect or fsop refuse them. Only the outcome is
    checked.
17. **`t.TempDir()` is under `/var/folders/...`, and `/var` is a symlink to
    `/private/var`.** So `env.Home` is not a resolved path in tests:
    - `lsof` reports resolved paths (`/private/var/...`), so `inuse.Check`
      must resolve the path it is given (for example with
      `filepath.EvalSymlinks`) before comparing. Without that, the npx test
      fails.
    - A symlinked-parent check in fsop must compare against the resolved
      home, not demand that `EvalSymlinks(path) == path`. Otherwise every
      item in a test home would be refused.
18. Whether scan turns a symlink that is a direct child (for example
    `~/Library/Caches/docs-link`) into an item is up to scan. The tests pass
    either way.
19. Scan treats a folder that does not exist as "no match", not as an error.
    The aggressive rules target many folders that a given fake home lacks.

**Protection list (case 8)**

20. Every path in `rules/protect/*.yml` is tested twice: once as a folder
    with a file inside, once as a plain file (for entries such as
    `~/.claude.json`). Each run uses the aggressive rules plus a rule aimed at
    the entry's parent's children (`~/**` for top-level entries). A `*` in a
    segment is filled in (`~/Pictures/*.photoslibrary` ->
    `~/Pictures/Sapu Regression.photoslibrary`). Entries with `?`, `[`, `**`
    or a path outside home are logged and not tested.
21. The paths that were in `core.yml` at Wave 0 are listed in
    `fixedProtectedPaths`. Removing one fails the test.

## Cases from the independent review (`review_test.go`)

Each of these moved user data before it was fixed.

- **A symlinked protected folder** (`TestSymlinkedProtectedFolderIsProtectedWhereItLeads`).
  `~/.claude` is moved into `~/Library/Caches` and linked back, and a rule
  aims at the caches. The Claude data must be protected under its real name,
  not only under the link's name. The same holds for a whitelisted symlink.
- **A folder that changed inside after planning** (`TestFolderChangedInsideAfterPlanningIsSkipped`).
  A program writes deep inside a cache folder after the plan was made. A
  folder's own identity does not change, so apply must still skip it as
  `changed` and leave it as it is now, while the other items move.

## Not covered here (left open)

- Items holding read-only folders (for example a Go module cache, mode
  0555): `Empty` and `Expire` would need to restore write permission before
  deleting. Not data loss, but space would not be freed.
- Other volumes (`/Volumes/<name>/.sapu-quarantine`) need a real second
  volume (`vm` tag).
- The crash-after-each-journal-line test belongs to fsop's own suite (W1-A).
