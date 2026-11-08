#!/bin/sh
# Installs the sapu command line tool on a Mac:
#
#   curl -fsSL https://sapu.khaisa.studio/install.sh | sh
#
# It downloads the newest release archive from GitHub, checks its SHA-256
# checksum against the release's checksums.txt, and puts sapu in
# ~/.local/bin. It never uses sudo and changes nothing else.
#
#   SAPU_VERSION=0.1.0       install that version instead of the newest
#   SAPU_INSTALL_DIR=/path   install into that folder instead of ~/.local/bin
#
# docs/verify.md shows how to also check the cosign signature by hand.
set -eu

repo="khmuhtadin/sapu"
dir="${SAPU_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	printf 'sapu install: %s\n' "$1" >&2
	exit 1
}

[ "$(uname -s)" = "Darwin" ] || fail "Sapu runs on macOS only."
for tool in curl shasum tar; do
	command -v "$tool" >/dev/null 2>&1 || fail "$tool is missing."
done

version="${SAPU_VERSION:-}"
if [ -z "$version" ]; then
	# The newest release, pre-releases included: every 0.x release is one.
	version=$(curl -fsSL "https://api.github.com/repos/$repo/releases?per_page=1" |
		sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || fail "could not find the newest release on GitHub."
fi
version=${version#v}

archive="sapu_${version}_darwin_universal.tar.gz"
base="https://github.com/$repo/releases/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

printf 'Downloading sapu %s...\n' "$version"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "could not download $base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "could not download checksums.txt"

grep "  $archive\$" "$tmp/checksums.txt" >"$tmp/expected" || fail "checksums.txt does not list $archive."
(cd "$tmp" && shasum -a 256 -c expected >/dev/null 2>&1) ||
	fail "the download does not match its checksum. Nothing was installed."

tar -xzf "$tmp/$archive" -C "$tmp" sapu
mkdir -p "$dir"
install -m 755 "$tmp/sapu" "$dir/sapu"
printf 'Installed %s in %s\n' "$("$dir/sapu" version)" "$dir"

case ":$PATH:" in
*":$dir:"*) ;;
*)
	printf '\n%s is not on your PATH yet. Add it with:\n' "$dir"
	# shellcheck disable=SC2016 # $PATH stays literal: it is a line to paste
	printf '  echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc && exec zsh\n' "$dir"
	;;
esac
printf '\nNext: run "sapu" to start, or "sapu clean" to review what can go.\n'
printf 'Nothing moves until you say yes, and "sapu undo" puts it back.\n'
