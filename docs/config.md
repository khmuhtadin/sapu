# Settings: sapu.toml

Sapu works without a settings file. When you want different defaults, put
them in `sapu.toml`. Sapu only reads this file; the one command that writes
it is `sapu config init`, and only when there is no file yet.

## Where it lives

1. `$SAPU_CONFIG`, when it is set. It must be an absolute path or start
   with `~/`.
2. Otherwise `~/.config/sapu/sapu.toml`.

A missing file means every setting has its default. The file may be a
symlink into a dotfiles repository; Sapu reads it where it points.

The background agent (`sapu schedule`, `sapu alerts`) runs from launchd,
which does not see the variables of your shell. When `SAPU_CONFIG` is set
as you run `sapu schedule on` or `sapu alerts on`, the agent's plist passes
it on (next to `SAPU_HOME`), so the agent reads the same file. If you
change `SAPU_CONFIG` later, run the command again. Only Sapu's `internal/env`
package reads the variable.

## Commands

| Command | What it does |
|---|---|
| `sapu config show` | The settings in effect and where each one comes from (`sapu.toml` or `default`), plus every whitelist entry and its file |
| `sapu config path` | Where Sapu reads `sapu.toml` from. It works even when the file has a mistake, so you can find and fix it |
| `sapu config init` | Writes a commented example with every setting at its default. It refuses when anything is already at that path, a symlink included (even one that points nowhere) |

All three take `--json` (schemas `config-show.json` and `config-path.json`).

## Every setting

```toml
profile = "standard"

[quarantine]
max_age = "7d"
max_size = "20GB"

[clean]
exclude = ["xcode.derived-data", "browsers"]

[purge]
roots = ["~/Projects"]
older_than = "30d"

[whitelist]
paths = ["~/Projects/keep-this"]
```

| Key | Default | Allowed | Used by |
|---|---|---|---|
| `profile` | `"standard"` | `"conservative"`, `"standard"`, `"aggressive"` | `clean` (text flow and review screen), `purge` |
| `quarantine.max_age` | `"7d"` | an age, at least `1d` | every apply (see below) |
| `quarantine.max_size` | `"20GB"` | a size, at least `1GB` | every apply (see below) |
| `clean.exclude` | none | rule ids and categories | `clean`, the scheduled clean |
| `purge.roots` | the current folder | paths starting with `~/` or `/` | `purge` without folder arguments |
| `purge.older_than` | none (every project) | an age | `purge` without `--older-than` |
| `whitelist.paths` | none | paths starting with `~/` or `/`, `*` globs allowed, no `**` | everything Sapu plans or moves |

Ages are a whole number and a unit: `m`, `h`, `d` or `w` (`90m`, `36h`,
`7d`, `2w`). Sizes are a whole number and `MB`, `GB` or `TB`, in thousands
as Sapu prints sizes (1 GB is 1,000,000,000 bytes); the unit may be lower
case and may follow a space.

### profile

Which items are selected when a plan starts. You can still change the
selection on the review screen or with `--only` and `--exclude`.

| Profile | Selects |
|---|---|
| `conservative` | safe items only |
| `standard` | safe and moderate items |
| `aggressive` | safe, moderate and risky items |

`aggressive` only pre-selects. The confirmation still lists every item,
risky ones included, and nothing moves without your yes (or `--yes`).

On the command line, `--risk` wins over `--profile`, and `--profile` wins
over the file:

```
sapu clean --profile conservative        # safe only, whatever sapu.toml says
sapu clean --profile aggressive --risk moderate   # --risk wins: safe and moderate
```

### [quarantine]

How long cleaned items stay in quarantine, where `sapu undo` can bring them
back, and how much the quarantine may hold before its oldest runs are
deleted. Expiry runs at the start of every apply you start yourself; the
scheduled clean of the background agent never expires anything, so nothing
is deleted for good without you.

These settings reach every apply: `clean` and `purge` (text flow and review
screen), `apply`, `analyze`, `dupes`, every command that shows a plan and
asks before moving (`uninstall`, `trash`, `installer`, `large`, `packages`,
`optimize`, `login-items disable`) and `sapu quarantine expire`. The texts that say how long items
stay and how big the quarantine may get (the review screen, the result of an
apply, `sapu quarantine list` and `expire`) name your `max_age` and
`max_size`, and so does `expires_after` in `--json`.

### [clean] exclude

Rule ids or categories (`system`, `logs`, `browsers`, `dev`, `apps`,
`media`) that are never selected. Their items are still listed. `--exclude`
on the command line adds to this list. A name that matches no rule is an
error: after a rule update renames a rule, an exclusion must not silently
stop working. The list of `sapu clean` titles each group with its rule id.

The scheduled clean leaves these rules out too.

### [purge]

`roots` are the folders `sapu purge` searches when you name none. Each must
exist. Folders on the command line replace the list. `older_than` skips
projects touched more recently; `--older-than` replaces it (`--older-than
0d` turns it off for one run).

### [whitelist]

Paths Sapu must never touch, added to the entries of `sapu whitelist`
(`~/.sapu/whitelist.txt`). They block exactly like those: the path itself,
everything inside it, and every folder that contains it. Sapu never writes
them; `sapu whitelist add` and `remove` change only `whitelist.txt`.
`sapu config show` lists both, and so does `sapu whitelist list`, which
marks the `sapu.toml` paths "from sapu.toml" (read-only: edit the file to
change them; `--json` gives them in `config_entries`).

## What the file cannot do

There is no setting that turns protection off, removes or narrows an entry
of the built-in protection list, or adds anything to its exceptions
(`allow`). The whitelist can only add places Sapu must not touch.
Such keys are unknown keys, and an unknown key is an error.

## Mistakes

Parsing is strict. An unknown key, a value of the wrong type and a value out
of range are errors that name the file and the line:

```
sapu: /Users/me/.config/sapu/sapu.toml: line 3: quarantine.max_age: must be at least 1d, not "12h": undo needs time
sapu: /Users/me/.config/sapu/sapu.toml: line 7: unknown setting "clean.exlude"
```

A broken file stops every command that plans or moves (exit code 1) before
it changes anything, and it stops the scheduled clean. `sapu config path`
still answers.

## Exit codes

Every command uses the same codes.

| Code | Meaning |
|---|---|
| 0 | Done |
| 1 | An error, including a broken `sapu.toml`. Nothing was changed, or the result says what was |
| 2 | A usage mistake (bad flag or argument), or the question was not confirmed. Nothing was changed |
| 3 | Done, but some items were skipped or failed (changed since the plan, in use, protected, ...). The result lists them |
| 130 | Stopped with Ctrl-C. The result lists what was moved before it stopped |

With `--json`, the envelope's `error.code` says the same: see
`schema/v1/README.md`.
