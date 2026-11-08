# Signed rule updates

Sapu's rules are data: the cleanup rules (`rules/cleaners`), the protection
list (`rules/protect`) and the purge types (`rules/purge`). They are built
into the binary, and a signed update can replace the cleanup rules and purge
types, and add to the protection list, without a new binary.

Downloading updates is out of scope until Sapu has an official domain.
Updates are local files that the user installs.

## For users

```
sapu rules status                                   # which rules are in use
sapu rules install rules-2026.11.20.json --sig rules-2026.11.20.json.sig
sapu rules reset                                    # back to the built-in rules
```

All three take `--json` (schema `rules-status.json`; install and reset
return the status after the change).

## For the owner

The tool is `cmd/sapu-rules`. Run it from the source tree with
`go run ./cmd/sapu-rules ...` (always with `GOTOOLCHAIN=local`).

### Once: the signing key

```
go run ./cmd/sapu-rules keygen ~/secure/sapu-rules.key
```

This writes the private key (mode 0600; an existing file is never
overwritten) and prints the public key. Then:

1. Put the public key in `PublicKeys` in `internal/rulesupdate/sign.go`,
   in place of `placeholderKey`. While only the placeholder is there, every
   install is refused with "no signing key configured in this build".
2. Keep the private key off the repository, backed up and offline. Anyone
   who has it can sign rules that every Sapu with this public key accepts.

### Every update

1. Change the files in `rules/` and run `make check`. The rule tests (every
   rule has a fixture, every protected entry survives) are the real review.
2. Pick the version: dotted numbers, newer than the last update and newer
   than `EmbeddedVersion` of the binaries that should take it. Use the date,
   with a fourth number for a second update on the same day:
   `2026.11.20`, `2026.11.20.2`.
3. Pick `min_sapu`: the oldest Sapu release whose rule loader understands
   every field the files use.
4. Build, sign and check:

   ```
   go run ./cmd/sapu-rules bundle -version 2026.11.20 -min-sapu 1.2.0 -o rules-2026.11.20.json rules
   go run ./cmd/sapu-rules sign rules-2026.11.20.json ~/secure/sapu-rules.key
   go run ./cmd/sapu-rules verify rules-2026.11.20.json <public key>
   ```

   `bundle` checks every file the way `sapu rules install` does and refuses
   a bad rule; `sign` refuses a file that is not a valid bundle.
5. Publish both files, `rules-2026.11.20.json` and `rules-2026.11.20.json.sig`.

### When the built-in rules change

Bump `EmbeddedVersion` in `internal/rulesupdate/rulesupdate.go` in the same
change; `TestEmbeddedVersionIsCurrent` fails until you do and prints the new
`embeddedDigest`. A stored update is only used while it is newer than the
built-in rules, so a release that fixes a rule is never hidden by an older
update on the user's Mac.

### Changing the key

Add the new public key to `PublicKeys` next to the old one and release. Sign
new updates with the new key. Remove the old key in a later release; updates
signed only with it then stop being used (Sapu falls back to its built-in
rules and says so).

## The bundle

One JSON document and a detached signature:

```json
{
  "version": "2026.11.20",
  "created": "2026-11-20T09:00:00Z",
  "min_sapu": "1.2.0",
  "cleaners": { "apps.yml": "...", "browsers.yml": "..." },
  "protect": { "core.yml": "..." },
  "purge": { "types.yml": "..." }
}
```

Each map holds every `*.yml` file of one rules folder, file name to text.
Unknown fields are refused. The signature file holds the base64 ed25519
signature of the exact bytes of the JSON file.

Installed updates live in Sapu's state folder:

```
~/.sapu/rules/
  2026.11.20.json        the bundle, byte for byte
  2026.11.20.json.sig    its signature
  active                 "2026.11.20", or "embedded" after sapu rules reset
```

## Safety rules

- **An update can only add protection.** The protection list in use is
  always the built-in list plus the update's entries. Each entry of the
  update becomes a match of its own next to the built-in ones, and a path is
  blocked when any match blocks it, each with only its own `allow`.
  So an update that leaves out a built-in entry, narrows its paths, or
  gives it an `allow`, even under the same id, changes nothing about
  that entry: an `allow` opens only its own entry's match. `TestHostileRuleUpdateKeepsEveryProtection` in the regression
  suite installs such an update and runs every protected case against it.
- **Every rule is checked before install,** by the loaders Sapu uses
  (`rules.Load` for cleanup rules, the protect and purge validators). One
  bad rule refuses the whole update, and nothing is written.
- **The signature is checked on every load,** not only at install. A stored
  update that changed, lost its signature, or cannot be loaded by this Sapu
  is not used: Sapu uses its built-in rules and prints a warning, and
  `sapu rules status` says why.
- **No going back.** Install refuses a version older than or equal to the
  rules in use, and an update whose `min_sapu` is newer than the binary. A
  stored update that is not newer than the built-in rules is not used.
- **Nothing is deleted or renamed.** Install writes three files in place and
  `sapu rules reset` only writes `active`. Old updates stay in
  `~/.sapu/rules/`.
- **The settings file cannot weaken it either:** `sapu.toml` has no key for
  protection (see `docs/config.md`).

What an update does replace, and why that is acceptable: cleanup rules and
purge types come from the update alone. They are checked by the same
validator as the built-in ones (no `**` above the last segment, at least two
folders below `~`, no `src` or `.git` artifacts, ...), they are signed by the
owner, and every item they find still goes through protection, the
whitelist, the open-files check and quarantine.
