#!/bin/sh
# Install githints on Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/cjrdz/githints/main/install.sh | sh
#
# Options, as environment variables:
#   GITHINTS_VERSION   tag to install (default: the latest release)
#   GITHINTS_BIN_DIR   where to put the binary (default: see pick_bin_dir)
#
# The install directory matters more here than for most tools. `githints init`
# records the binary's path in each repository's git hooks, so installing to a
# stable location means those hooks keep working. Hooks fall back to PATH if
# the recorded path disappears, but a stable path avoids relying on that.

set -eu

REPO="cjrdz/githints"

die() {
	echo "githints install: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

detect_platform() {
	os=$(uname -s)
	case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "unsupported OS: $os (Windows: use install.ps1)" ;;
	esac

	arch=$(uname -m)
	case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "unsupported architecture: $arch" ;;
	esac

	echo "${os}_${arch}"
}

latest_version() {
	# The redirect from /releases/latest carries the tag, which avoids both a
	# JSON dependency and the API rate limit an unauthenticated request hits.
	url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
		die "could not reach GitHub to find the latest release"
	tag=${url##*/}
	[ -n "$tag" ] && [ "$tag" != "latest" ] || die "could not determine the latest version"
	echo "$tag"
}

# pick_bin_dir chooses a directory that is on PATH and writable, preferring a
# system-wide one when we can write to it. Installing somewhere not on PATH is
# the most common way an install "works" but the command is not found.
pick_bin_dir() {
	if [ -n "${GITHINTS_BIN_DIR:-}" ]; then
		echo "$GITHINTS_BIN_DIR"
		return
	fi
	for candidate in /usr/local/bin "$HOME/.local/bin"; do
		if [ -d "$candidate" ] && [ -w "$candidate" ]; then
			echo "$candidate"
			return
		fi
	done
	# Nothing writable exists yet; ~/.local/bin is the one we may create.
	echo "$HOME/.local/bin"
}

main() {
	need curl
	need tar
	need uname

	platform=$(detect_platform)
	version=${GITHINTS_VERSION:-$(latest_version)}
	number=${version#v}
	bin_dir=$(pick_bin_dir)

	case "$platform" in
	*_*) : ;;
	*) die "internal error: bad platform $platform" ;;
	esac

	archive="githints_${number}_${platform}.tar.gz"
	base="https://github.com/$REPO/releases/download/$version"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	echo "githints $version ($platform) -> $bin_dir"

	curl -fsSL "$base/$archive" -o "$tmp/$archive" ||
		die "download failed: $base/$archive"

	# Verify against the published checksums. A tool that installs itself into
	# every repository's git hooks should not skip this.
	if curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" 2>/dev/null; then
		if command -v sha256sum >/dev/null 2>&1; then
			expected=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
			actual=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
		elif command -v shasum >/dev/null 2>&1; then
			expected=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
			actual=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
		else
			expected=""
		fi
		if [ -n "$expected" ]; then
			[ "$expected" = "$actual" ] || die "checksum mismatch for $archive"
			echo "checksum ok"
		else
			echo "githints install: no sha256 tool found; skipping verification" >&2
		fi
	else
		echo "githints install: checksums.txt unavailable; skipping verification" >&2
	fi

	tar -xzf "$tmp/$archive" -C "$tmp" githints || die "could not extract the archive"

	mkdir -p "$bin_dir" || die "could not create $bin_dir"
	# Replace by rename so a running process keeps its open file and the
	# destination is never half-written.
	mv "$tmp/githints" "$bin_dir/githints.new" && mv "$bin_dir/githints.new" "$bin_dir/githints" ||
		die "could not write to $bin_dir (try: GITHINTS_BIN_DIR=\$HOME/.local/bin)"
	chmod 0755 "$bin_dir/githints"

	echo "installed $bin_dir/githints"
	"$bin_dir/githints" version >/dev/null 2>&1 ||
		echo "githints install: the binary did not run; the download may be for the wrong platform" >&2

	case ":$PATH:" in
	*":$bin_dir:"*) ;;
	*)
		echo
		echo "$bin_dir is not on your PATH. Add it:"
		echo "  echo 'export PATH=\"$bin_dir:\$PATH\"' >> ~/.profile"
		;;
	esac

	echo
	echo "Next: run 'githints init' inside a repository you want tracked."
}

main "$@"
