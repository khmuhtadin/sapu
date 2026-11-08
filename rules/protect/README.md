# Protection list ("caches that are really data", blueprint S4)

Nothing that equals, sits inside, or contains one of these paths is ever
touched. There is no switch to turn this off. Each entry cites the data-loss
case it prevents where there is one. Format: `internal/protect/protect.go`.

## Writing an entry

- `id` is unique across all files. `reason.en` says what is lost.
- Paths start with `~/`. Each segment may use `*`, `?` and `[...]`. `**` is not
  allowed.
- Matching is case-insensitive and also blocks every parent folder (a path that
  *contains* the entry). Protecting `~/.claude` therefore also blocks `~`.
  Keep paths as narrow as the real data, so cache rules next to them still work.
- Do not protect the cache folders of apps that have a cleanup rule (browsers,
  Slack, Discord, Spotify, ...). Protect the data folders next to them.
- Never remove or loosen an entry without the owner's approval.

## Exceptions (`allow`)

An entry may let one dedicated command offer single items inside it:

```yaml
  allow:
    - source: backups   # plan.Item.Source of the command
      paths:
        - ~/Library/Application Support/MobileSync/Backup/*
```

An item passes only when its source is `source` and its path matches one of
`paths` exactly, segment by segment with the same number of segments. The
example lets one backup folder through, never MobileSync, `Backup` itself or
anything inside a backup. The whitelist always wins. Cleanup rules, purge and
analyze never get an exception, and an exception in a rule update opens only
that update's own entry. Adding one needs the owner's approval.
