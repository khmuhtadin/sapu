#!/bin/sh
# Builds the npm package "@khmuhtadin/sapu" from a published GitHub release.
#
#   packaging/npm/build.sh 0.1.0 packaging/npm/dist
#
# It downloads the release archive and checksums.txt, checks the archive's
# SHA-256, and puts the binary, the license files, package.json (with the
# version set) and the npm README into OUTDIR. It prints the npm publish
# command and does not run it. It refuses if the checksum does not match.
set -eu

repo="khmuhtadin/sapu"

fail() {
	printf 'build.sh: %s\n' "$1" >&2
	exit 1
}

[ $# -eq 2 ] || fail "usage: build.sh VERSION OUTDIR"
version=${1#v}
out=$2

case "$version" in
*[!0-9A-Za-z.-]* | "") fail "VERSION must look like 0.1.0" ;;
esac
for tool in curl shasum tar sed; do
	command -v "$tool" >/dev/null 2>&1 || fail "$tool is missing."
done
if [ -e "$out" ] && [ -n "$(ls -A "$out")" ]; then
	fail "$out already exists and is not empty."
fi

here=$(cd "$(dirname "$0")" && pwd)
archive="sapu_${version}_darwin_universal.tar.gz"
base="https://github.com/$repo/releases/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "could not download $base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "could not download checksums.txt"

grep "  $archive\$" "$tmp/checksums.txt" >"$tmp/expected" || fail "checksums.txt does not list $archive."
(cd "$tmp" && shasum -a 256 -c expected >/dev/null 2>&1) ||
	fail "the download does not match its checksum. Nothing was built."

mkdir -p "$out"
tar -xzf "$tmp/$archive" -C "$out" sapu LICENSE NOTICE THIRD_PARTY_LICENSES THIRD_PARTY_NOTICES
sed "s/^  \"version\": \".*\",\$/  \"version\": \"$version\",/" "$here/package.json" >"$out/package.json"
grep -q "\"version\": \"$version\"" "$out/package.json" || fail "could not set the version in package.json."
cp "$here/README.md" "$out/README.md"

printf 'Built sapu %s in %s (checksum verified).\n' "$version" "$out"
printf 'Publish it yourself with:\n  npm publish %s\n' "$out"
