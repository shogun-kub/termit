#!/bin/sh
# Termit management server bootstrap.
#
# Installs the server on a Debian host from a published release: fetch it, verify its SHA-256 against
# the release's SHA256SUMS, install `termit-mn` and its unit, give it the probe worker it needs,
# create the configuration and start the service.
#
#   mn.sh --domain mn.example.com --email you@example.com
#
# Re-running it is the upgrade: the binary and the unit are replaced, `termit-mn init` keeps every
# decision already made — domain, listen, master key, the nodes — and the service is restarted.
set -eu

PROGRAM=termit-mn
INSTALL_DIR=/usr/bin
UNIT_DIR=/etc/systemd/system
CONFIG_DIR=/etc/termit/mn
CONFIG=$CONFIG_DIR/config.yaml
PROBER_DIR=/usr/local/lib/termit

# The pinned probe worker, the same Xray-core release the nodes run. Keep in step with
# config.DefaultXrayVersion in internal/uv/config.
PROBER_VERSION=26.11.9

# The release directory. A mirror, a file:// URL or a local path all work; the checksum is what
# matters, not where the bytes came from.
BASE_URL=${TERMIT_RELEASE_BASE:-https://github.com/shogun-kub/termit/releases/download}
XRAY_BASE=${TERMIT_XRAY_BASE:-https://github.com/XTLS/Xray-core/releases/download}

VERSION=
LATEST=0
VERSION_SET=0
DOMAIN=
EMAIL=
MODE=
ACME_CA=
ACME_SKIP=0
API_PREFIX=
ADMIN_TOKEN_OUT=
PROBER=1
DRY_RUN=0
CHECKSUM_FILE=
EXPECTED_SHA256=

say() { printf '%s\n' "$*"; }
die() {
	printf 'mn.sh: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: mn.sh --domain NAME [--email ADDRESS] [options]

Install or upgrade one Termit management server. Must run as root on Debian 13, amd64 or arm64.

Required on a first install:
  --domain NAME            the name this server answers on; its A/AAAA record must already point here

Options:
  --version VERSION        release to install, e.g. v0.4.1 or v0.4.1p1
                           (default: the newest published release)
  --email ADDRESS          ACME account contact
  --mode MODE              enrollment: invite+approval (default), invite or open
  --acme-ca URL            ACME directory; default is Let's Encrypt production
  --acme-skip-tls-verify   do not verify the ACME endpoint (lab only)
  --api-prefix PATH        the path the node API is served under (default: random)
  --admin-token-out PATH   write the admin token to a file instead of printing it
  --prober-version VERSION Xray-core release for the probe worker (default: 26.11.9)
  --no-prober              do not install the probe worker; every check is then unknown
  --dry-run                verify the downloads and print what would be done, change nothing
  --base-url URL|PATH      release directory (default: the GitHub releases page)
  --checksum-file PATH     local SHA256SUMS, when the mirror does not serve one
  --sha256 HEX             expected digest of the binary, for a private build
  -h, --help               this text

The server's own certificate is issued on its first start, so `:80` and `:443` must be free and the
name must already resolve. The unit is installed from the release, so no repository clone is needed.
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
		VERSION_SET=1
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
	--mode)
		need "$@"
		MODE=$2
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
	--api-prefix)
		need "$@"
		API_PREFIX=$2
		shift 2
		;;
	--admin-token-out)
		need "$@"
		ADMIN_TOKEN_OUT=$2
		shift 2
		;;
	--prober-version)
		need "$@"
		PROBER_VERSION=$2
		shift 2
		;;
	--no-prober)
		PROBER=0
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
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown argument $1 (see --help)"
		;;
	esac
done

[ -n "$DOMAIN" ] || die "--domain is required: it is the name the server answers on"
[ "$(id -u)" = 0 ] || die "run this as root"
[ "$(uname -s)" = Linux ] || die "this installs a Linux server; the current system is $(uname -s)"

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported architecture $(uname -m); Termit ships linux/amd64 and linux/arm64" ;;
esac

# A local path is a file:// URL: the same code path, and the same checksum, for a mirror on a USB stick.
case "$BASE_URL" in
/*) BASE_URL="file://$BASE_URL" ;;
esac

# Without --version the release is the newest published one. `releases/latest` is a redirect to its tag,
# so following it answers the question in one request, with no API token and no rate limit — and it
# never returns a pre-release, which is what a participant should install. A mirror or a local directory
# has no such redirect, and there the version has to be named.
if [ "$VERSION_SET" = 0 ]; then
	case "$BASE_URL" in
	*/) BASE_URL=${BASE_URL%/} ;;
	esac
	# The base is the *download* directory; the redirect that names the newest release lives one level up.
	case "$BASE_URL" in
	*/download) LATEST_URL="${BASE_URL%/download}/latest" ;;
	*) LATEST_URL="$BASE_URL/latest" ;;
	esac
	case "$BASE_URL" in
	file://*) die "the newest release cannot be looked up in $BASE_URL; pass --version" ;;
	esac
	command -v curl >/dev/null 2>&1 ||
		die "looking up the newest release needs curl; pass --version to name one"
	tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$LATEST_URL" 2>/dev/null) ||
		die "cannot look up the newest release at $LATEST_URL; pass --version"
	tag=${tag##*/}
	case "$tag" in
	v[0-9]*.[0-9]*.[0-9]*) VERSION=$tag LATEST=1 ;;
	*) die "$LATEST_URL did not point at a release tag (got '$tag'); pass --version" ;;
	esac
fi

case "$VERSION" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) die "--version wants a tag such as v0.4.1 or v0.4.1p1, not '$VERSION'" ;;
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

verify() { # verify FILE WANT; the digest is checked before anything is installed
	command -v sha256sum >/dev/null 2>&1 || die "sha256sum is not available"
	actual=$(sha256sum "$1" | awk '{ print $1 }')
	[ "$actual" = "$2" ] || die "checksum mismatch for $(basename "$1"): expected $2, got $actual"
}

# The expected digest comes from one of three places, in order of precedence: --sha256, --checksum-file,
# the release's own SHA256SUMS. There is no fourth: an unverified binary is not installed.
sums=
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

unitSum() { # unitSum NAME; the release's digest for a file shipped beside the binary
	[ -n "$sums" ] || die "the unit is verified against the release's SHA256SUMS; a private build needs --checksum-file"
	awk -v want="$1" '
		{ name = $2; sub(/^\*/, "", name); if (name == want) { print $1; exit } }
	' "$sums"
}

if [ "$LATEST" = 1 ]; then
	say "release:  $VERSION ($ARCH, the newest published one; pin one with --version)"
else
	say "release:  $VERSION ($ARCH)"
fi
say "fetching: $BASE_URL/$VERSION/$ASSET"
fetch "$BASE_URL/$VERSION/$ASSET" "$WORK/$ASSET" ||
	die "cannot download $BASE_URL/$VERSION/$ASSET"
verify "$WORK/$ASSET" "$expected"
chmod 0755 "$WORK/$ASSET"

say "fetching: $BASE_URL/$VERSION/$PROGRAM.service"
fetch "$BASE_URL/$VERSION/$PROGRAM.service" "$WORK/$PROGRAM.service" ||
	die "cannot download $BASE_URL/$VERSION/$PROGRAM.service"
serviceSum=$(unitSum "$PROGRAM.service")
[ -n "$serviceSum" ] || die "$PROGRAM.service is not listed in the checksum file"
verify "$WORK/$PROGRAM.service" "$serviceSum"

if [ "$DRY_RUN" = 1 ]; then
	say "would install: $INSTALL_DIR/$PROGRAM, $UNIT_DIR/$PROGRAM.service"
	say "would create:  $CONFIG (init), then enable and start $PROGRAM"
	if [ "$PROBER" = 1 ]; then
		say "would install: the $PROBER_VERSION probe worker in $PROBER_DIR"
	fi
	say "dry run: nothing is installed and nothing is changed"
	exit 0
fi

install -m 0755 "$WORK/$ASSET" "$INSTALL_DIR/$PROGRAM"
say "installed: $INSTALL_DIR/$PROGRAM"
install -m 0644 "$WORK/$PROGRAM.service" "$UNIT_DIR/$PROGRAM.service"
say "installed: $UNIT_DIR/$PROGRAM.service"

# `termit-mn init` writes the configuration, the database, the master key and the first admin token. On
# an installed server it keeps every decision already made, so this is also the upgrade path.
install -d -m 0750 "$CONFIG_DIR"
set -- init --config "$CONFIG" --domain "$DOMAIN"
[ -n "$EMAIL" ] && set -- "$@" --email "$EMAIL"
[ -n "$MODE" ] && set -- "$@" --mode "$MODE"
[ -n "$ACME_CA" ] && set -- "$@" --acme-ca "$ACME_CA"
[ "$ACME_SKIP" = 1 ] && set -- "$@" --acme-skip-tls-verify
[ -n "$API_PREFIX" ] && set -- "$@" --api-prefix "$API_PREFIX"
[ -n "$ADMIN_TOKEN_OUT" ] && set -- "$@" --admin-token-out "$ADMIN_TOKEN_OUT"
say "running: $PROGRAM $*"
"$INSTALL_DIR/$PROGRAM" "$@"

# The probe worker is what opens a real connection to a node: without it no node can enroll. The same
# Xray-core release the nodes run, checked against the SHA2-256 line of the `.dgst` file that release
# publishes — the file the node's own pin was taken from.
if [ "$PROBER" = 1 ]; then
	case "$ARCH" in
	amd64) XRAY_ASSET=Xray-linux-64.zip ;;
	arm64) XRAY_ASSET=Xray-linux-arm64-v8a.zip ;;
	esac
	command -v unzip >/dev/null 2>&1 || {
		say "installing unzip, which the probe worker comes in"
		DEBIAN_FRONTEND=noninteractive apt-get install -y unzip >/dev/null 2>&1 ||
			die "cannot install unzip; install it and run this again, or pass --no-prober"
	}
	say "fetching: $XRAY_BASE/v$PROBER_VERSION/$XRAY_ASSET"
	fetch "$XRAY_BASE/v$PROBER_VERSION/$XRAY_ASSET" "$WORK/$XRAY_ASSET" ||
		die "cannot download $XRAY_BASE/v$PROBER_VERSION/$XRAY_ASSET"
	fetch "$XRAY_BASE/v$PROBER_VERSION/$XRAY_ASSET.dgst" "$WORK/$XRAY_ASSET.dgst" ||
		die "cannot download the digest of $XRAY_ASSET; refusing to install an unverified binary"
	xraySum=$(awk '/^SHA2-256=/{print $2}' "$WORK/$XRAY_ASSET.dgst")
	[ -n "$xraySum" ] || die "$XRAY_ASSET.dgst has no SHA2-256 line"
	verify "$WORK/$XRAY_ASSET" "$xraySum"
	install -d -m 0755 "$PROBER_DIR"
	unzip -qo "$WORK/$XRAY_ASSET" -d "$PROBER_DIR" || die "cannot unpack $XRAY_ASSET"
	chmod 0755 "$PROBER_DIR/xray"
	say "installed: $PROBER_DIR/xray ($PROBER_VERSION)"
	# An empty value is what `init` writes; anything else is the operator's decision and stays.
	sed -i "s|^  xray_binary: \"\"|  xray_binary: $PROBER_DIR/xray|" "$CONFIG"
fi

systemctl daemon-reload
systemctl enable "$PROGRAM" >/dev/null 2>&1 || true
say "starting: $PROGRAM (the first start issues the certificate)"
systemctl restart "$PROGRAM"
sleep 3
systemctl is-active --quiet "$PROGRAM" ||
	die "$PROGRAM did not stay up; journalctl -u $PROGRAM -n 50"

if [ -n "$ADMIN_TOKEN_OUT" ] && [ -f "$ADMIN_TOKEN_OUT" ]; then
	TERMIT_MN_ADMIN_TOKEN_FILE=$ADMIN_TOKEN_OUT "$INSTALL_DIR/$PROGRAM" status
else
	say "done: $INSTALL_DIR/$PROGRAM status --token-file <the admin token>"
fi
