# How Sapu keeps your data safe

Sapu's one job is to free disk space without ever losing something you needed.
When it is unsure, it skips the item and tells you why. This page explains how
a cleanup works, the rules the code must keep, what is protected, and what Sapu
will never do. The code behind each rule is named in brackets, and each rule
has a test.

## How a cleanup works

```
scan ──► plan ──► checks ──► quarantine ──► journal ──► undo or expiry
```

1. **Plan.** `sapu clean` (or `purge`, or marking items in `analyze`) builds a
   plan: a list of items, each with its path, size, risk, a plain explanation,
   and a fingerprint of the file (device, inode, size and modification time).
   Building a plan writes nothing. `--plan FILE` saves it, and `sapu apply FILE`
   applies exactly that file later.
2. **Checks.** Every item is checked against the protection list, your
   whitelist, and the files that are open right now. Blocked items leave the
   plan and appear in a "skipped" list with the reason. Risky items stay in the
   plan but start unselected.
3. **Confirm.** You see the plan and confirm. Caches and other things that come
   back by themselves are grouped by risk. Everything else (apps, login items,
   files you picked, duplicates, Trash items, installers) is listed under "To
   move" or "Not selected", because "rebuilt" would be wrong for them. Nothing
   happens before this.
4. **Quarantine.** Sapu first takes a lock, so only one sapu works at a time.
   For each selected item it checks everything again, then
   moves the item into its quarantine folder with a single `rename()`. The item
   is never copied, so its contents, permissions, extended attributes and hard
   links stay exactly as they were.
5. **Journal.** Before each move, Sapu writes a line to the run's journal
   (`~/.sapu/runs/<run_id>/journal.jsonl`) and flushes it to disk. After the
   move it writes another. If the power fails or you press Ctrl-C, the journal
   shows exactly what moved, and `sapu undo` can put it back.
6. **Undo or expiry.** `sapu undo` restores a run. If you do nothing, the run
   expires after 7 days, or earlier if quarantine grows past 20 GB (oldest
   runs go first). Expiry runs only when you run Sapu yourself: at the start
   of `clean`, `purge` and `apply` (and the other commands that move
   things), and on `sapu quarantine expire`. The scheduled clean
   (`sapu schedule`) never expires or empties anything, so nothing is
   deleted for good while you are not there. `sapu quarantine empty` frees
   it at once.

Moving a file into quarantine does not free space, because the file is still on
the disk. The space comes back when the quarantine expires or is emptied. Sapu
says so after every run, with the date.

Quarantine lives in `~/.sapu/quarantine/<run_id>/`. For files on another volume
it is `<volume>/.sapu-quarantine/<uid>/<run_id>/`, because a `rename()` cannot
cross volumes. `SAPU_HOME` moves the state folder.

## The rules Sapu must keep

1. **Sapu deletes in one place only: inside its own quarantine folders.**
   Every other change is a move into quarantine. `--permanent` does not delete
   directly either: it quarantines first, then empties that one run's
   quarantine. [`internal/fsop`]
2. **Only `internal/fsop` moves or deletes files.** A Go test and the linter
   fail the build if any other package calls `os.Remove`, `os.RemoveAll`,
   `os.Rename`, `os.Truncate`, or runs `rm` or `mv`. [`internal/guard`]
3. **Apply touches only items in the plan,** and only if each item is still the
   same file that was planned. If its device, inode, size or modification time
   changed, it is skipped as "changed". A folder is skipped as "changed" too
   when anything inside it was modified after the plan was made, even if the
   folder itself looks the same. A folder Sapu cannot read all of (macOS may
   need Full Disk Access, see `sapu doctor`) is skipped as "unreadable": Sapu
   cannot tell whether something inside changed, so it does not move it. A file
   that vanished is skipped as "gone".
   [`internal/engine`, `internal/fsop`]
4. **Protection and the whitelist are checked twice:** when the plan is built,
   and again just before each move. Editing a plan file by hand to add a
   protected path does not work, and neither does changing an item's source
   to the one command that has an exception (see below). Protection follows symlinked folders: if a
   protected or whitelisted folder is a symlink, the place it leads to is
   protected too, so moving `~/.claude` into a cache folder and linking it back
   does not expose it. [`internal/engine`, `internal/protect`]
5. **Open files are skipped,** checked at planning and again at apply. A cache
   that a running program is using right now is left alone. Rules can also say
   "skip while this app is running" or "skip if modified in the last 3 days".
   The list of open files is refreshed during a long run, at least every 3
   seconds, so a program that starts using a cache halfway through is noticed.
   [`internal/inuse`, `internal/engine`]
6. **Planning never writes.** `--plan`, `explain`, `analyze` and `doctor` are
   read-only, including the state folder and the home folder's own timestamps.
   [`test/regression`]
7. **Undo never overwrites.** If something exists again at the original path,
   the restored item goes next to it as `<name> (sapu <run_id>)` and is
   reported as a conflict. The kernel itself refuses to replace a file, even
   one that appears after Sapu looked. [`internal/fsop`]
8. **The journal comes before the action,** so a crash always leaves a state
   that can be undone. A run without an "end" line is shown in `sapu history`
   as interrupted. [`internal/runlog`]
9. **Quarantine only accepts safe paths.** Sapu refuses paths that are not
   absolute and clean, that are outside your home folder or `/Volumes/<name>/`,
   or that equal, contain or sit inside Sapu's own state or quarantine
   folders. Emptying refuses any path outside a quarantine folder, and right
   before it deletes, it checks that the run's folder and the quarantine
   folders above it are real folders, not symlinks, owned by you, and in the
   place they should be. [`internal/fsop`]
10. **One sapu at a time.** Moving, undoing, emptying and expiring all take a
    lock on the state folder. A second sapu does not wait or interfere; it stops
    at once with "Another sapu is working on this Mac right now" and changes
    nothing. This keeps one sapu from emptying or undoing a run while another is
    still applying it. [`internal/fsop`]
11. **An item replaced while it moves is put back.** After each move Sapu checks
    that what it moved is what it planned. If something replaced the item in the
    moment between the check and the move, that thing goes straight back where
    it was, and the item is skipped as "changed". If its old place is taken
    again by then, it stays in quarantine and Sapu says so: `sapu undo <run>`
    puts it back next to what is there now. [`internal/fsop`]
12. **Emptying waits for unplugged drives.** If a run has items on an external
    drive that is not connected, `sapu quarantine empty` stops, names the
    drive, and deletes nothing, not even the items of that run on your Mac. The
    run stays undoable: connect the drive and try again. Automatic expiry skips
    such a run quietly. [`internal/fsop`]
13. **Tests never touch a real home folder.** Every test builds a fake home in a
    temporary folder. [`internal/guard`]
14. **An app's files go only with the app.** `sapu uninstall` ties every file
    the app left in your Library, and the permission reset of
    `--reset-permissions`, to the app itself (`requires` in the plan). Apply
    moves the app first; if the app is not moved in that run (it changed
    after the plan was made, it is open, it failed to move, you unselected
    it), none of them is moved or run, and each is skipped as "requires".
    A plan whose `requires` names no item to move of that plan is refused.
    If you remove `requires` from a plan by hand, the plan is applied as you
    wrote it. [`internal/engine`, `internal/cli`]
15. **Sapu's own files never land in yours.** A plan file (`--plan FILE`),
    settings, the whitelist, snapshots, rule updates, the agent's plist,
    `agent.log` and each run's `plan.json` and journal are written or
    appended to only when the file is a regular file with one name, owned by
    you. A symlink, a hard link to another file (say `plan.json` linked to
    your thesis), a folder or a device is refused, and nothing in it is cut
    or added: Sapu opens the file without following a symlink and without
    emptying it, checks what it opened, and only then writes. A journal is
    checked before Sapu even repairs a torn last line. Each file also has to
    stay inside the folder it belongs to (the state folder, the LaunchAgents
    folder, the folder you named for a plan): every folder below it is
    opened one at a time, relative to the one above, and a symlink is
    refused, so a symlinked folder inside `~/.sapu` that leads elsewhere
    cannot redirect the write. Because Sapu works on the open folder and not
    on its name, swapping a folder for a symlink while Sapu is writing does
    not redirect it either. That folder itself may be a symlink, so `~/.sapu`
    or `~/.config` kept in a dotfiles folder works. A second name that
    another program gives the file after Sapu checked it only ever shows
    Sapu's own content: it cannot make Sapu write into a file that already
    held your data. [`internal/safefile`, `internal/runlog`, `internal/agent`]

## What is protected, and why

The list is `rules/protect/core.yml`. A path is blocked when it is on the list,
inside a listed folder, or contains a listed folder. The last case matters:
cleaning `~/Library/Application Support` would take every app's data with it.
There is no switch to turn protection off, and no cleanup rule can override it.

Several entries exist because other cleaners really lost people's data. Each of
these is a test in `test/regression/` that must pass before any change merges.

| Case | What went wrong elsewhere | What Sapu does |
|---|---|---|
| V2EX 1163304 | JetBrains DataGrip lost its data sources and query consoles | `JetBrains` settings and caches are protected |
| V2EX 1163304 | Hugging Face models in `~/.cache` were wiped; they are many GB to download again | `~/.cache/huggingface` is protected |
| V2EX 1163304 | GitHub Desktop lost its accounts and repository list | `GitHub Desktop` data is protected |
| Mole #823 | Claude Code project memory was removed as if it were cache | `~/.claude` and the Claude app data are protected |
| Mole #856 | The Apple Notes database was cleaned | Notes containers are protected |
| Mole #32 | Apple Mail messages and the Mail container were removed | `~/Library/Mail` and the Mail container are protected |
| Mole #1941 | Browsers installed for automation (Camoufox, Playwright) were deleted and tools broke | Both browser folders are protected |
| Mole #1870 | `~/.npm/_npx/<hash>` was deleted while `npx` was running from it | Open-file check skips anything a process is using |
| Mole #163 | Emptying the Trash ignored the user's whitelist | The whitelist is checked twice and covers every command |
| Mole #1865 | A "dry run" deleted files | Planning is read-only; a test checks that nothing, not even a timestamp, changes |

The rest of the list follows one idea: things that look like a cache but hold
the only copy of something.

- **Virtual machines and containers** (Docker, OrbStack, Colima, UTM,
  Parallels, VMware, VirtualBox, Tart). Their disk images hold every image,
  container and volume, including databases.
- **Cloud drive folders** (Dropbox, Google Drive, OneDrive) and the Google
  Drive cache, which keeps files not yet uploaded. Deleting there deletes them
  online, or loses the unsynced copy.
- **Your personal Apple data:** Messages, Photos, iCloud Drive, Safari,
  Calendars, Contacts, Voice Memos, Reminders, Music, and iPhone backups, which
  are often the only copy.
- **Secrets:** keychains, SSH and GPG keys, password manager vaults, crypto
  wallets (a lost wallet file can lose the money), cloud and developer logins.
- **Editors, IDEs and AI tools:** settings, workspaces, local history, chat
  history and models. Xcode archives are kept because their debug symbols are
  needed to read crash reports.

Cache folders of apps that have a cleanup rule (browsers, Slack, Discord,
Spotify and so on) are not protected as a whole, so those rules still work. The
data folders next to them are.

You can add your own paths with `sapu whitelist add`. A whitelist entry blocks
the same way: the path, what is inside it, and every folder that contains it.

**One exception, and it is exact.** `sapu backups` lets you pick one iPhone or
iPad backup to move. The protection list allows that and nothing else: an item
from `sapu backups` whose path is exactly one folder inside
`~/Library/Application Support/MobileSync/Backup`. Not MobileSync, not
`Backup` itself, not a file inside a backup, and not a backup on your
whitelist, which always wins. The exception is checked against the plan
item's path segment by segment, so a plan edited to say "backups" on a wider
path is still blocked. Messages has no exception: `sapu messages` only lists
attachments. A signed rule update cannot add an exception to the built-in
list: an update's entries only add protection of their own.

The source names of the dedicated commands (`backups`, `messages`,
`uninstall`, `reset`, `large`, `trash`, `installer`, `dupes`, `similar`,
`tidy`, `packages`, `optimize`, `login-items`, `analyze`, and `purge` with
anything after it) are reserved. A cleanup rule may not take one as its id,
so a rule, even one in a signed rule update, can never pass its items off as
`sapu backups` items and get that exception: such an update is refused at
install, and one already stored is not used (Sapu falls back to the built-in
rules and says why). An exception may name only one of these commands, never
a rule. [`internal/rules`, `internal/scan`, `internal/protect`,
`rules/protect/core.yml`]

## Other programs' cleanup commands

Some tools clean their own caches better than Sapu can: `brew cleanup`,
`npm cache clean`, `docker system prune`. Sapu never deletes those tools' files
itself. It can run the tool's own command, and only under these rules:

- **Only Sapu's own commands.** Apply runs a command only when Sapu itself
  makes exactly that command for the item's source: the command of a cleanup
  rule in the rules in use, one of the fixed commands of `sapu packages` or
  `sapu optimize`, `git -C <repository> worktree remove <worktree>` from
  `sapu purge` (only in a real repository folder with a `.git` folder), or
  `tccutil reset All <bundle id>` from `sapu uninstall`. The program must be
  the one PATH leads to now, by the command's own program name. A plan file
  edited to run another program, a wrapper such as `perl -e` or
  `find -exec`, other arguments, another copy of the program, or another
  source's command is refused as "not a command Sapu knows" and never
  started. [`internal/engine`]
- **Only when you ask.** `clean`, `purge` and `apply` run commands only with
  `--run-commands`; without it they only show them. `sapu packages` and
  `sapu optimize` exist to run such commands, so they run theirs without the
  flag. No other command has the flag or runs commands.
- **After a confirmation that says they are not undoable.** Sapu lists every
  command exactly as it will run and says "These commands are not undoable."
  before asking. Quarantine cannot hold what a tool deletes itself, and
  `sapu undo` restores only what Sapu moved. `--yes` answers the question for
  you; it never makes commands run that you did not ask for.
- **A deny list, as a second layer.** A program that gains admin rights
  (`sudo`, `su`, `doas`), deletes or moves files (`rm`, `rmdir`, `mv`, ...),
  runs scripts or runs other programs (`sh`, `bash`, `zsh`, `osascript`,
  `env`, `xargs`, ...) is never started, even if a plan file asks for it and
  even behind a symlink with another name, such as a `brew` on PATH that
  leads to a shell.
- **No shell.** The program starts directly, with the plan's arguments as they
  are, so nothing in them is expanded or interpreted. It runs in your home
  folder with no input, so a question it asks gets no answer, and it is stopped
  after 10 minutes.
- **No `sudo`.** Actions that need admin rights are shown with the exact
  command for you to type in Terminal, and never run.
- **The usual checks first.** Protection, the whitelist and open files are
  checked again on the item's path right before its command starts, and the
  journal records the start and the exit code of every command.

Commands run after the moves, in plan order. A command that fails is reported
with the end of its output.

## Replacing duplicates with clones

`sapu dupes --replace-with-clone` keeps every file where it is. Each extra copy
is replaced by an APFS clone of the kept file: it opens the same, but the two
share their space on the disk, and changing one later does not change the
other. Sapu does this without deleting anything outside its quarantine:

- **The extra copy goes to quarantine first,** with the same checks as any
  move, including the one that the kept copy is still exactly the file that
  was planned. If the kept copy changed, the extra stays where it is. A plan
  edited by hand cannot pick another file to clone: the clone's source must be
  the plan's kept copy, or the item is refused. [`internal/engine`]
- **The clone is made inside the quarantine folder,** checked (a regular file
  as long as the copy it replaces, on the same volume), given the mode and
  modification time of the copy it replaces, and written to the journal with
  its fingerprint. Only then is it renamed into place, and the kernel refuses
  to replace anything that appeared there in the meantime. [`internal/fsop`]
- **If the clone cannot be placed** (the volume is not APFS, the kept copy
  changed at the last moment, a file appeared at the path), the extra copy
  simply stays in quarantine, and Sapu lists it under "Clones not placed".
  `sapu undo` puts it back. `--permanent` does not empty such a run.
- **Undo deletes a clone only if nobody touched it.** If the file at the path
  still has exactly the clone's fingerprint (device, inode, mode, size and
  modification time), Sapu moves it into the run's quarantine folder, checks
  the fingerprint again there, deletes it, and puts the original back in its
  place. A clone that was edited, renamed over, or replaced stays, and the
  original comes back next to it as `<name> (sapu <run_id>)`. If something
  takes the clone's place in the moment Sapu moves it, that file goes straight
  back. After a crash, Sapu deletes only a file whose fingerprint the journal
  recorded before it was placed; anything else stays.
- **Emptying the quarantine** deletes the original copies only. The clones
  are ordinary files and stay.

The space comes back when the quarantine is emptied or expires, like after any
move. A clone gets the extended attributes (Finder tags, "where from") of the
kept file, not of the copy it replaces; the replaced copy keeps its own in
quarantine until then.

## Risk labels

Every cleanup rule has a label, and a CI check rejects a rule without one.

- **safe:** rebuilt automatically, for example a download cache.
- **moderate:** rebuilt, but at a cost, for example slower first launch or a
  re-download.
- **risky:** may lose something you care about. Never selected by default.

Every rule also says whether the item regenerates, and has a plain-English
explanation shown next to it. The risk headings in a plan are only for items
that regenerate. Apps, login items, picked files and backups, duplicates, Trash
items and installers do not, so they appear under "To move" (selected) and "Not
selected", each with its explanation.

## What Sapu will never do

- Delete anything outside its own quarantine folders.
- Clean a protected path, with or without `--yes`, with any rule.
- Move a file or folder that changed after it was planned, or that is open
  right now.
- Run two cleanups at the same time.
- Delete anything for good from a scheduled run: the scheduled clean only
  moves safe caches into quarantine and never expires old runs.
- Move an app's files, or reset its permissions, when the app itself was not
  moved in the same run.
- Empty a run while a drive that holds some of its items is unplugged.
- Overwrite your files when undoing.
- Run another program's cleanup command unless you ask with `--run-commands`
  or use `packages` or `optimize`, and then only after a confirmation that says
  the commands are not undoable (see above). Never a command Sapu did not make
  itself, whatever a plan file says.
- Use `sudo`, or modify anything outside your home folder and mounted volumes.
- Write anything while only planning, explaining or analyzing.
- Write or append its own files (plans, settings, journals, logs) through a
  symlink or a hard link, into a file of another user, or outside the folder
  they belong to.

## What Sapu can't promise

Sapu moves items you confirmed. If you confirm an item you shouldn't, undo is
there for the quarantine window, but after it expires or you empty it, the data
is gone. Use `--plan` first, read the reasons, and whitelist anything you want
kept. If you find a path that should be protected, please report it.
