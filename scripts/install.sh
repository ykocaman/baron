#!/bin/sh
# BARON install script. Detects platform/arch, downloads
# the requested (default: latest) release binary from GitHub Releases,
# verifies its SHA256 against SHA256SUMS.txt, and installs it.
#
# Usage:
#   curl -fsSL https://get.baron.dev/install.sh | sh
#   curl -fsSL https://get.baron.dev/install.sh | sh -s -- --version v0.1.0
#   curl -fsSL https://get.baron.dev/install.sh | sh -s -- --dry-run
set -eu

REPO="baron-cli/baron"
VERSION="latest"
DRY_RUN=0
INSTALL_DIR=""

usage() {
	cat <<'EOF'
BARON install script

  --version <ver>   Install a specific release (default: latest)
  --dry-run         Print what would happen without installing anything
  -h, --help        Show this help
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		VERSION="$2"
		shift 2
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "unknown argument: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

# detect_platform prints "<os>-<arch>" in BARON's release-asset naming
#.
detect_platform() {
	os=$(uname -s)
	arch=$(uname -m)
	case "$os" in
	Darwin) os="darwin" ;;
	Linux) os="linux" ;;
	*)
		echo "unsupported OS: $os (v1 targets macOS)" >&2
		exit 1
		;;
	esac
	case "$arch" in
	x86_64 | amd64) arch="amd64" ;;
	arm64 | aarch64) arch="arm64" ;;
	*)
		echo "unsupported architecture: $arch" >&2
		exit 1
		;;
	esac
	echo "${os}-${arch}"
}

# install_dir picks the first writable candidate, preferring /usr/local/bin
#.
install_dir() {
	if [ -n "$INSTALL_DIR" ]; then
		echo "$INSTALL_DIR"
		return
	fi
	if [ -w /usr/local/bin ] 2>/dev/null; then
		echo /usr/local/bin
		return
	fi
	echo "${HOME}/.local/bin"
}

platform=$(detect_platform)
dest=$(install_dir)
asset="baron-${VERSION}-${platform}.tar.gz"
url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
sums_url="https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS.txt"

echo "platform:    ${platform}"
echo "version:     ${VERSION}"
echo "install to:  ${dest}/baron"
echo "asset:       ${url}"

if [ "$DRY_RUN" = 1 ]; then
	echo "(dry run: nothing downloaded or installed)"
	exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading ${asset}..."
curl -fsSL "$url" -o "${tmp}/${asset}"
curl -fsSL "$sums_url" -o "${tmp}/SHA256SUMS.txt"

echo "verifying checksum..."
(cd "$tmp" && grep " ${asset}\$" SHA256SUMS.txt | shasum -a 256 -c -) ||
	{
		echo "checksum verification failed; aborting" >&2
		exit 1
	}

tar -xzf "${tmp}/${asset}" -C "$tmp"
mkdir -p "$dest"
install -m 0755 "${tmp}/baron" "${dest}/baron"

echo "installed ${dest}/baron"
if command -v baron >/dev/null 2>&1; then
	baron doctor || true
else
	echo "note: ${dest} is not on PATH; add it or move the binary"
fi
