#!/bin/sh
# Termit node bootstrap.
#
# Installs one node on a Debian host: fetch a pinned release, verify its SHA-256 against the release's
# SHA256SUMS, install the binary, then let `termit-uv init` converge the host and `termit-uv enroll` register it.
#
# Nothing here is interactive and nothing is piped from a branch: the version is an argument, the
# checksum is checked before the binary is installed, and a mismatch aborts before anything is written.
#
#   uv.sh --version v0.1.0 --domain node1.example.net --email you@example.com \
#         --mn https://mn.example.com --invite ABCD-EFGH-IJKL
#
# Re-running it is the upgrade path: the binary is replaced and `termit-uv init` converges, which never
# regenerates the node's identity, its Xray path or its certificate.
set -eu

PROGRAM=termit-uv
INSTALL_DIR=/usr/bin
CONFIG=/etc/termit/uv/config.yaml

# The release directory. A mirror, a file:// URL or a local path all work; the checksum is what
# matters, not where the bytes came from.
BASE_URL=${TERMIT_RELEASE_BASE:-https://github.com/shogun-kub/termit/releases/download}

VERSION=
DOMAIN=
EMAIL=
MN_URL=
INVITE=
ACME_CA=
WARP=all
WARP_SET=0
NO_ENROLL=0
DRY_RUN=0
CHECKSUM_FILE=
EXPECTED_SHA256=

say() { printf '%s\n' "$*"; }
die() {
	printf 'uv.sh: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: uv.sh --version VERSION [--domain NAME] [options]

Install or upgrade one Termit node. Must run as root on Debian 13, amd64 or arm64.

Required on a first install:
  --version VERSION        release to install, e.g. v0.1.0 or v0.1.0-rc1
  --domain NAME            the node's public name; its A/AAAA record must already point here
  --mn URL                 management server URL, e.g. https://mn.example.com
  --invite CODE            invite code from `termit-mn invite create`

Options:
  --email ADDRESS          ACME account contact
  --acme-ca URL            ACME directory; default is Let's Encrypt production
  --acme-skip-tls-verify   do not verify the ACME endpoint (lab only)
  --warp MODE              all (default), selected or off
  --warp-selected LIST     domains that use WARP in selected mode
  --no-warp                opt out of WARP and exit directly
  --no-enroll              install only; run `termit-uv enroll` yourself
  --dry-run                report what `termit-uv init` would change, touch nothing
  --base-url URL|PATH      release directory (default: the GitHub releases page)
  --checksum-file PATH     local SHA256SUMS, when the mirror does not serve one
  --sha256 HEX             expected digest of the binary, for a private build
  --install-dir DIR        where the binary goes (default: /usr/bin)
  --config PATH            configuration path (default: /etc/termit/uv/config.yaml)
  -h, --help               this text

The invite code is a secret: it is passed on the command line, so it appears in `ps` for the duration
of the run. Revoke it afterwards if that matters to you (`termit-mn invite revoke`).
EOF
}

need() {
	[ $# -ge 2 ] || die "$1 needs a value"
}

while [ $# -gt 0 ]; do
	case $1 in
	--version)
		need "$@"
		VERSION=$2
		shift 2
		;;
	--domain)
		need "$@"
		DOMAIN=$2
		shift 2
		;;
	--email)
		need "$@"
		EMAIL=$2
		shift 2
		;;
	--mn)
		need "$@"
		MN_URL=$2
		shift 2
		;;
	--invite)
		need "$@"
		INVITE=$2
		shift 2
		;;
	--acme-ca)
		need "$@"
		ACME_CA=$2
		shift 2
		;;
	--acme-skip-tls-verify)
		ACME_SKIP=1
		shift
		;;
	--warp)
		need "$@"
		WARP=$2
		WARP_SET=1
		shift 2
		;;
	--warp-selected)
		need "$@"
		WARP_SELECTED=$2
		shift 2
		;;
	--no-warp)
		WARP=off
		WARP_SET=1
		shift
		;;
	--no-enroll)
		NO_ENROLL=1
		shift
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	--base-url)
		need "$@"
		BASE_URL=$2
		shift 2
		;;
	--checksum-file)
		need "$@"
		CHECKSUM_FILE=$2
		shift 2
		;;
	--sha256)
		need "$@"
		EXPECTED_SHA256=$2
		shift 2
		;;
	--install-dir)
		need "$@"
		INSTALL_DIR=$2
		shift 2
		;;
	--config)
		need "$@"
		CONFIG=$2
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown argument $1 (see --help)"
		;;
	esac
done

[ -n "$VERSION" ] || die "--version is required; this installer never guesses a release"
[ "$(id -u)" = 0 ] || die "run this as root"
[ "$(uname -s)" = Linux ] || die "this installs a Linux node; the current system is $(uname -s)"

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported architecture $(uname -m); Termit ships linux/amd64 and linux/arm64" ;;
esac

# A local path is a file:// URL: the same code path, and the same checksum, for a mirror on a USB stick.
case "$BASE_URL" in
/*) BASE_URL="file://$BASE_URL" ;;
esac

ASSET="${PROGRAM}_${VERSION}_linux_${ARCH}"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT INT TERM

fetch() { # fetch URL DEST; non-zero on failure, so the caller can say what was being fetched
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1" || return 1
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1" || return 1
	else
		die "neither curl nor wget is installed"
	fi
}

# The expected digest comes from one of three places, in order of precedence: --sha256, --checksum-file,
# the release's own SHA256SUMS. There is no fourth: an unverified binary is not installed.
expected=
if [ -n "$EXPECTED_SHA256" ]; then
	expected=$EXPECTED_SHA256
	say "checksum: from --sha256"
else
	sums=$CHECKSUM_FILE
	if [ -z "$sums" ]; then
		sums="$WORK/SHA256SUMS"
		fetch "$BASE_URL/$VERSION/SHA256SUMS" "$sums" ||
			die "the release has no SHA256SUMS; refusing to install an unverified binary"
	fi
	expected=$(awk -v want="$ASSET" '
		{ name = $2; sub(/^\*/, "", name); if (name == want) { print $1; exit } }
	' "$sums")
	[ -n "$expected" ] || die "$ASSET is not listed in the checksum file; refusing to install it"
	say "checksum: from $(basename "$sums")"
fi

say "release:  $VERSION ($ARCH)"
say "fetching: $BASE_URL/$VERSION/$ASSET"
fetch "$BASE_URL/$VERSION/$ASSET" "$WORK/$ASSET" ||
	die "cannot download $BASE_URL/$VERSION/$ASSET"

command -v sha256sum >/dev/null 2>&1 || die "sha256sum is not available"
actual=$(sha256sum "$WORK/$ASSET" | awk '{ print $1 }')
if [ "$actual" != "$expected" ]; then
	die "checksum mismatch for $ASSET: expected $expected, got $actual"
fi
say "verified: $actual"
chmod 0755 "$WORK/$ASSET"

if [ "$DRY_RUN" = 1 ]; then
	# The binary is verified but not installed: it runs from the download directory so the host is
	# untouched even when the download itself is suspect.
	say "dry run: nothing is installed and nothing is changed"
	PROGRAM_BIN="$WORK/$ASSET"
else
	install -m 0755 "$WORK/$ASSET" "$INSTALL_DIR/$PROGRAM"
	say "installed: $INSTALL_DIR/$PROGRAM"
	PROGRAM_BIN="$INSTALL_DIR/$PROGRAM"
fi

# `termit-uv init` is the convergence: on a first run it installs everything, on a re-run it changes only
# what drifted and never regenerates the identity, the path or the certificate.
set -- init --config "$CONFIG"
if [ -n "$DOMAIN" ]; then
	set -- "$@" --domain "$DOMAIN"
fi
if [ -n "$EMAIL" ]; then
	set -- "$@" --email "$EMAIL"
fi
if [ -n "${ACME_CA:-}" ]; then
	set -- "$@" --acme-ca "$ACME_CA"
fi
if [ "${ACME_SKIP:-0}" = 1 ]; then
	set -- "$@" --acme-skip-tls-verify
fi
if [ "$WARP_SET" = 1 ]; then
	if [ "$WARP" = selected ]; then
		[ -n "${WARP_SELECTED:-}" ] || die "--warp selected needs --warp-selected"
		set -- "$@" --warp selected --warp-selected "$WARP_SELECTED"
	else
		set -- "$@" --warp "$WARP"
	fi
fi
if [ "$DRY_RUN" = 1 ]; then
	set -- "$@" --dry-run
fi

say "running: $PROGRAM $*"
"$PROGRAM_BIN" "$@"

if [ "$DRY_RUN" = 1 ]; then
	exit 0
fi
if [ "$NO_ENROLL" = 1 ]; then
	say "not enrolling: run \`$PROGRAM enroll --mn URL --invite CODE\` when you are ready"
	exit 0
fi
if [ -z "$MN_URL" ]; then
	say "no --mn given: the node is installed and stands alone"
	exit 0
fi
[ -n "$INVITE" ] || die "--invite is required to enroll; pass --no-enroll to skip enrollment"

say "enrolling with $MN_URL"
"$PROGRAM_BIN" enroll --mn "$MN_URL" --invite "$INVITE"
say "done: $PROGRAM status"
