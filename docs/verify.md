# Verify your download

Sapu can move your files, so check that the binary you downloaded is the one we
built. This takes a minute.

## What a 0.x download is

Sapu 0.x releases are **developer previews**. Each one has:

- a SHA-256 checksum of the archive, in `checksums.txt`;
- a [cosign](https://docs.sigstore.dev/cosign/) (Sigstore) signature of
  `checksums.txt`, which proves that GitHub Actions built it from this
  repository.

A 0.x download is **not** signed with an Apple Developer ID and **not**
notarized by Apple. Apple cannot vouch for it. Steps 1 and 2 below are how you
check it yourself. Developer ID signing and notarization are planned for a
later release; this page will say so when a release has them.

## Official sources

Sapu's command-line tool is distributed only through:

- the GitHub repository, [github.com/khmuhtadin/sapu](https://github.com/khmuhtadin/sapu),
  and its [Releases page](https://github.com/khmuhtadin/sapu/releases);
- the Homebrew tap [khmuhtadin/homebrew-sapu](https://github.com/khmuhtadin/homebrew-sapu)
  (`brew install khmuhtadin/sapu/sapu`), which installs the release archive and
  checks its SHA-256;
- the installer `https://sapu.khaisa.studio/install.sh`, which downloads the
  release archive and checks its SHA-256 against `checksums.txt` (it does not
  check the cosign signature; do that by hand as below if you want it);
- the npm package [`@khmuhtadin/sapu`](https://www.npmjs.com/package/@khmuhtadin/sapu) (`npx @khmuhtadin/sapu`),
  which carries the same binary as the release archive;
- `go install github.com/khmuhtadin/sapu/cmd/sapu@vX.Y.Z`, which builds from
  that repository.

The website is [sapu.khaisa.studio](https://sapu.khaisa.studio). Sites with
similar names, ads for "Sapu cleaner", and files sent to you in chat are not
ours. If a download did not come from one of these, do not run it.

Every release has these files:

| File | What it is |
|---|---|
| `sapu_<version>_darwin_universal.tar.gz` | The binary for Apple Silicon and Intel, with LICENSE, NOTICE, THIRD_PARTY_LICENSES, THIRD_PARTY_NOTICES and README |
| `checksums.txt` | SHA-256 of every archive |
| `checksums.txt.sigstore.json` | Signature of `checksums.txt` (cosign, keyless) |

`<version>` is the tag without the `v`: for the tag `v0.1.0` the archive is
`sapu_0.1.0_darwin_universal.tar.gz`.

## 1. Check the checksum

Put the archive, `checksums.txt` and the signature in one folder, then:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
```

You should see `sapu_<version>_darwin_universal.tar.gz: OK`. Anything else
means the file is damaged or was changed. Delete it.

A checksum only helps if `checksums.txt` itself is genuine. That is what step 2
checks.

## 2. Check the signature on the checksums

Releases are signed with cosign using keyless signing. There is no private key
to steal. The signing certificate says which GitHub workflow produced the file,
and the signature is recorded in a public log. Install cosign
(`brew install cosign`), then:

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/khmuhtadin/sapu/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

`Verified OK` means `checksums.txt` was produced by the release workflow of the
`khmuhtadin/sapu` repository for a version tag. If cosign reports any other
identity or fails, do not use the download.

## 3. Running an unsigned binary on macOS

How macOS treats `sapu` depends on how you downloaded it.

- **Downloaded with `curl` or `gh release download`:** the file has no
  quarantine flag, and Gatekeeper does not check it. `sapu` runs from the
  Terminal. This is why the install commands in the README use `curl`.
- **Downloaded with a browser** (or received by AirDrop or chat): macOS marks
  the download as quarantined, and opening the archive in Finder passes the
  mark on to `sapu`. Because the binary is not notarized, macOS can refuse to
  run it with "cannot be opened because Apple cannot check it for malicious
  software". After you have done steps 1 and 2, remove the mark:

  ```sh
  tar -xzf sapu_<version>_darwin_universal.tar.gz
  xattr -d com.apple.quarantine sapu
  ```

  Do this only for a file you have verified. `xattr -l sapu` shows whether the
  flag is there (`com.apple.quarantine`); if it prints nothing, there is
  nothing to remove.

What you will see with `codesign` on a 0.x binary:

```sh
codesign -dv --verbose=2 sapu
```

shows `Signature=adhoc` and `TeamIdentifier=not set`. That is the minimal
signature Go's linker adds to every Apple Silicon binary so the kernel will
run it. It names no developer and proves nothing about who built the file.
Only steps 1 and 2 do.

## When releases are Apple-signed

Once a release is signed with a Developer ID and notarized, its release notes
will say so, and you can check it:

```sh
codesign -dv --verbose=4 sapu
codesign --verify --strict --verbose=2 sapu
```

`codesign -dv` should show `Authority=Developer ID Application: ...`, a
`TeamIdentifier`, and `Timestamp=`. `codesign --verify` should say `valid on
disk`. `spctl -a -vv` can answer "does not seem to be an app" for a bare
command-line tool even when the signature is fine; rely on the `codesign`
checks. Steps 1 and 2 apply to every release.

## If something does not match

Do not run the binary. Download again from the
[Releases page](https://github.com/khmuhtadin/sapu/releases). If it still
fails, open an issue on the repository and include the output of the command
that failed.
