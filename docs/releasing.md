# Releasing Sapu

For the owner. A release is a git tag. Pushing the tag starts
`.github/workflows/release.yml`, which runs `make check` on macos-15 and then
GoReleaser: a universal binary, an archive, `checksums.txt`, a cosign signature
of `checksums.txt`, and a GitHub release with a changelog from the commit
messages. If the Apple secrets below exist, it also signs and notarizes the
binary.

## What a 0.x release is

Sapu 0.x releases are **unsigned developer previews**:

- the archive has SHA-256 checksums and a cosign (Sigstore) signature of
  `checksums.txt`;
- the binary has no Apple Developer ID signature and is not notarized;
- the release is marked as a pre-release on GitHub, and its notes start with a
  header that says so (`release.header` in `.goreleaser.yml`);
- `docs/verify.md` tells users what that means and how macOS treats the binary.

`.goreleaser.yml` sets `release.prerelease: true`, so every 0.x release is
created as a pre-release and is never public as a stable release. At 1.0,
change it to `auto`, which marks only tags with a suffix such as `-beta.1`.

When you start signing with a Developer ID (all five secrets below set), edit
the `release.header` text in `.goreleaser.yml` and the "What a 0.x download is"
section of `docs/verify.md` in the same change, so they stay true. A tag push
needs no other change: signing turns on by itself when `MACOS_SIGN_P12` is not
empty.

## Owner gates

- **Only the owner pushes a release tag.** Agents may prepare a release and push
  a beta tag only when the owner says so.
- **At most one release every two weeks.** Fixes in between ship as betas.
- Enforce the first gate in GitHub: create an environment named `release`
  (Settings > Environments), add yourself as required reviewer, and keep any
  secrets in it. The release job waits for your approval.

  The workflow names the environment, so it works even if you never create it:
  GitHub then creates an empty `release` environment on the first run, with no
  reviewers, and the job starts without waiting for anyone. Create it with
  required reviewers **before** the first tag, or the first release is not
  gated.
- Any change to a `risk: risky` rule, anything that uses `sudo`, and a breaking
  change to the JSON output also need the owner.

## One-time setup

### Repository

- The repository is `github.com/khmuhtadin/sapu`, the same as the Go module
  path. `.goreleaser.yml` names it in `release.github`, and the cosign identity
  pattern in `docs/verify.md` and `README.md` names it too. Change all of them
  together if the repository ever moves.
- Create the `release` environment as described above.
- No secret is needed for a 0.x release. Cosign uses the workflow's identity,
  which is why the release job has `id-token: write`. The job also has
  `contents: write` to create the release. `GITHUB_TOKEN` is provided by
  GitHub.

### Apple secrets (optional, for later)

Skip this until you want Apple-signed releases. Create these in the `release`
environment (or as repository secrets). A secret that does not exist reaches
GoReleaser as an empty string, and the sign and notarize step is skipped.

| Secret | What to put in it |
|---|---|
| `MACOS_SIGN_P12` | Your "Developer ID Application" certificate with its private key, exported from Keychain Access as `.p12`, then base64-encoded: `base64 -i cert.p12 \| pbcopy` |
| `MACOS_SIGN_PASSWORD` | The password you set when exporting the `.p12` |
| `MACOS_NOTARY_KEY` | The App Store Connect API key (`AuthKey_XXXX.p8`), base64-encoded: `base64 -i AuthKey_XXXX.p8 \| pbcopy` |
| `MACOS_NOTARY_KEY_ID` | The key's ID, shown next to the key in App Store Connect |
| `MACOS_NOTARY_ISSUER_ID` | The issuer ID at the top of the API keys page in App Store Connect |

You need a paid Apple Developer Program membership. The API key is created at
App Store Connect > Users and Access > Integrations. Names and meanings are the
same ones used in `.goreleaser.yml`. Set all five: with `MACOS_SIGN_P12` set and
another one missing, the step fails and nothing is published. If the signing
step fails with a decoding error, check the base64 encoding of the first and
third secret.

## Dependency notices

`THIRD_PARTY_NOTICES` holds the license text of every Go module linked into the
binary, and the archive ships it. After any change to `go.mod`, run
`make notices` and commit the file. `go test ./...` fails with "out of date" if
you forget. The test is skipped (with a message) when the module cache lacks a
module; `go mod download` fills it.

## Cut a beta

Betas are tags `vX.Y.Z-beta.N`, published as GitHub pre-releases. They are not
put in Homebrew.

1. Make sure `main` is green in CI. The release workflow runs `make check`, but
   the runner has no golangci-lint, so the lint step only runs in `ci.yml`.
2. Tag and push:
   ```sh
   git checkout main && git pull
   git tag v0.2.0-beta.1
   git push origin v0.2.0-beta.1
   ```
3. Watch the `release` workflow. Approve the `release` environment if you set
   one up.
4. Download the pre-release and follow `docs/verify.md` yourself.

A beta was tagged wrongly? Delete the GitHub release and the tag, fix, and tag
`beta.N+1`. Don't reuse a tag name.

## Cut a release (v0.1.0 and later)

Only the owner. At most once every two weeks.

1. Check the last release tag is at least 14 days old:
   `git tag --list 'v*' --sort=-creatordate`.
2. Run the beta steps first and use the beta for a few days. Read
   `sapu history` and `sapu doctor` on a test Mac. (For the very first release,
   v0.1.0, there is no earlier tag to wait for.)
3. Run locally: `make check` and `make release-snapshot` (needs goreleaser).
   Check that `THIRD_PARTY_NOTICES` is current (`make notices`, no diff).
4. Tag and push:
   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```
5. Approve the workflow. When it finishes, the release should be marked
   "Pre-release" while the version starts with `0.`. Download it and verify as
   in `docs/verify.md`: checksum and cosign, and the quarantine behavior for a
   browser download. If Apple signing is on, check `codesign` too.
6. Install it the way the README says, with the `curl` commands and with
   `go install github.com/khmuhtadin/sapu/cmd/sapu@v0.1.0`, and run
   `sapu version`.

### Homebrew (later)

There is no tap yet, and the README says so. When you add one, create the
repository `khmuhtadin/homebrew-sapu` and copy `packaging/homebrew/sapu.rb` to
`Formula/sapu.rb` there. Replace `<VERSION>`, `<SHA256>` (the line for the
`.tar.gz` in `checksums.txt`) and the homepage, then:

```sh
brew install --build-from-source ./Formula/sapu.rb
brew test sapu
```

and push. (`brews` in GoReleaser is deprecated, so the formula is not
generated.) Then change the Homebrew paragraph in the README's Install section
and in `docs/verify.md`.

## Try a build locally

- `make build-universal` writes a universal binary to `bin/sapu`.
- `make release-snapshot` runs the whole GoReleaser pipeline into `dist/`
  without publishing or signing. Needs `brew install goreleaser`.
- `goreleaser check` validates `.goreleaser.yml`.

## When something fails

- **make check fails in the workflow:** nothing was published. Fix on `main`,
  delete the tag, and tag again.
- **Notarization times out or is rejected:** the workflow fails before it
  publishes. Check the Apple credentials and run it again by pushing a new tag.
- **Cosign fails:** confirm the job has `id-token: write`. Nothing is published
  if signing fails.
- **A bad release is out:** mark the release as a pre-release or delete it, tell
  users, and ship a fixed version. This is the one case where the two-week rule
  bends, with the owner's OK.

## Pinned versions

Actions in `release.yml` are pinned to commit SHAs with a version comment, and
cosign (`cosign-release`) and GoReleaser (`version`) versions are pinned too. To
update one, look up the new tag's commit
(`gh api repos/<owner>/<repo>/git/ref/tags/<tag>`), replace the SHA and the
comment together, and run a beta.
