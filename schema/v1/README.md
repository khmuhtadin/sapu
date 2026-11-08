# JSON output contract, version 1

Every `--json` output is one document matching `envelope.json`. The `data`
of each command matches `<command>.json` (for subcommands,
`<command>-<subcommand>.json`, e.g. `quarantine-list.json`). Minor
releases only add optional fields; anything else bumps `schema_version`.
Contract tests in `internal/cli` validate real command output against these
files.

## Names

The `command` field of the envelope is the name of the schema, so
subcommands have a hyphen, not a space. Every command that takes `--json`
is in this table, once:

| Command line | `command` | Schema |
|---|---|---|
| `sapu` | `summary` | `summary.json` |
| `sapu status` | `status` | `status.json` |
| `sapu clean` | `clean` | `clean.json` |
| `sapu purge` | `purge` | `purge.json` |
| `sapu apply` | `apply` | `apply.json` |
| `sapu undo` | `undo` | `undo.json` |
| `sapu history` | `history` | `history.json` |
| `sapu diff` | `diff` | `diff.json` |
| `sapu analyze` | `analyze` | `analyze.json` |
| `sapu explain` | `explain` | `explain.json` |
| `sapu doctor` | `doctor` | `doctor.json` |
| `sapu version` | `version` | `version.json` |
| `sapu large`, `backups`, `messages`, `trash`, `installer`, `dupes`, `similar`, `tidy`, `slim`, `health`, `updates`, `optimize`, `packages`, `uninstall`, `reset` | the command name | `<command>.json` |
| `sapu login-items` | `login-items` | `login-items.json` |
| `sapu login-items disable` | `login-items-disable` | `login-items.json` (the run shape) |
| `sapu quarantine list` | `quarantine-list` | `quarantine-list.json` |
| `sapu quarantine empty` | `quarantine-empty` | `quarantine-empty.json` |
| `sapu quarantine expire` | `quarantine-expire` | `quarantine-expire.json` |
| `sapu whitelist list`, `add`, `remove` | `whitelist-list`, `whitelist-add`, `whitelist-remove` | `whitelist-list.json` |
| `sapu schedule on`, `off`, `status` | `schedule-on`, `schedule-off`, `schedule-status` | `schedule.json` |
| `sapu alerts on`, `off`, `status` | `alerts-on`, `alerts-off`, `alerts-status` | `alerts.json` |
| `sapu config show` | `config-show` | `config-show.json` |
| `sapu config path`, `init` | `config-path`, `config-init` | `config-path.json` |
| `sapu rules status`, `install`, `reset` | `rules-status`, `rules-install`, `rules-reset` | `rules-status.json` (the rules in use after the command) |

`sapu mcp` serves MCP over stdio and has no JSON output of its own.
Other files here are not command outputs: `envelope.json` is the wrapper of
all of them, and `plan.json` is the plan that `--plan FILE` saves and
`sapu apply` reads (see `--plan -` below).

A few commands share a schema because their data has the same shape:
`whitelist add`, `remove` and `list` return the whitelist as it is after the
command (only `list` also has `config_entries`, the read-only paths of
`sapu.toml`); `config init` returns what `config path` returns; `rules
install` and `reset` return what `rules status` returns; `login-items
disable` is a run like `clean`. Every other command uses its own name.

## Errors

On failure `ok` is `false`, `data` is absent, and `error.code` is one of:

| Code | Meaning | Exit code |
|---|---|---|
| `error` | Anything not listed below | 1 |
| `usage` | Bad flag or argument, or the question was not confirmed | 2 |
| `partial` | Some items were skipped or failed (with `clean`, `purge` and `apply` the result is in `data` and `ok` stays `true`) | 3 |
| `cancelled` | Stopped with Ctrl-C | 130 |
| `locked` | Another sapu is working on the state folder | 1 |
| `unavailable` | A drive that holds quarantined items is not connected | 1 |

`error.message` is the sentence a person would read.

## `--plan -`

`sapu clean --plan -` (and `purge --plan -`) writes the plan itself to stdout:
the raw document that matches `plan.json`, with no envelope. The same plan is
what `--plan FILE` saves and `sapu apply` reads. Do not combine `--plan -` with
`--json`; that is a usage error. With `--plan FILE --json` you get the usual
envelope, with `data.plan_file` and `data.summary`.

## Progress events

`--progress ndjson` writes one JSON object per line to stderr while a command
works. Stdout still holds only the final document. Every line has
`"type": "progress"` and a `phase`; the other fields are left out when they do
not apply:

| `phase` | Sent by | Fields |
|---|---|---|
| `scan` | `clean`, `purge`, `analyze`, `dupes`, `packages` | `files`, `bytes`, `path` while scanning; or `text` for a step with no counters ("Measuring ~…") |
| `apply` | every command that moves items (`clean`, `purge`, `apply`, `uninstall`, `dupes`, ...) | `done`, `total`, `path` (the item being moved) |
| `explain` | `explain` | `text` ("Measuring System Data…") |
| `command` | `clean`, `purge`, `apply`, `packages`, `optimize` | `text` ("Running npm cache clean --force…"), sent right before an official cleanup command starts |

`text` is the sentence a terminal would show on its progress line. It is
worded for people, so read the counters, not the text, in scripts. A new phase
or field may be added in a minor release; ignore what you don't know.

## Sizes and dates

- `freed` fields are the recorded sizes of the items deleted for good. The
  space macOS gets back can be smaller, for example when a file also has a
  hard link outside the quarantine.
- `expires_after` is the earliest time a later sapu run (`clean`, `purge`,
  `apply`, `quarantine expire`) may delete a run's quarantine. Nothing is
  scheduled; the size cap can expire a run sooner.

## Plan items and skips

- `clone_from` (optional) is on an item of `sapu dupes --replace-with-clone`:
  the kept copy whose APFS clone takes the item's place. It must equal
  `keep_copy.path`; apply refuses the item otherwise. A run result lists the
  clones placed in `cloned`; a clone that could not be placed is in both
  `moved` and `failed`.
- `keep_copy` (optional) is on an item of `sapu dupes` or `sapu similar`: the copy that stays,
  `{ "path", "identity" }`. Apply moves the item only while that copy still
  exists with exactly that identity. It appears wherever an item does: in
  plans, and in the `moved`, `failed` and `suggested` lists of a result.
- A skip's `reason` is one of `protected`, `whitelisted`, `in_use`, `changed`,
  `gone`, `duplicate` or `unreadable`. `unreadable` means Sapu could not read
  the contents of a folder to check whether it changed since the plan (macOS
  may need Full Disk Access; see `sapu doctor`). The item stays where it is.
  Such a folder used to be reported as `changed`; read both.
