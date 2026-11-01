#!/usr/bin/env bash
# NebulaCtrl installer.
#
#   curl -fsSL https://<host>/install.sh | sudo bash
#   curl -fsSL https://<host>/install.sh | sudo bash -s -- --domain nebula.example.com --email you@example.com
#   sudo bash deploy/install/install.sh [options]        # from a checkout
#
# Installs the control plane (the server with the web UI, and Postgres 17) on
# this machine with Docker Compose, in /opt/nebulactrl, and enrolls the machine
# as the first server, so apps can be deployed here right away. Every step is
# idempotent: re-running upgrades and repairs, it never regenerates a secret
# that exists and never changes SSH configuration.
# An upgrade whose new control plane does not become healthy is rolled back: the
# previous image (and its compose file and settings) is put back and the installer
# exits with status 4. The control-plane backup it took first is the way back for data.
#
# What it does (each optional step has a --no-* flag):
#   - installs Docker Engine from Docker's official repository when it is missing
#   - applies safe Docker daemon defaults (log rotation, live-restore,
#     userland-proxy off, an address pool away from the agent's)
#   - generates every secret and keeps them in /opt/nebulactrl/.env (mode 0600)
#   - runs Postgres on a private network (SCRAM auth, tuned to the RAM) and the
#     control plane in a hardened container
#   - serves the dashboard over HTTPS with Let's Encrypt when --domain is given,
#     else over plain HTTP on http://<ip>:8000 (with a warning)
#   - schedules a daily control-plane backup to /var/backups/nebulactrl
#   - installs the agent on this host (the first server, "deploy apps here")
#   - firewall (ufw / firewalld): SSH, 80, 443/tcp, 443/udp only
#   - automatic security updates, fail2ban for SSH
#   - Lynis hardening audit and curated safe fixes
# Then it prints the dashboard URL and the one-time setup URL.
#
# Supported: Debian 12/13, Ubuntu 22.04, 24.04 and 26.04, RHEL/Rocky/AlmaLinux 9; amd64 and arm64.
set -Eeuo pipefail
# Predictable tool output, and no locale warnings from perl/dpkg on minimal images.
export LC_ALL=C

NC_INSTALLER_VERSION=1
export NC_DRY_RUN=0
NC_SELF=${BASH_SOURCE[0]:-$0}
NC_SRC_DIR=$(cd "$(dirname "$NC_SELF")" 2>/dev/null && pwd || echo /nonexistent)
# Releases are signed in CI with minisign (.github/workflows/release.yml). This is the public
# key, in the minisign format; the same key is compiled into the control plane and the agent
# (internal/releasesign), and a test keeps the two equal. Everything this installer takes on
# trust is checked against it: see "release verification" below.
NC_RELEASE_PUBKEY=RWSfYAoo0hKcaKeFYcOsofTSgfZdb2GVo/jjE4pfxaDYSi4SSsC6inQP
NC_RELEASE_REPO=nebulactrl/nebulactrl
# Where the signed files of a release are published: nebulactrl.com/releases/<version>/ (and
# .../latest/ for the newest), next to the release feed. The repository's own release assets carry
# the same files.
NC_RELEASE_SITE=https://nebulactrl.com/releases

# ---- output ------------------------------------------------------------------
if [ -t 1 ]; then NC_B=$'\033[1m'; NC_R=$'\033[0m'; NC_Y=$'\033[33m'; NC_G=$'\033[32m'; NC_RED=$'\033[31m'; else NC_B=''; NC_R=''; NC_Y=''; NC_G=''; NC_RED=''; fi
NC_WARNINGS=()
NC_STEP=starting
nc_log() { printf '%s==>%s %s\n' "$NC_B" "$NC_R" "$*"; }
nc_ok() { printf '    %sok%s  %s\n' "$NC_G" "$NC_R" "$*"; }
nc_info() { printf '        %s\n' "$*"; }
nc_warn() { printf '    %swarn%s %s\n' "$NC_Y" "$NC_R" "$*" >&2; NC_WARNINGS+=("$*"); }
nc_die() { printf '%serror:%s %s\n' "$NC_RED" "$NC_R" "$*" >&2; exit 1; }
# nc_on_exit runs when the installer ends. A failure of an upgrade is also recorded for
# the dashboard (nc_report_update_failure), which shows it when the upgrade was started there.
nc_on_exit() {
	local rc=$? note
	if [ $rc -ne 0 ]; then
		printf "\n%sThe installer stopped during: %s (exit %s).%s\nNothing is half-applied that a re-run does not repair; fix the cause above and run it again.\n" "$NC_RED" "$NC_STEP" "$rc" "$NC_R" >&2
		note=$NC_FAIL_NOTE
		[ -n "$note" ] || note="The installer stopped during: $NC_STEP (exit $rc). The control plane was not changed, or it is still the previous version."
		nc_report_update_failure "$note" || true
	fi
	# The copies an upgrade keeps for a rollback hold secrets: never leave them behind.
	if [ "${UPGRADING:-0}" = 1 ] && [ -n "${ENV_FILE:-}" ]; then rm -f "$ENV_FILE.previous" "${COMPOSE_FILE:-}.previous"; fi
	[ -z "${NC_MANIFEST_DIR:-}" ] || rm -rf "$NC_MANIFEST_DIR"
	return 0
}
trap nc_on_exit EXIT

# ---- release verification ------------------------------------------------------
# Three things are checked against NC_RELEASE_PUBKEY before they are used:
#   - the release manifest (release-manifest.txt and its .minisig): the control-plane image the
#     installer pulls (always image@sha256:<digest>, never a tag), the SHA-256 of the agent
#     binaries and of every file under deploy/install;
#   - the libraries and assets (compose file, ...) when this script is neither the assembled
#     one nor run from a checkout. They are fetched from the release's tag (never a branch)
#     and compared with the manifest;
#   - (in install-agent.sh) the agent binary, by its own signature.
# The check uses minisign when it is installed and openssl otherwise (nc_verify_minisig), so
# an ordinary host needs nothing new. NC_RELEASE_BASE (the release's files) and NC_SOURCE_BASE
# (the repository's files at the tag) may point at a mirror, https only: the signatures still
# have to verify, so a mirror cannot change what is installed.
#
# An image chosen with --image or --image-archive is the operator's own and is not verified.

# >>> nc_verify_minisig
# nc_verify_minisig <file> <signature-file> <public-key>: returns 0 only when the minisign
# signature verifies, 1 when it does not, 2 when this host has no tool to check it. It uses
# minisign when that is installed, else openssl 3.x: a minisign signature is Ed25519 over the
# BLAKE2b-512 of the file ("ED", what `minisign -S` writes), and openssl does both, so the host
# needs no extra package. The same function is in deploy/agent/install.sh.tmpl and
# deploy/install/install.sh (a test keeps them identical).
nc_verify_minisig() {
	nc_v_file=$1 nc_v_sig=$2 nc_v_key=$3
	[ -r "$nc_v_file" ] && [ -r "$nc_v_sig" ] || return 1
	if [ -z "${NC_VERIFY_NO_MINISIGN:-}" ] && command -v minisign >/dev/null 2>&1; then
		minisign -Vq -P "$nc_v_key" -x "$nc_v_sig" -m "$nc_v_file" >/dev/null 2>&1 && return 0
		return 1
	fi
	command -v openssl >/dev/null 2>&1 || return 2
	nc_v_dir=$(mktemp -d) || return 1
	nc_v_rc=1
	{
		printf '%s' "$nc_v_key" | base64 -d >"$nc_v_dir/key" &&
			sed -n 2p "$nc_v_sig" | tr -d '\r' | base64 -d >"$nc_v_dir/sig" &&
			[ "$(wc -c <"$nc_v_dir/key" | tr -d ' ')" = 42 ] &&
			[ "$(wc -c <"$nc_v_dir/sig" | tr -d ' ')" = 74 ] &&
			[ "$(head -c 2 "$nc_v_dir/sig")" = ED ] &&
			[ "$(head -c 10 "$nc_v_dir/key" | tail -c 8 | od -An -tx1 | tr -d ' \n')" = "$(head -c 10 "$nc_v_dir/sig" | tail -c 8 | od -An -tx1 | tr -d ' \n')" ] &&
			{ printf '%s' MCowBQYDK2VwAyEA | base64 -d && tail -c 32 "$nc_v_dir/key"; } >"$nc_v_dir/pub.der" &&
			tail -c 64 "$nc_v_dir/sig" >"$nc_v_dir/sig.bin" &&
			openssl dgst -blake2b512 -binary "$nc_v_file" >"$nc_v_dir/hash" &&
			openssl pkeyutl -verify -pubin -inkey "$nc_v_dir/pub.der" -keyform DER -rawin -in "$nc_v_dir/hash" -sigfile "$nc_v_dir/sig.bin"
	} >/dev/null 2>&1 && nc_v_rc=0
	rm -rf "$nc_v_dir"
	return $nc_v_rc
}
# <<< nc_verify_minisig

NC_MANIFEST=''         # the verified release manifest (a file), once nc_manifest_load ran
NC_MANIFEST_DIR=''
NC_MANIFEST_VERSION='' # the release it describes

nc_sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
# nc_curl downloads over https only (also across redirects).
nc_curl() { curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 "$@"; }

# nc_release_base prints where the signed files of the release being installed are:
# nebulactrl.com/releases/<version>, or NC_RELEASE_BASE (a mirror of that directory).
nc_release_base() {
	local b=${NC_RELEASE_BASE:-} v=${VERSION:-$NC_DEFAULT_VERSION}
	if [ -z "$b" ]; then
		if [[ $v =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
			b=$NC_RELEASE_SITE/$v
		elif [ "$v" = latest ]; then
			b=$NC_RELEASE_SITE/latest
		else
			nc_die "\"$v\" is not a release version, so there is no signed release to install. Use a release version such as 0.1.2, or --image <image> to install an image of your own (it is not verified)."
		fi
	fi
	case $b in https://*) ;; *) nc_die "NC_RELEASE_BASE must be an https:// URL" ;; esac
	printf '%s' "${b%/}"
}
nc_source_base() {
	local b=${NC_SOURCE_BASE:-https://raw.githubusercontent.com/$NC_RELEASE_REPO}
	case $b in https://*) ;; *) nc_die "NC_SOURCE_BASE must be an https:// URL" ;; esac
	printf '%s' "${b%/}"
}

# nc_verify_or_die <file> <signature> <what>
nc_verify_or_die() {
	local rc=0
	nc_verify_minisig "$1" "$2" "$NC_RELEASE_PUBKEY" || rc=$?
	case $rc in
	0) return 0 ;;
	2) nc_die "cannot verify the $3: neither minisign nor openssl is installed" ;;
	*) nc_die "the $3 is not signed with the NebulaCtrl release key. Nothing was installed: the download may have been tampered with." ;;
	esac
}

# nc_manifest_load downloads and verifies the release manifest once (dies when it cannot).
# Its first line must name the format, and for a concrete version it must be that version.
nc_manifest_load() {
	[ -z "$NC_MANIFEST" ] || return 0
	local base dir
	base=$(nc_release_base)
	dir=$(mktemp -d)
	nc_curl -o "$dir/manifest" "$base/release-manifest.txt" || { rm -rf "$dir"; nc_die "could not download the release manifest $base/release-manifest.txt (does this release exist?)"; }
	nc_curl -o "$dir/manifest.minisig" "$base/release-manifest.txt.minisig" || { rm -rf "$dir"; nc_die "could not download $base/release-manifest.txt.minisig"; }
	NC_MANIFEST_DIR=$dir
	nc_verify_or_die "$dir/manifest" "$dir/manifest.minisig" "release manifest"
	[ "$(head -n 1 "$dir/manifest")" = "nebulactrl-release-manifest 1" ] || nc_die "the release manifest has an unknown format"
	NC_MANIFEST=$dir/manifest
	NC_MANIFEST_VERSION=$(nc_manifest_value version)
	[[ $NC_MANIFEST_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || nc_die "the release manifest has no valid version"
	if [[ ${VERSION:-} =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] && [ "$NC_MANIFEST_VERSION" != "$VERSION" ]; then
		nc_die "the release manifest is for $NC_MANIFEST_VERSION, not for $VERSION"
	fi
}
nc_manifest_value() { awk -v k="$1" '$1 == k { print $2; exit }' "$NC_MANIFEST"; }
nc_manifest_file_sum() { awk -v p="$1" '$1 == "file" && $3 == p { print $2; exit }' "$NC_MANIFEST"; }

# nc_release_file <path in the repository> prints the file as it is at the release's tag,
# after checking its SHA-256 against the signed manifest.
nc_release_file() {
	local rel=$1 want got tmp
	nc_manifest_load
	want=$(nc_manifest_file_sum "$rel")
	[ -n "$want" ] || nc_die "$rel is not in the signed manifest of release $NC_MANIFEST_VERSION"
	tmp=$(mktemp)
	nc_curl -o "$tmp" "$(nc_source_base)/v$NC_MANIFEST_VERSION/$rel" || { rm -f "$tmp"; nc_die "could not download $rel of release $NC_MANIFEST_VERSION"; }
	got=$(nc_sha256 "$tmp")
	if [ "$got" != "$want" ]; then
		rm -f "$tmp"
		nc_die "$rel does not match the signed manifest of release $NC_MANIFEST_VERSION (sha256 $got, signed $want)"
	fi
	cat "$tmp"
	rm -f "$tmp"
}

# ---- assets and libraries ------------------------------------------------------
# nc_asset <name> prints a file that ships next to this script. The control plane
# replaces this definition with one that has every asset inlined (at the
# assets marker below); from a checkout it reads the directory; otherwise it is
# fetched from the release's tag and verified (nc_release_file).
nc_asset() {
	if [ -r "$NC_SRC_DIR/$1" ]; then cat "$NC_SRC_DIR/$1"; else nc_release_file "deploy/install/$1"; fi
}
#@assets

# Library functions (Lynis hardening, owned by deploy/install/lib/hardening.sh).
# The control plane inlines them at the marker; a checkout or a raw download
# loads them at run time (nc_load_libs).
#@include lib/hardening.sh
nc_load_libs() {
	local lib tmp
	for lib in hardening; do
		if [ "$(type -t "nc_${lib}_step" 2>/dev/null)" = function ]; then continue; fi
		if [ -r "$NC_SRC_DIR/lib/$lib.sh" ]; then
			# shellcheck disable=SC1090
			. "$NC_SRC_DIR/lib/$lib.sh"
		else
			tmp=$(mktemp)
			# In a subshell: a file that does not verify fails this library only, and is never sourced.
			if (nc_release_file "deploy/install/lib/$lib.sh") >"$tmp" && [ -s "$tmp" ]; then
				# shellcheck disable=SC1090
				. "$tmp"
			else
				nc_warn "lib/$lib.sh was not loaded: it could not be downloaded, or it does not match the signed release manifest"
			fi
			rm -f "$tmp"
		fi
	done
}

# ---- options -------------------------------------------------------------------
INSTALL_DIR=/opt/nebulactrl
BACKUP_HOST_DIR=/var/backups/nebulactrl
INSTANCE_DIR=''
AGENT_STATE_DIR=/var/lib/nebulactrl-agent
DOMAIN=''
EMAIL=''
PUBLIC_IP=''
VERSION=''
IMAGE=''
IMAGE_ARCHIVE=''
UPDATE_TO=''
IP_PORT=''
NO_DOCKER_INSTALL=''
NO_DAEMON_DEFAULTS=''
NO_FIREWALL=''
NO_FAIL2BAN=''
NO_AUTO_UPDATES=''
NO_HARDENING=''
NO_LOCAL_SERVER=''
NO_BACKUP=''
RESTART_DOCKER=''
BIND=''
DASHBOARD_ALLOW=''
DASHBOARD_DNS01=''
DNS_TOKEN_FILE=''
DNS_TOKEN=''
PROXY_HTTP_PORT=''
PROXY_HTTPS_PORT=''
PROXY_ADMIN_PORT=''
LOOPBACK_PORT=''
PORT_FLAG=0
NEED_LOOPBACK=0
PROXY_MOVING=0
FORCE_UNSUPPORTED=0
CHECK_ONLY=0
FORCE_UPGRADE=''
UPGRADING=0   # an installed control plane is being upgraded (step_remember)
PREV_IMAGE='' # the image it ran, and that image's ID: put back when the new one is not healthy
PREV_IMAGE_ID=''
NC_FAIL_NOTE=''
EXIT_ROLLED_BACK=4
UNINSTALL=0
UNINSTALL_ARGS=()

usage() {
	cat <<'EOF'
Usage: install.sh [options]
       install.sh --uninstall [uninstall options]   (see uninstall.sh --help)

Address
  --domain <fqdn>        Serve the dashboard over HTTPS with a Let's Encrypt certificate.
                         The domain's A record must point at this machine first.
  --email <address>      Let's Encrypt contact. Required with --domain.
  --public-ip <ip>       This machine's public IPv4 address (detected when omitted).
  --port <n>             Dashboard port without a domain (default 8000). With a domain it is the
                         loopback port of the control plane that the host's agent uses (default 8080).
  --bind <ip>            Without a domain: publish the dashboard port on this local address only, for
                         example the Tailscale IP (default 0.0.0.0, every address). Remembered.

Image
  --version <tag>        NebulaCtrl image tag (default: the version of the control plane that served this
                         script, or "latest" when the script was fetched from nebulactrl.com). Remembered
                         only when given, so a plain re-run upgrades; --version latest drops the pin.
  --update-to <version>  Install this release for this run only (what Admin -> Updates runs): unlike
                         --version it is not remembered, so a later plain re-run still upgrades. An
                         installation pinned with --version moves its pin to <version>.
  --image <ref>          Full image reference (default: ghcr.io/nebulactrl/nebulactrl:<version>).
                         Remembered when given, like --version.
  --image-archive <file> Load the image from a `docker save` archive instead of pulling it.
                         A release (the default, or --version <release>) is verified: the image is pulled by the
                         digest in the release's signed manifest. An image of your own (--image,
                         --image-archive) is not verified.

Proxy ports (run NebulaCtrl's proxy next to another one, for example Coolify's on 80/443)
  --proxy-http-port <n>  Host port of the local server's HTTP listener (default 80).
  --proxy-https-port <n> Host port of the local server's HTTPS listener (default 443).
  --proxy-admin-port <n> Loopback port of the proxy's admin entrypoint (default 9000).
                         Re-run with 80 and 443 to move the proxy back once the other proxy is stopped.

Dashboard access (needs --domain; the first run seeds them, Admin -> Dashboard access edits them later)
  --dashboard-allow <cidr,...>  Only these addresses may open the dashboard, for example a tailnet:
                         100.64.0.0/10,fd7a:115c:a1e0::/48 ("off" removes the list).
  --dashboard-dns01 cloudflare  Issue the dashboard certificate with DNS-01 instead of HTTP-01, so it works
                         when the name resolves to a private address or :80 is held by another proxy.
  --dns-token-file <path>  File holding the Cloudflare API token (Zone:Read and DNS:Edit). It is read from
                         the file, never from the command line, and never printed.

Optional steps (all on by default; each flag turns one off)
  --no-docker-install    Fail instead of installing Docker Engine.
  --no-daemon-defaults   Leave /etc/docker/daemon.json alone.
  --no-local-server      Do not install an agent on this machine (no apps here, and with
                         --domain you must provide your own HTTPS proxy for 127.0.0.1:8080).
  --no-backup            Do not schedule the daily control-plane backup.
  --no-firewall          Do not touch ufw / firewalld.
  --no-fail2ban          Do not install fail2ban.
  --no-auto-updates      Do not enable automatic security updates.
  --no-hardening         Do not run Lynis or apply hardening fixes.

Other
  --restart-docker       Restart Docker to apply daemon.json even when containers run
                         without live-restore (they restart). Without it the installer only
                         restarts Docker when that is safe, and tells you otherwise.
  --dir <path>           Installation directory (default /opt/nebulactrl).
  --backup-dir <path>    Local backup directory (default /var/backups/nebulactrl).
  --instance-dir <path>  Where the built-in S3, registry and log store keep their data (default:
                         Docker volumes on this machine's Docker disk). Every team gets 50 GiB of
                         built-in backup storage by default, so pick a large disk here.
  --force-unsupported    Continue on a distribution or version that is not supported.
  --force-upgrade        Upgrade although the control-plane backup taken first failed. Without
                         it a failed backup stops the upgrade before anything is changed.
  --check                Run the preflight checks and print the plan; change nothing.
  -h, --help             This text.

Options are remembered in <dir>/install.conf, so a plain re-run keeps them. The version
is remembered only when pinned with --version or --image: a plain re-run upgrades.
EOF
}

NC_CONF_KEYS=(DOMAIN EMAIL PUBLIC_IP PIN_VERSION PIN_IMAGE IP_PORT BIND LOOPBACK_PORT PROXY_HTTP_PORT PROXY_HTTPS_PORT PROXY_ADMIN_PORT DASHBOARD_ALLOW DASHBOARD_DNS01 NO_DOCKER_INSTALL NO_DAEMON_DEFAULTS NO_FIREWALL NO_FAIL2BAN NO_AUTO_UPDATES NO_HARDENING NO_LOCAL_SERVER NO_BACKUP BACKUP_HOST_DIR INSTANCE_DIR)

# Persisted options come first; flags then override them.
if [ -r "${NEBULACTRL_INSTALL_DIR:-$INSTALL_DIR}/install.conf" ]; then
	INSTALL_DIR=${NEBULACTRL_INSTALL_DIR:-$INSTALL_DIR}
	# shellcheck disable=SC1090
	. "$INSTALL_DIR/install.conf"
	# Files written by 0.1.0 saved the resolved VERSION and IMAGE (and on some
	# systems the OS version in VERSION): they never pinned anything, so a re-run
	# installs the default release instead. Pins live in PIN_VERSION / PIN_IMAGE.
	unset VERSION IMAGE
fi

need_val() { [ $# -ge 2 ] && [ -n "$2" ] || nc_die "$1 needs a value"; }
while [ $# -gt 0 ]; do
	case "$1" in
	--domain) need_val "$@"; DOMAIN=$2; shift 2 ;;
	--domain=*) DOMAIN=${1#*=}; shift ;;
	--email) need_val "$@"; EMAIL=$2; shift 2 ;;
	--email=*) EMAIL=${1#*=}; shift ;;
	--public-ip) need_val "$@"; PUBLIC_IP=$2; shift 2 ;;
	--public-ip=*) PUBLIC_IP=${1#*=}; shift ;;
	--port) need_val "$@"; IP_PORT=$2; PORT_FLAG=1; shift 2 ;;
	--port=*) IP_PORT=${1#*=}; PORT_FLAG=1; shift ;;
	--bind) need_val "$@"; BIND=$2; shift 2 ;;
	--bind=*) BIND=${1#*=}; shift ;;
	--proxy-http-port) need_val "$@"; PROXY_HTTP_PORT=$2; shift 2 ;;
	--proxy-http-port=*) PROXY_HTTP_PORT=${1#*=}; shift ;;
	--proxy-https-port) need_val "$@"; PROXY_HTTPS_PORT=$2; shift 2 ;;
	--proxy-https-port=*) PROXY_HTTPS_PORT=${1#*=}; shift ;;
	--proxy-admin-port) need_val "$@"; PROXY_ADMIN_PORT=$2; shift 2 ;;
	--proxy-admin-port=*) PROXY_ADMIN_PORT=${1#*=}; shift ;;
	--dashboard-allow) need_val "$@"; DASHBOARD_ALLOW=$2; shift 2 ;;
	--dashboard-allow=*) DASHBOARD_ALLOW=${1#*=}; shift ;;
	--dashboard-dns01) need_val "$@"; DASHBOARD_DNS01=$2; shift 2 ;;
	--dashboard-dns01=*) DASHBOARD_DNS01=${1#*=}; shift ;;
	--dns-token-file) need_val "$@"; DNS_TOKEN_FILE=$2; shift 2 ;;
	--dns-token-file=*) DNS_TOKEN_FILE=${1#*=}; shift ;;
	--version) need_val "$@"; VERSION=$2; PIN_VERSION=$2; shift 2 ;;
	--update-to) need_val "$@"; UPDATE_TO=$2; shift 2 ;;
	--update-to=*) UPDATE_TO=${1#*=}; shift ;;
	--version=*) VERSION=${1#*=}; PIN_VERSION=$VERSION; shift ;;
	--image) need_val "$@"; IMAGE=$2; PIN_IMAGE=$2; shift 2 ;;
	--image=*) IMAGE=${1#*=}; PIN_IMAGE=$IMAGE; shift ;;
	--image-archive) need_val "$@"; IMAGE_ARCHIVE=$2; shift 2 ;;
	--image-archive=*) IMAGE_ARCHIVE=${1#*=}; shift ;;
	--dir) need_val "$@"; INSTALL_DIR=$2; shift 2 ;;
	--dir=*) INSTALL_DIR=${1#*=}; shift ;;
	--backup-dir) need_val "$@"; BACKUP_HOST_DIR=$2; shift 2 ;;
	--backup-dir=*) BACKUP_HOST_DIR=${1#*=}; shift ;;
	--instance-dir) need_val "$@"; INSTANCE_DIR=$2; shift 2 ;;
	--instance-dir=*) INSTANCE_DIR=${1#*=}; shift ;;
	--no-docker-install) NO_DOCKER_INSTALL=1; shift ;;
	--no-daemon-defaults) NO_DAEMON_DEFAULTS=1; shift ;;
	--no-local-server) NO_LOCAL_SERVER=1; shift ;;
	--no-backup) NO_BACKUP=1; shift ;;
	--no-firewall) NO_FIREWALL=1; shift ;;
	--no-fail2ban) NO_FAIL2BAN=1; shift ;;
	--no-auto-updates) NO_AUTO_UPDATES=1; shift ;;
	--no-hardening) NO_HARDENING=1; shift ;;
	--restart-docker) RESTART_DOCKER=1; shift ;;
	--force-unsupported) FORCE_UNSUPPORTED=1; shift ;;
	--force-upgrade) FORCE_UPGRADE=1; shift ;;
	--check) CHECK_ONLY=1; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*)
		if [ "$UNINSTALL" = 1 ]; then UNINSTALL_ARGS+=("$1"); shift; continue; fi
		usage >&2; nc_die "unknown option: $1" ;;
	esac
done
# Everything after --uninstall belongs to the uninstaller.
export NC_NO_HARDENING=${NO_HARDENING:-0}

# --update-to <version>: the dashboard's update. It installs that release without turning
# it into a pin; an installation that was pinned moves its pin along (it asked for a fixed version).
if [ -n "$UPDATE_TO" ]; then
	[[ $UPDATE_TO =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || nc_die "--update-to needs a release version such as 0.1.2, not \"$UPDATE_TO\""
	[ -z "${PIN_IMAGE:-}" ] || nc_die "this installation runs a pinned image (${PIN_IMAGE}); an update to a release cannot change it. Re-run the installer with --image <the new image>"
	VERSION=$UPDATE_TO
	[ -z "${PIN_VERSION:-}" ] || PIN_VERSION=$UPDATE_TO
fi

# The release build (Dockerfile) rewrites the next line to the control plane's own
# version, so an installer served by a control plane installs that same version.
# Fetched from nebulactrl.com it stays "latest" (the newest release).
NC_DEFAULT_VERSION=latest
[ "${VERSION:-}" = latest ] && PIN_VERSION=''
VERSION=${VERSION:-${PIN_VERSION:-$NC_DEFAULT_VERSION}}
# An image given with --image (now or remembered) is the operator's own: it is used as it is and
# is not verified. Otherwise step_release pins the release's image by digest.
NC_IMAGE_CUSTOM=0
[ -z "${IMAGE:-}${PIN_IMAGE:-}" ] || NC_IMAGE_CUSTOM=1
NC_RELEASE_VERIFIED=0 # step_release verified the release this run installs
IMAGE=${IMAGE:-${PIN_IMAGE:-ghcr.io/nebulactrl/nebulactrl:$VERSION}}
IP_PORT=${IP_PORT:-8000}
ENV_FILE=$INSTALL_DIR/.env
COMPOSE_FILE=$INSTALL_DIR/docker-compose.yml
STATE_DIR=$INSTALL_DIR/state
AGENT_BIN=/usr/local/bin/nebulactrl-agent
NC_AGENT_BIN=$AGENT_BIN
export NC_AGENT_BIN
CP_PORT=''
CP_BIND=''
CP_URL_LOCAL=''
PUBLIC_URL=''
MODE=ip

# ---- helpers -------------------------------------------------------------------
step_on() { [ -z "${1:-}" ] || [ "$1" = 0 ]; } # step_on "$NO_X": true unless --no-x was given ("" or 0 = enabled)
have() { command -v "$1" >/dev/null 2>&1; }
rand_hex() { head -c "$1" /dev/urandom | od -An -vtx1 | tr -d ' \n'; }
rand_b64_32() { head -c 32 /dev/urandom | base64 | tr -d '\n'; }
rand_b32() { head -c "$1" /dev/urandom | base32 | tr -d '=\n' | tr 'A-Z' 'a-z'; }
env_get() { [ -r "$ENV_FILE" ] && grep -m1 "^$1=" "$ENV_FILE" | cut -d= -f2- || true; }
state_set() { mkdir -p "$STATE_DIR"; touch "$STATE_DIR/$1"; }
state_has() { [ -e "$STATE_DIR/$1" ]; }
dc() {
	local files=(-f "$COMPOSE_FILE")
	# A bind address that is neither 0.0.0.0 nor 127.0.0.1 also needs the loopback publish the host's agent uses.
	[ -e "$INSTALL_DIR/docker-compose.loopback.yml" ] && files+=(-f "$INSTALL_DIR/docker-compose.loopback.yml")
	docker compose -p nebulactrl "${files[@]}" --env-file "$ENV_FILE" "$@"
}

# nc_report_update_failure <note>: when this upgrade was started from the dashboard
# (Admin -> Updates) the control plane is waiting for its outcome in instance_update.
# Record a failure there. Best effort: the database may be down, or too old to have the table.
nc_report_update_failure() {
	[ "${UPGRADING:-0}" = 1 ] || return 0
	have docker || return 0
	printf '%s\n' "UPDATE instance_update SET state = 'failed', error = :'note', finished_at = now(), updated_at = now() WHERE state = 'running';" |
		PGPASSWORD=$(env_get POSTGRES_PASSWORD) dc exec -T -e PGPASSWORD postgres psql -U nebulactrl -d nebulactrl -v ON_ERROR_STOP=1 -v "note=$1" >/dev/null 2>&1 || true
}

# wait_ready: the control plane answers /readyz (up to two minutes).
wait_ready() {
	local i
	for i in $(seq 1 60); do
		curl -fsS --max-time 3 "$CP_URL_LOCAL/readyz" >/dev/null 2>&1 && return 0
		sleep 2
	done
	return 1
}

pkg_refresh() {
	[ "${NC_PKG_REFRESHED:-0}" = 1 ] && return 0
	if [ "$NC_PKG" = apt ]; then DEBIAN_FRONTEND=noninteractive apt-get update -qq; fi
	NC_PKG_REFRESHED=1
}
pkg_install() {
	pkg_refresh
	if [ "$NC_PKG" = apt ]; then
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "$@" >/dev/null
	else
		dnf install -y -q "$@" >/dev/null
	fi
}

port_busy() { ss -H -ltn "sport = :$1" 2>/dev/null | grep -q .; }
port_busy_udp() { ss -H -lun "sport = :$1" 2>/dev/null | grep -q .; }

# ---- steps ---------------------------------------------------------------------
step_os() {
	NC_STEP="checking the system"
	nc_log "Checking the system"
	[ "$(id -u)" = 0 ] || nc_die "must run as root: curl -fsSL <url>/install.sh | sudo bash"
	[ "$(uname -s)" = Linux ] || nc_die "only Linux hosts are supported"
	[ -r /etc/os-release ] || nc_die "cannot read /etc/os-release: unsupported system"
	# Read the fields in a subshell: /etc/os-release defines VERSION, NAME and ID,
	# and sourcing it here overwrote the installer's own VERSION (the release to
	# install) with the OS version ("26.04.1 LTS (Resolute Raccoon)"), which then
	# reached install.conf and the pre-upgrade backup's command line.
	local os_like os_pretty
	{ read -r OS_ID; read -r OS_VER; read -r OS_CODENAME; read -r os_like; read -r os_pretty; } < <(
		# shellcheck disable=SC1091
		. /etc/os-release
		printf '%s\n' "${ID:-unknown}" "${VERSION_ID:-}" "${VERSION_CODENAME:-}" "${ID_LIKE:-}" "${PRETTY_NAME:-}"
	)
	local ok=0 major=${OS_VER%%.*}
	case "$OS_ID" in
	debian) NC_PKG=apt; NC_DISTRO_FAMILY=debian; case "$major" in 12 | 13) ok=1 ;; esac ;;
	ubuntu) NC_PKG=apt; NC_DISTRO_FAMILY=debian; case "$OS_VER" in 22.04 | 24.04 | 26.04) ok=1 ;; esac ;;
	rhel | rocky | almalinux | centos) NC_PKG=dnf; NC_DISTRO_FAMILY=rhel; [ "$major" = 9 ] && ok=1 ;;
	*)
		case " $os_like " in
		*" debian "*) NC_PKG=apt; NC_DISTRO_FAMILY=debian ;;
		*" rhel "* | *" fedora "*) NC_PKG=dnf; NC_DISTRO_FAMILY=rhel ;;
		*) NC_PKG='' ;;
		esac ;;
	esac
	NC_DISTRO_ID=$OS_ID
	export NC_DISTRO_ID NC_PKG NC_DISTRO_FAMILY
	if [ "$ok" = 0 ]; then
		[ "$FORCE_UNSUPPORTED" = 1 ] && [ -n "$NC_PKG" ] || nc_die "unsupported system: ${os_pretty:-$OS_ID $OS_VER}. Supported: Debian 12/13, Ubuntu 22.04, 24.04 and 26.04, RHEL/Rocky/AlmaLinux 9 (--force-unsupported to try anyway on a Debian or RHEL family system)."
		nc_warn "unsupported system ${os_pretty:-$OS_ID $OS_VER}: continuing because of --force-unsupported"
	fi
	case "$(uname -m)" in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) nc_die "unsupported CPU architecture: $(uname -m) (amd64 and arm64 are supported)" ;;
	esac
	[ -d /run/systemd/system ] || nc_die "systemd is required (this machine does not run it as init)"
	nc_ok "${os_pretty:-$OS_ID $OS_VER}, $ARCH, systemd"

	MEM_KB=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
	MEM_MB=$((MEM_KB / 1024))
	if [ "$MEM_MB" -lt 900 ]; then nc_warn "only ${MEM_MB} MB RAM: NebulaCtrl needs about 1 GB for the control plane alone"; fi
	local free_kb
	free_kb=$(df -Pk "${INSTALL_DIR%/*}" 2>/dev/null | awk 'NR==2 {print $4}' || echo 0)
	[ "${free_kb:-0}" -ge 3000000 ] || nc_warn "less than 3 GB free disk space (images and build cache need more)"
	check_instance_disk
	nc_ok "${MEM_MB} MB RAM"
}

# check_instance_disk warns when the disk that will hold the built-in S3, registry and log
# store has less than 60 GB free: every team gets a 50 GiB backup quota by default (not
# reserved, but backups stop when the disk is full) and the images and logs need room too.
# The disk is --instance-dir, else Docker's data directory, else the installation's.
check_instance_disk() {
	local where free_kb
	where=${INSTANCE_DIR:-}
	if [ -z "$where" ]; then
		if [ -d /var/lib/docker ]; then where=/var/lib/docker; else where=${INSTALL_DIR%/*}; fi
	fi
	# The directory may not exist yet: measure the nearest parent that does.
	while [ -n "$where" ] && [ "$where" != / ] && [ ! -d "$where" ]; do where=${where%/*}; done
	[ -n "$where" ] || where=/
	free_kb=$(df -Pk "$where" 2>/dev/null | awk 'NR==2 {print $4}' || echo 0)
	if [ "${free_kb:-0}" -lt 62914560 ]; then
		nc_warn "less than 60 GB free on the disk for the built-in services ($where, $((${free_kb:-0} / 1048576)) GB free): each team gets 50 GiB of built-in backup storage by default. Free space, lower the default in Admin -> Built-in services, or move it to a larger disk with --instance-dir <path>."
	fi
}

step_packages() {
	NC_STEP="installing base packages"
	nc_log "Base packages"
	local missing=()
	have curl || missing+=(curl)
	have ss || missing+=(iproute2)
	have base32 || have /usr/bin/base32 || missing+=(coreutils)
	have openssl || have minisign || missing+=(openssl) # to check the release signature (nc_verify_minisig)
	if [ "$NC_PKG" = apt ]; then
		[ -e /etc/ssl/certs/ca-certificates.crt ] || missing+=(ca-certificates)
		have gpg || missing+=(gnupg)
	else
		[ -e /etc/pki/tls/certs/ca-bundle.crt ] || missing+=(ca-certificates)
	fi
	if [ ${#missing[@]} -gt 0 ]; then
		pkg_install "${missing[@]}"
		nc_ok "installed: ${missing[*]}"
	else
		nc_ok "present"
	fi
}

docker_version_ok() {
	local v major
	v=$(docker version --format '{{.Server.Version}}' 2>/dev/null) || return 1
	major=${v%%.*}
	case "$major" in '' | *[!0-9]*) return 1 ;; esac
	[ "$major" -ge 24 ]
}

install_docker_apt() {
	local distro=$OS_ID codename=$OS_CODENAME
	[ "$distro" = ubuntu ] || distro=debian
	[ -n "$codename" ] || codename=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-}")
	[ -n "$codename" ] || nc_die "cannot determine the distribution codename for Docker's repository"
	# Distribution Docker packages conflict with docker-ce.
	local old
	for old in docker.io docker-doc docker-compose podman-docker containerd runc; do
		if dpkg -s "$old" >/dev/null 2>&1; then
			nc_die "the distribution package '$old' is installed and conflicts with Docker Engine; remove it (apt-get remove $old) or install Docker yourself and re-run."
		fi
	done
	install -m 0755 -d /etc/apt/keyrings
	curl -fsSL --retry 3 "https://download.docker.com/linux/$distro/gpg" -o /etc/apt/keyrings/docker.asc
	chmod a+r /etc/apt/keyrings/docker.asc
	printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
		"$(dpkg --print-architecture)" "$distro" "$codename" >/etc/apt/sources.list.d/docker.list
	NC_PKG_REFRESHED=0
	pkg_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
}

install_docker_dnf() {
	local repo=centos
	[ "$OS_ID" = rhel ] && repo=rhel
	local old
	for old in podman-docker docker docker-client moby-engine; do
		if rpm -q "$old" >/dev/null 2>&1; then
			nc_die "the package '$old' is installed and conflicts with Docker Engine; remove it (dnf remove $old) or install Docker yourself and re-run."
		fi
	done
	pkg_install dnf-plugins-core
	dnf config-manager --add-repo "https://download.docker.com/linux/$repo/docker-ce.repo" >/dev/null 2>&1 \
		|| curl -fsSL "https://download.docker.com/linux/$repo/docker-ce.repo" -o /etc/yum.repos.d/docker-ce.repo
	pkg_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
}

step_docker() {
	NC_STEP="installing Docker"
	nc_log "Docker Engine"
	if ! have docker; then
		step_on "$NO_DOCKER_INSTALL" || nc_die "Docker is not installed and --no-docker-install was given"
		nc_info "installing Docker Engine from Docker's official repository ..."
		if [ "$NC_PKG" = apt ]; then install_docker_apt; else install_docker_dnf; fi
		DOCKER_FRESH=1
		nc_ok "Docker Engine installed"
	else
		DOCKER_FRESH=0
	fi
	systemctl enable --now docker >/dev/null 2>&1 || true
	local i
	for i in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
	docker info >/dev/null 2>&1 || nc_die "the Docker daemon is not reachable; check: systemctl status docker"
	docker_version_ok || nc_die "Docker Engine 24 or newer is required (found $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown))"
	if ! docker compose version >/dev/null 2>&1; then
		nc_info "installing the Docker Compose plugin ..."
		pkg_install docker-compose-plugin || nc_die "the Docker Compose plugin (docker compose) is missing and could not be installed; install docker-compose-plugin from Docker's repository"
		docker compose version >/dev/null 2>&1 || nc_die "docker compose is still not available"
	fi
	nc_ok "Docker $(docker version --format '{{.Server.Version}}'), Compose $(docker compose version --short)"
}

# Safe daemon defaults. Existing settings always win: a key that is already set is
# kept (and reported when it differs), only missing keys are added.
step_daemon() {
	NC_STEP="applying Docker daemon defaults"
	step_on "$NO_DAEMON_DEFAULTS" || { nc_info "daemon defaults skipped (--no-daemon-defaults)"; return 0; }
	nc_log "Docker daemon defaults"
	local file=/etc/docker/daemon.json
	local ours='{"log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"},"live-restore":true,"userland-proxy":false,"default-address-pools":[{"base":"10.212.0.0/16","size":24}]}'
	mkdir -p /etc/docker
	local before=''
	[ -s "$file" ] && before=$(cat "$file")
	local result
	if [ -z "$before" ]; then
		result=$(printf '%s\n' "$ours")
		if have jq; then result=$(printf '%s' "$ours" | jq .); fi
	else
		have jq || pkg_install jq
		jq -e . "$file" >/dev/null 2>&1 || nc_die "$file is not valid JSON; fix it or pass --no-daemon-defaults"
		# Keep existing keys, add missing ones.
		result=$(jq --argjson ours "$ours" '. as $e | $e + ($ours | with_entries(select($e[.key] == null)))' "$file")
		local k
		for k in log-driver live-restore userland-proxy default-address-pools; do
			if [ "$(jq -c --arg k "$k" '.[$k]' <<<"$before")" != "null" ] && [ "$(jq -c --arg k "$k" '.[$k]' <<<"$before")" != "$(jq -c --arg k "$k" '.[$k]' <<<"$ours")" ]; then
				nc_info "kept your existing \"$k\" setting ($(jq -c --arg k "$k" '.[$k]' <<<"$before"))"
			fi
		done
	fi
	if [ "$(printf '%s' "$result" | jq -S -c . 2>/dev/null || echo x)" = "$(printf '%s' "$before" | jq -S -c . 2>/dev/null || echo y)" ]; then
		nc_ok "already in place"
		return 0
	fi
	if [ -n "$before" ] && [ ! -e "$file.nebulactrl-bak" ]; then cp -p "$file" "$file.nebulactrl-bak"; fi
	printf '%s\n' "$result" >"$file.tmp" && mv "$file.tmp" "$file"
	chmod 0644 "$file"
	state_set daemon-json-modified
	nc_ok "wrote $file"
	# default-address-pools and userland-proxy need a daemon restart.
	local running live
	running=$(docker ps -q 2>/dev/null | wc -l)
	live=$(docker info --format '{{.LiveRestoreEnabled}}' 2>/dev/null || echo false)
	if [ "${DOCKER_FRESH:-0}" = 1 ] || [ "$running" = 0 ] || [ "$live" = true ] || [ "$RESTART_DOCKER" = 1 ]; then
		systemctl restart docker
		local i
		for i in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
		docker info >/dev/null 2>&1 || nc_die "Docker did not come back after the restart; see: journalctl -u docker"
		nc_ok "Docker restarted with the new defaults"
	else
		nc_warn "Docker was NOT restarted: $running container(s) run here and live-restore is off, so a restart would stop them. The new defaults apply after you run 'systemctl restart docker' in a maintenance window (or re-run with --restart-docker)."
	fi
}

step_network() {
	NC_STEP="creating the edge network"
	nc_log "Edge network"
	if docker network inspect nebulactrl-edge >/dev/null 2>&1; then
		nc_ok "nebulactrl-edge exists"
	else
		docker network create --label nebulactrl.managed=true nebulactrl-edge >/dev/null
		nc_ok "created nebulactrl-edge"
	fi
}

detect_ips() {
	LAN_IP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") {print $(i + 1); exit}}' || true)
	if [ -z "$PUBLIC_IP" ]; then
		local ip u
		for u in https://api.ipify.org https://ipv4.icanhazip.com https://ifconfig.me/ip; do
			ip=$(curl -4 -fsS --max-time 5 "$u" 2>/dev/null | tr -d ' \r\n' || true)
			case "$ip" in
			*[!0-9.]* | '') ip='' ;;
			esac
			if [ -n "$ip" ]; then PUBLIC_IP=$ip; break; fi
		done
	fi
	[ -n "$PUBLIC_IP" ] || PUBLIC_IP=$LAN_IP
	[ -n "$PUBLIC_IP" ] || nc_die "cannot determine this machine's IP address; pass --public-ip <ip>"
	case "$PUBLIC_IP" in
	*[!0-9.]*) nc_die "--public-ip must be an IPv4 address" ;;
	esac
}

is_private_ip() {
	case "$1" in
	10.* | 192.168.* | 127.* | 169.254.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[0-1].* | 100.6[4-9].* | 100.[7-9][0-9].* | 100.1[01][0-9].* | 100.12[0-7].*) return 0 ;;
	esac
	return 1
}

valid_port() {
	case "$1" in *[!0-9]* | '') return 1 ;; esac
	[ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

valid_ipv4() {
	printf '%s' "$1" | grep -Eq '^[0-9]{1,3}(\.[0-9]{1,3}){3}$' || return 1
	local o
	for o in ${1//./ }; do [ "$o" -le 255 ] || return 1; done
}

# An IPv4 or IPv6 address or CIDR (lower case, no spaces).
valid_cidr() {
	local ip=${1%%/*} len=''
	case "$1" in */*) len=${1#*/} ;; esac
	if valid_ipv4 "$ip"; then
		[ -z "$len" ] && return 0
		printf '%s' "$len" | grep -Eq '^[0-9]{1,2}$' && [ "$len" -le 32 ]
	else
		printf '%s' "$ip" | grep -Eq '^([0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}$' || return 1
		[ -z "$len" ] && return 0
		printf '%s' "$len" | grep -Eq '^[0-9]{1,3}$' && [ "$len" -le 128 ]
	fi
}

lower() { printf '%s' "$1" | tr 'A-Z' 'a-z'; }

# normalize_allow <list>: validate a comma-separated list of addresses and CIDRs and print it
# without spaces and in lower case.
normalize_allow() {
	local raw item out='' n=0
	raw=$(lower "$1")
	raw=${raw//[[:space:]]/}
	while IFS= read -r item; do
		[ -n "$item" ] || continue
		valid_cidr "$item" || nc_die "--dashboard-allow: \"$item\" is not an IP address or CIDR (for example 100.64.0.0/10 or fd7a:115c:a1e0::/48)"
		n=$((n + 1))
		out=${out:+$out,}$item
	done <<<"${raw//,/$'\n'}"
	[ "$n" -le 200 ] || nc_die "--dashboard-allow takes at most 200 entries"
	printf '%s' "$out"
}

# traefik_host_ports prints the host ports the local proxy container publishes.
traefik_host_ports() {
	docker port nebulactrl-traefik 2>/dev/null | awk '{n = split($NF, a, ":"); print a[n]}' | sort -un | tr '\n' ' ' || true
}

# port_is_proxy <port>: the local server's proxy uses it (never true without a local server).
port_is_proxy() {
	step_on "$NO_LOCAL_SERVER" || return 1
	[ "$1" = "$PROXY_HTTP_PORT" ] || [ "$1" = "$PROXY_HTTPS_PORT" ] || [ "$1" = "$PROXY_ADMIN_PORT" ]
}

# Validates and normalizes the options that need more than a pattern. Nothing prints a token.
check_options() {
	PROXY_HTTP_PORT=${PROXY_HTTP_PORT:-80}
	PROXY_HTTPS_PORT=${PROXY_HTTPS_PORT:-443}
	PROXY_ADMIN_PORT=${PROXY_ADMIN_PORT:-9000}
	valid_port "$PROXY_HTTP_PORT" || nc_die "--proxy-http-port must be a port number between 1 and 65535"
	valid_port "$PROXY_HTTPS_PORT" || nc_die "--proxy-https-port must be a port number between 1 and 65535"
	valid_port "$PROXY_ADMIN_PORT" || nc_die "--proxy-admin-port must be a port number between 1 and 65535"
	[ "$PROXY_HTTP_PORT" != "$PROXY_HTTPS_PORT" ] || nc_die "--proxy-http-port and --proxy-https-port must differ"
	[ "$PROXY_ADMIN_PORT" != "$PROXY_HTTP_PORT" ] && [ "$PROXY_ADMIN_PORT" != "$PROXY_HTTPS_PORT" ] || nc_die "--proxy-admin-port must differ from the HTTP and HTTPS ports"
	if ! step_on "$NO_LOCAL_SERVER" && { [ "$PROXY_HTTP_PORT" != 80 ] || [ "$PROXY_HTTPS_PORT" != 443 ] || [ "$PROXY_ADMIN_PORT" != 9000 ]; }; then
		nc_warn "the proxy port options are ignored with --no-local-server (no proxy is installed on this machine)"
	fi

	case "$(lower "$BIND")" in
	any | '') BIND='' ;;
	*) valid_ipv4 "$BIND" || nc_die "--bind must be an IPv4 address of this machine (for example its Tailscale address), or 0.0.0.0" ;;
	esac

	case "$(lower "$DASHBOARD_ALLOW")" in
	off | none | any) DASHBOARD_ALLOW='' ;;
	*) DASHBOARD_ALLOW=$(normalize_allow "$DASHBOARD_ALLOW") ;;
	esac

	case "$(lower "$DASHBOARD_DNS01")" in
	'') ;;
	off | none) DASHBOARD_DNS01='' ;;
	cloudflare) DASHBOARD_DNS01=cloudflare ;;
	*) nc_die "--dashboard-dns01 supports cloudflare here (for another DNS provider add its credential under Servers, Proxy after the install and choose it in Admin, Dashboard access)" ;;
	esac

	if [ -n "$DNS_TOKEN_FILE" ]; then
		[ -n "$DASHBOARD_DNS01" ] || nc_die "--dns-token-file goes with --dashboard-dns01"
		[ -f "$DNS_TOKEN_FILE" ] && [ -r "$DNS_TOKEN_FILE" ] || nc_die "cannot read --dns-token-file $DNS_TOKEN_FILE"
		local fmode
		fmode=$(stat -c %a "$DNS_TOKEN_FILE" 2>/dev/null || true)
		case "$fmode" in
		'' | *00) ;;
		*) nc_warn "$DNS_TOKEN_FILE is readable by other users: delete it once the installer is done (the token is kept in $ENV_FILE, mode 0600, and sealed in the database)" ;;
		esac
		DNS_TOKEN=$(head -c 4096 "$DNS_TOKEN_FILE")
		DNS_TOKEN=${DNS_TOKEN%$'\r'}
		case "$DNS_TOKEN" in *$'\n'*) nc_die "--dns-token-file must hold the token on a single line" ;; esac
		printf '%s' "$DNS_TOKEN" | grep -Eq '^[A-Za-z0-9_.~-]{16,255}$' || nc_die "--dns-token-file does not hold an API token (letters, digits and _ . ~ - only, 16 to 255 characters, nothing else in the file)"
	fi
	if [ -n "$DASHBOARD_DNS01" ] && [ -z "$DNS_TOKEN" ] && [ -z "$(env_get NEBULACTRL_DASHBOARD_DNS_TOKEN)" ]; then
		nc_die "--dashboard-dns01 $DASHBOARD_DNS01 needs --dns-token-file <file with the API token> (Cloudflare: Zone:Read and DNS:Edit on the zone of the dashboard's domain)"
	fi
}

step_plan() {
	NC_STEP="planning"
	nc_log "Plan"
	check_options
	detect_ips
	local url_host=''
	if [ -n "$DOMAIN" ]; then
		DOMAIN=$(printf '%s' "$DOMAIN" | tr 'A-Z' 'a-z')
		printf '%s' "$DOMAIN" | grep -Eq '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$' || nc_die "--domain must be a DNS name such as nebula.example.com"
		[ -n "$EMAIL" ] || nc_die "--email is required with --domain (Let's Encrypt contact)"
		printf '%s' "$EMAIL" | grep -Eq '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$' || nc_die "--email is not an email address"
		MODE=domain
		PUBLIC_URL=https://$DOMAIN
		step_on "$NO_LOCAL_SERVER" && [ "$PROXY_HTTPS_PORT" != 443 ] && PUBLIC_URL=$PUBLIC_URL:$PROXY_HTTPS_PORT
		CP_BIND=127.0.0.1
		# The loopback port the host's agent uses. 8080 unless --port says otherwise or the
		# proxy sits on it (the side-by-side layout 8080/8443).
		if [ "$PORT_FLAG" = 1 ]; then CP_PORT=$IP_PORT; else CP_PORT=${LOOPBACK_PORT:-8080}; fi
		valid_port "$CP_PORT" || nc_die "--port must be a port number between 1 and 65535"
		if port_is_proxy "$CP_PORT"; then
			[ "$PORT_FLAG" = 0 ] || nc_die "--port $CP_PORT is also a port of the proxy ($PROXY_HTTP_PORT, $PROXY_HTTPS_PORT, admin $PROXY_ADMIN_PORT); choose another"
			while port_is_proxy "$CP_PORT"; do CP_PORT=$((CP_PORT + 1)); done
			nc_info "the control plane listens on 127.0.0.1:$CP_PORT (the proxy holds the ports $PROXY_HTTP_PORT, $PROXY_HTTPS_PORT and $PROXY_ADMIN_PORT)"
		fi
		LOOPBACK_PORT=$CP_PORT
		CP_URL_LOCAL=http://127.0.0.1:$CP_PORT
		[ -z "$BIND" ] || nc_info "--bind $BIND is ignored with a domain: the control plane listens on 127.0.0.1 behind the host's proxy"
		local resolved
		resolved=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1 {print $1}' || true)
		if [ -z "$resolved" ]; then
			nc_warn "$DOMAIN does not resolve yet. Point its A record at $PUBLIC_IP (or the address clients reach it on); the dashboard is reachable on $CP_URL_LOCAL from this machine meanwhile."
		elif [ -n "$DASHBOARD_DNS01" ]; then
			nc_info "$DOMAIN resolves to $resolved; the DNS-01 certificate does not depend on it"
		elif [ "$resolved" != "$PUBLIC_IP" ]; then
			nc_warn "$DOMAIN resolves to $resolved, but this machine's address is $PUBLIC_IP. Let's Encrypt validates through DNS: fix the A record or the certificate will not be issued."
		fi
		if [ -z "$DASHBOARD_DNS01" ] && step_on "$NO_LOCAL_SERVER"; then
			if [ "$PROXY_HTTP_PORT" != 80 ]; then
				nc_warn "the proxy listens on :$PROXY_HTTP_PORT, not :80, so Let's Encrypt cannot validate $DOMAIN over HTTP-01 and the dashboard keeps a self-signed certificate. Add --dashboard-dns01 cloudflare --dns-token-file <file>, or move the proxy to 80/443."
			elif [ -n "$DASHBOARD_ALLOW" ]; then
				nc_warn "with --dashboard-allow the name usually resolves to a private address that Let's Encrypt cannot reach, so HTTP-01 fails. Add --dashboard-dns01 cloudflare --dns-token-file <file>."
			fi
		fi
	else
		MODE=ip
		valid_port "$IP_PORT" || nc_die "--port must be a port number between 1 and 65535"
		if [ -z "$BIND" ]; then
			# A bind address someone put into .env by hand is kept and remembered from now on.
			local env_bind
			env_bind=$(env_get NEBULACTRL_BIND)
			case "$env_bind" in
			'' | 0.0.0.0 | 127.0.0.1) ;;
			*) if valid_ipv4 "$env_bind"; then BIND=$env_bind; nc_info "keeping the bind address $BIND found in $ENV_FILE (from now on --bind remembers it)"; fi ;;
			esac
		fi
		CP_BIND=${BIND:-0.0.0.0}
		if [ -n "$BIND" ] && [ "$BIND" != 0.0.0.0 ] && [ "$BIND" != 127.0.0.1 ]; then
			if have ip && ! ip -4 -o addr show 2>/dev/null | awk '{sub(/\/.*/, "", $4); print $4}' | grep -qx "$BIND"; then
				nc_die "--bind $BIND is not an address of this machine (ip -4 addr lists them); Docker could not publish the port there"
			fi
			# The host's agent reaches the control plane on loopback (the only plain-HTTP URL it accepts).
			NEED_LOOPBACK=1
		fi
		url_host=$PUBLIC_IP
		[ "$CP_BIND" = 0.0.0.0 ] || url_host=$CP_BIND
		PUBLIC_URL=http://$url_host:$IP_PORT
		CP_PORT=$IP_PORT
		CP_URL_LOCAL=http://127.0.0.1:$IP_PORT
		port_is_proxy "$CP_PORT" && nc_die "--port $CP_PORT is also a port of the proxy ($PROXY_HTTP_PORT, $PROXY_HTTPS_PORT, admin $PROXY_ADMIN_PORT); choose another"
	fi
	if [ -n "$DASHBOARD_ALLOW$DASHBOARD_DNS01" ]; then
		[ "$MODE" = domain ] || nc_die "--dashboard-allow and --dashboard-dns01 shape the dashboard's HTTPS route and need --domain (without a domain limit access with --bind)"
		step_on "$NO_LOCAL_SERVER" || nc_die "--dashboard-allow and --dashboard-dns01 are applied by the host's proxy: drop --no-local-server"
	fi
	if [ "$MODE" = ip ] && [ -n "$url_host" ] && is_private_ip "$url_host"; then
		if [ "$NEED_LOOPBACK" = 1 ]; then
			nc_info "the dashboard is bound to $url_host: only addresses that reach it can open it, and servers you add must be able to connect to $PUBLIC_URL"
		else
			nc_warn "$url_host is a private address: the dashboard will only be reachable from your network, and servers outside it cannot connect (use --public-ip or a domain)."
		fi
	fi
	nc_ok "mode: $MODE, dashboard: $PUBLIC_URL"
	[ "$MODE" = ip ] && nc_info "no domain given: the dashboard is served over plain HTTP (see the notice at the end)"
	if step_on "$NO_LOCAL_SERVER" && { [ "$PROXY_HTTP_PORT" != 80 ] || [ "$PROXY_HTTPS_PORT" != 443 ]; }; then
		nc_info "the proxy of this machine uses ports $PROXY_HTTP_PORT (HTTP) and $PROXY_HTTPS_PORT (HTTPS), next to whatever holds 80 and 443; apps answer there until you move it (docs/installing/installer.md)"
	fi

	# Ports we need that something else may hold.
	local busy=0 p cur
	if step_on "$NO_LOCAL_SERVER"; then
		if docker inspect nebulactrl-traefik >/dev/null 2>&1; then
			# Re-run on a host that already has the proxy: only ports it does not publish yet matter.
			cur=" $(traefik_host_ports) "
			for p in "$PROXY_HTTP_PORT" "$PROXY_HTTPS_PORT"; do
				case "$cur" in
				*" $p "*) ;;
				*)
					PROXY_MOVING=1
					if port_busy "$p"; then nc_warn "port $p is in use on this machine: the proxy cannot move there until it is free"; busy=1; fi
					;;
				esac
			done
			if [ "$PROXY_MOVING" = 1 ]; then nc_info "the proxy container is recreated on the new ports (about a second of dropped connections); it publishes:$cur now"; fi
		else
			for p in "$PROXY_HTTP_PORT" "$PROXY_HTTPS_PORT"; do
				if port_busy "$p"; then nc_warn "port $p is in use on this machine: the proxy of the local server cannot start until it is free (use --proxy-http-port/--proxy-https-port for other ports, or --no-local-server to skip the local server)"; busy=1; fi
			done
			if port_busy "$PROXY_ADMIN_PORT"; then nc_warn "port $PROXY_ADMIN_PORT is in use: the proxy's admin entrypoint needs it on 127.0.0.1 (--proxy-admin-port picks another)"; fi
			if port_busy_udp "$PROXY_HTTPS_PORT"; then nc_info "udp $PROXY_HTTPS_PORT is in use (HTTP/3 will not be available)"; fi
		fi
	fi
	local cp_has=' '
	if docker inspect nebulactrl-nebulactrl-1 >/dev/null 2>&1; then
		cp_has=" $(docker port nebulactrl-nebulactrl-1 2>/dev/null | awk '{n = split($NF, a, ":"); print a[n]}' | sort -un | tr '\n' ' ' || true) "
	fi
	case "$cp_has" in
	*" $CP_PORT "*) ;;
	*) if port_busy "$CP_PORT"; then nc_die "port $CP_PORT is already in use on this machine; free it or choose another with --port"; fi ;;
	esac
	[ "$busy" = 0 ] || [ "$CHECK_ONLY" = 1 ] || nc_die "the proxy's ports are not free: stop what uses them (Coolify: docker stop coolify-proxy), or run NebulaCtrl's proxy next to it on other ports (--proxy-http-port, --proxy-https-port), or re-run with --no-local-server and put your own HTTPS proxy in front of $CP_URL_LOCAL"
	if [ "$CHECK_ONLY" = 1 ]; then
		nc_log "Check only: nothing was changed."
		exit 0
	fi
}

# step_remember: an installed control plane is being upgraded. Keep what it takes to put
# the previous version back when the new one is not healthy: the image by ID (a tag such as
# latest moves when the new image is pulled), the compose file and the settings.
step_remember() {
	[ "$CHECK_ONLY" = 0 ] || return 0
	[ -e "$ENV_FILE" ] || return 0 # a new installation: nothing to roll back to
	UPGRADING=1
	have docker || return 0
	PREV_IMAGE=$(env_get NEBULACTRL_IMAGE)
	local cid
	cid=$(dc ps -q nebulactrl 2>/dev/null | head -n 1 || true)
	[ -z "$cid" ] || PREV_IMAGE_ID=$(docker inspect -f '{{.Image}}' "$cid" 2>/dev/null || true)
	if [ -z "$PREV_IMAGE_ID" ] && [ -n "$PREV_IMAGE" ]; then PREV_IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$PREV_IMAGE" 2>/dev/null || true); fi
	[ ! -r "$COMPOSE_FILE" ] || cp -p "$COMPOSE_FILE" "$COMPOSE_FILE.previous"
	cp -p "$ENV_FILE" "$ENV_FILE.previous"
}

# step_prebackup: re-running the installer over an existing install is an
# upgrade. The running control plane takes a control-plane backup first (its own
# pg_dump matches the database it reads); when that fails the upgrade stops before
# anything is changed, unless --force-upgrade. A control plane too old to have the
# command is backed up with pg_dump in the Postgres container instead.
step_prebackup() {
	NC_STEP="backing up the control plane before the upgrade"
	[ "$CHECK_ONLY" = 0 ] || return 0
	[ -e "$ENV_FILE" ] || return 0 # a new installation: nothing to back up
	nc_log "Backup before the upgrade"
	local running to_arg='' force_arg='' out rc=0 stamp dump
	running=$(dc ps --status running --services 2>/dev/null || true)
	case "$VERSION" in [0-9]*.[0-9]*.[0-9]*) to_arg="--to ${VERSION#v}" ;; esac
	[ -z "$FORCE_UPGRADE" ] || force_arg=--force
	if printf '%s\n' "$running" | grep -qx nebulactrl; then
		# shellcheck disable=SC2086
		out=$(dc exec -T nebulactrl nebulactrl-server pre-upgrade-backup $to_arg $force_arg 2>&1) || rc=$?
		case $rc in
		0) nc_ok "$(printf '%s' "$out" | tail -n 1)"; return 0 ;;
		3) prebackup_failed "$(printf '%s' "$out" | head -n 1)"; return 0 ;;
		esac
		nc_info "the running control plane cannot take the backup itself ($(printf '%s' "$out" | tail -n 1)); dumping the database instead"
	fi
	if ! printf '%s\n' "$running" | grep -qx postgres; then
		nc_info "the control plane and its database are not running: nothing to back up from"
		return 0
	fi
	stamp=$(date -u +%Y%m%dT%H%M%SZ)
	dump="$BACKUP_HOST_DIR/pre-upgrade-$stamp.dump"
	mkdir -p "$BACKUP_HOST_DIR"
	# The password reaches pg_dump through the environment, never a command line.
	if PGPASSWORD=$(env_get POSTGRES_PASSWORD) dc exec -T -e PGPASSWORD postgres pg_dump -U nebulactrl -d nebulactrl --format=custom --no-owner --no-acl --exclude-table-data=sessions >"$dump" 2>/dev/null && [ -s "$dump" ]; then
		chmod 0600 "$dump"
		nc_ok "database dumped to $dump (restore: docs/operations/backup-restore.md)"
	else
		rm -f "$dump"
		prebackup_failed "pg_dump of the database failed"
	fi
}

# prebackup_failed <why>: stop the upgrade, or warn when --force-upgrade.
prebackup_failed() {
	if [ -n "$FORCE_UPGRADE" ]; then
		nc_warn "the backup before the upgrade failed ($1); continuing because of --force-upgrade"
		return 0
	fi
	nc_die "the control-plane backup before the upgrade failed: $1. Nothing was changed. Fix the cause, or re-run with --force-upgrade to upgrade without a backup."
}

# nc_prepare_instance_dir creates the --instance-dir for the built-in services. It holds
# every team's backups (database dumps included) and images, so it is root's alone: 0700,
# with 0750 service directories (each owned by the uid its container runs as). Files the
# services write inside stay out of other users' reach because the directory above them is
# closed; the one-level check below tightens what an earlier installation left readable.
nc_prepare_instance_dir() {
	local d=$INSTANCE_DIR
	[ -n "$d" ] || return 0
	[[ $d == /* ]] || nc_die "--instance-dir must be an absolute path"
	[ ! -L "$d" ] || nc_die "--instance-dir $d is a symbolic link; give the real directory"
	mkdir -p "$d/s3" "$d/registry" "$d/loki"
	chmod 0700 "$d"
	chmod 0750 "$d/s3" "$d/registry" "$d/loki"
	# The installer runs as root; the check lets the function run unprivileged in a test.
	if [ "$(id -u)" = 0 ]; then
		chown 0:0 "$d" "$d/s3" "$d/registry" && chown 10001:10001 "$d/loki" || nc_die "could not set the owner of $d (a file system that does not keep owners cannot hold the built-in services' data)"
	fi
	# An older installation made these 0755. Tighten what is one level down (not the data: it can be terabytes).
	find "$d" -mindepth 1 -maxdepth 2 -perm /o+rwx -exec chmod o-rwx {} + 2>/dev/null || true
	# GNU stat on the target systems; the BSD form lets the installer's tests run on a Mac.
	[ "$(stat -c %a "$d" 2>/dev/null || stat -f %Lp "$d")" = 700 ] || nc_die "$d is not mode 0700 after setting it; the file system does not keep permissions"
	[ "$(stat -c %u "$d" 2>/dev/null || stat -f %u "$d")" = "$(id -u)" ] || nc_die "$d is not owned by the user running the installer"
}

step_files() {
	NC_STEP="writing the installation files"
	nc_log "Installation files in $INSTALL_DIR"
	umask 077
	mkdir -p "$INSTALL_DIR" "$STATE_DIR"
	chmod 0750 "$INSTALL_DIR"
	if [ ! -e "$ENV_FILE" ]; then
		FRESH=1
		# Never generate a new secret key over an existing database.
		if docker volume inspect nebulactrl_postgres-data >/dev/null 2>&1; then
			nc_die "a database volume (nebulactrl_postgres-data) from an earlier install exists but $ENV_FILE does not. Its secret key and password are gone. Restore $ENV_FILE from your backup, or remove the old install with uninstall.sh --purge first."
		fi
	else
		FRESH=0
	fi

	local pgpw key setup_tok enroll_tok
	pgpw=$(env_get POSTGRES_PASSWORD); [ -n "$pgpw" ] || pgpw=$(rand_hex 24)
	key=$(env_get NEBULACTRL_SECRET_KEY); [ -n "$key" ] || key=$(rand_b64_32)
	setup_tok=$(env_get NEBULACTRL_SETUP_TOKEN); [ -n "$setup_tok" ] || setup_tok=$(rand_b32 20)
	enroll_tok=$(env_get NEBULACTRL_LOCAL_ENROLL_TOKEN); [ -n "$enroll_tok" ] || enroll_tok=nct_enroll_$(rand_b32 32)
	# The built-in services: the control plane's admin identity in the S3 (generated once; the S3
	# container creates it at start and the control plane signs its IAM calls with it) and the
	# registry's upload-state secret (a restart must not invalidate running pushes).
	local s3key s3secret regsec
	s3key=$(env_get NEBULACTRL_S3_ADMIN_KEY); [ -n "$s3key" ] || s3key=$(rand_b32 16 | tr 'a-z' 'A-Z' | cut -c1-20)
	s3secret=$(env_get NEBULACTRL_S3_ADMIN_SECRET); [ -n "$s3secret" ] || s3secret=$(rand_hex 20)
	regsec=$(env_get NEBULACTRL_REGISTRY_HTTP_SECRET); [ -n "$regsec" ] || regsec=$(rand_hex 24)

	# Postgres tuning, from the RAM of this host (it also runs apps, so the share is modest).
	local sb ecs wm mwm mc=100 rpc=1.1
	sb=$((MEM_MB * 15 / 100)); [ "$sb" -lt 128 ] && sb=128; [ "$sb" -gt 2048 ] && sb=2048
	ecs=$((MEM_MB * 40 / 100)); [ "$ecs" -lt 256 ] && ecs=256; [ "$ecs" -gt 12288 ] && ecs=12288
	wm=$((MEM_MB / 256)); [ "$wm" -lt 4 ] && wm=4; [ "$wm" -gt 32 ] && wm=32
	mwm=$((MEM_MB / 16)); [ "$mwm" -lt 64 ] && mwm=64; [ "$mwm" -gt 512 ] && mwm=512
	if [ -r /sys/block ] || [ -d /sys/block ]; then
		local rot
		rot=$(cat /sys/block/"$(lsblk -ndo PKNAME "$(findmnt -no SOURCE /)" 2>/dev/null | head -1)"/queue/rotational 2>/dev/null || echo 0)
		[ "$rot" = 1 ] && rpc=4.0
	fi

	local upstream='' status_upstream='' name ip_val local_token=''
	if step_on "$NO_LOCAL_SERVER"; then
		# The local server's agent enrolls with this token, and its proxy serves the dashboard and,
		# through the status listener (never published on the host), the public status pages.
		local_token=$enroll_tok
		if [ "$MODE" = domain ]; then
			upstream=nebulactrl:8080
			status_upstream=nebulactrl:8081
		fi
	fi
	name=$(hostname -s 2>/dev/null || hostname)
	ip_val=$PUBLIC_IP
	# The DNS-01 token of the dashboard certificate: from --dns-token-file, else the one kept from an earlier
	# run. It lives only in this 0600 file (and sealed in the database once the control plane read it).
	local dash_tok=''
	if [ -n "$DASHBOARD_DNS01" ]; then dash_tok=${DNS_TOKEN:-$(env_get NEBULACTRL_DASHBOARD_DNS_TOKEN)}; fi

	local tmp
	tmp=$(mktemp "$INSTALL_DIR/.env.XXXXXX")
	{
		printf '# Generated by install.sh (version %s). Mode 0600: this file holds every secret.\n' "$NC_INSTALLER_VERSION"
		printf '# NEBULACTRL_SECRET_KEY exists nowhere else: back it up apart from the database backups.\n'
		printf 'NEBULACTRL_IMAGE=%s\n' "$IMAGE"
		printf 'POSTGRES_PASSWORD=%s\n' "$pgpw"
		printf 'NEBULACTRL_SECRET_KEY=%s\n' "$key"
		printf 'NEBULACTRL_PUBLIC_URL=%s\n' "$PUBLIC_URL"
		printf 'NEBULACTRL_BIND=%s\n' "$CP_BIND"
		printf 'NEBULACTRL_PORT=%s\n' "$CP_PORT"
		printf 'NEBULACTRL_SETUP_TOKEN=%s\n' "$setup_tok"
		printf 'NEBULACTRL_DASHBOARD_UPSTREAM=%s\n' "$upstream"
		printf 'NEBULACTRL_STATUS_UPSTREAM=%s\n' "$status_upstream"
		printf 'NEBULACTRL_LOCAL_ENROLL_TOKEN=%s\n' "$local_token"
		printf 'NEBULACTRL_LOCAL_SERVER_NAME=%s\n' "$name"
		printf 'NEBULACTRL_LOCAL_SERVER_IP=%s\n' "$ip_val"
		printf 'NEBULACTRL_ACME_EMAIL=%s\n' "$EMAIL"
		printf 'NEBULACTRL_DASHBOARD_ALLOW_CIDRS=%s\n' "$DASHBOARD_ALLOW"
		printf 'NEBULACTRL_DASHBOARD_DNS_RESOLVER=%s\n' "$DASHBOARD_DNS01"
		printf 'NEBULACTRL_DASHBOARD_DNS_TOKEN=%s\n' "$dash_tok"
		printf 'NEBULACTRL_BACKUP_HOST_DIR=%s\n' "$BACKUP_HOST_DIR"
		printf 'NEBULACTRL_S3_ADMIN_KEY=%s\n' "$s3key"
		printf 'NEBULACTRL_S3_ADMIN_SECRET=%s\n' "$s3secret"
		printf 'NEBULACTRL_REGISTRY_HTTP_SECRET=%s\n' "$regsec"
		printf 'NEBULACTRL_REGISTRY_AUTH_DIR=%s\n' "$INSTALL_DIR/registry-auth"
		# Without --instance-dir the compose file's named volumes hold the built-in services' data.
		if [ -n "$INSTANCE_DIR" ]; then
			printf 'NEBULACTRL_S3_DATA=%s\n' "$INSTANCE_DIR/s3"
			printf 'NEBULACTRL_REGISTRY_DATA=%s\n' "$INSTANCE_DIR/registry"
			printf 'NEBULACTRL_LOKI_DATA=%s\n' "$INSTANCE_DIR/loki"
		fi
		# Written only when on: a stored "false" would lock the switch in Admin -> MCP, so it is dropped.
		if [ "$(env_get NEBULACTRL_MCP_ENABLED)" = true ]; then printf 'NEBULACTRL_MCP_ENABLED=true\n'; fi
		# Settings the admin may have added to .env by hand (docs/operations/updating.md): kept as they are.
		for opt in NEBULACTRL_UPDATE_CHECK NEBULACTRL_UPDATE_FEED_URL; do
			opt_val=$(env_get "$opt")
			[ -z "$opt_val" ] || printf '%s=%s\n' "$opt" "$opt_val"
		done
		printf 'NEBULACTRL_LOG_LEVEL=%s\n' "$(env_get NEBULACTRL_LOG_LEVEL | grep . || echo info)"
		printf 'PG_SHARED_BUFFERS=%sMB\n' "$sb"
		printf 'PG_EFFECTIVE_CACHE_SIZE=%sMB\n' "$ecs"
		printf 'PG_WORK_MEM=%sMB\n' "$wm"
		printf 'PG_MAINTENANCE_WORK_MEM=%sMB\n' "$mwm"
		printf 'PG_MAX_CONNECTIONS=%s\n' "$mc"
		printf 'PG_RANDOM_PAGE_COST=%s\n' "$rpc"
		if [ "$NEED_LOOPBACK" = 1 ]; then printf 'COMPOSE_FILE=docker-compose.yml:docker-compose.loopback.yml\n'; fi
	} >"$tmp"
	chmod 0600 "$tmp"
	mv "$tmp" "$ENV_FILE"
	if [ "$FRESH" = 1 ]; then nc_ok "generated secrets in $ENV_FILE (0600)"; else nc_ok "kept the existing secrets, refreshed settings in $ENV_FILE"; fi
	if [ "$NEED_LOOPBACK" = 1 ]; then
		# Bound to one address (for example the Tailscale IP): the host's agent still needs 127.0.0.1.
		printf 'services:\n  nebulactrl:\n    ports:\n      - "127.0.0.1:${NEBULACTRL_PORT}:8080"\n' >"$INSTALL_DIR/docker-compose.loopback.yml"
		chmod 0644 "$INSTALL_DIR/docker-compose.loopback.yml"
	else
		rm -f "$INSTALL_DIR/docker-compose.loopback.yml"
	fi

	# The built-in services' configuration (read-only mounts) and directories.
	local cfg
	mkdir -p "$INSTALL_DIR/instance"
	for cfg in master.toml registry.yaml loki.yaml; do
		nc_asset "instance/$cfg" >"$INSTALL_DIR/instance/$cfg.tmp" && [ -s "$INSTALL_DIR/instance/$cfg.tmp" ] || nc_die "could not obtain instance/$cfg"
		chmod 0644 "$INSTALL_DIR/instance/$cfg.tmp"
		mv "$INSTALL_DIR/instance/$cfg.tmp" "$INSTALL_DIR/instance/$cfg"
	done
	# The registry's public token key: written by the control plane (uid 65532), read by the registry.
	install -d -m 0755 -o 65532 -g 65532 "$INSTALL_DIR/registry-auth"
	if [ -n "$INSTANCE_DIR" ]; then
		nc_prepare_instance_dir
		nc_ok "built-in services keep their data in $INSTANCE_DIR (mode 0700)"
	fi

	nc_asset docker-compose.yml >"$COMPOSE_FILE.tmp"
	[ -s "$COMPOSE_FILE.tmp" ] || nc_die "could not obtain docker-compose.yml"
	chmod 0644 "$COMPOSE_FILE.tmp"
	mv "$COMPOSE_FILE.tmp" "$COMPOSE_FILE"
	nc_asset uninstall.sh >"$INSTALL_DIR/uninstall.sh.tmp" && [ -s "$INSTALL_DIR/uninstall.sh.tmp" ] \
		&& chmod 0750 "$INSTALL_DIR/uninstall.sh.tmp" && mv "$INSTALL_DIR/uninstall.sh.tmp" "$INSTALL_DIR/uninstall.sh" \
		|| { rm -f "$INSTALL_DIR/uninstall.sh.tmp"; nc_warn "could not save uninstall.sh"; }
	save_conf
	install -d -m 0700 -o 65532 -g 65532 "$BACKUP_HOST_DIR"
	nc_ok "backup directory $BACKUP_HOST_DIR"
}

save_conf() {
	local f=$INSTALL_DIR/install.conf k
	: >"$f.tmp"
	for k in "${NC_CONF_KEYS[@]}"; do
		printf '%s=%q\n' "$k" "${!k:-}" >>"$f.tmp"
	done
	chmod 0600 "$f.tmp"
	mv "$f.tmp" "$f"
}

# step_release: the control-plane image this run installs, pinned by digest from the signed
# release manifest. An image of the operator's own (--image, --image-archive) is left alone.
step_release() {
	NC_STEP="verifying the release"
	if [ -n "$IMAGE_ARCHIVE" ] || [ "$NC_IMAGE_CUSTOM" = 1 ]; then
		nc_warn "the image ($IMAGE${IMAGE_ARCHIVE:+, from $IMAGE_ARCHIVE}) is not checked against a NebulaCtrl release signature: it was chosen with --image or --image-archive"
		return 0
	fi
	nc_log "Release"
	nc_manifest_load
	local ref
	ref=$(nc_manifest_value image)
	[[ $ref =~ ^ghcr\.io/nebulactrl/nebulactrl@sha256:[0-9a-f]{64}$ ]] || nc_die "the signed release manifest has no usable image reference"
	[ "$VERSION" != latest ] || VERSION=$NC_MANIFEST_VERSION
	IMAGE=$ref
	NC_RELEASE_VERIFIED=1
	nc_ok "release $VERSION: signature verified, image ${ref#*@}"
}

step_image() {
	NC_STEP="getting the NebulaCtrl image"
	nc_log "NebulaCtrl image"
	if [ -n "$IMAGE_ARCHIVE" ]; then
		[ -r "$IMAGE_ARCHIVE" ] || nc_die "cannot read --image-archive $IMAGE_ARCHIVE"
		docker load -i "$IMAGE_ARCHIVE" >/dev/null
		docker image inspect "$IMAGE" >/dev/null 2>&1 || nc_die "the archive does not contain the image $IMAGE (pass --image <name:tag> as it was saved)"
		nc_ok "loaded $IMAGE from $IMAGE_ARCHIVE"
	elif docker image inspect "$IMAGE" >/dev/null 2>&1 && [ "$VERSION" != latest ]; then
		nc_ok "$IMAGE is present"
	else
		nc_info "pulling $IMAGE ..."
		docker pull -q "$IMAGE" >/dev/null || nc_die "could not pull $IMAGE. Check the version and that the image is public (ghcr.io/nebulactrl/nebulactrl), or use a local build: docker build -t nebulactrl:local . and then --image nebulactrl:local or --image-archive <file>."
		nc_ok "pulled $IMAGE"
	fi
}

step_start() {
	NC_STEP="starting the control plane"
	nc_log "Starting the control plane"
	# A fresh install on a host that still has an agent identity of an older one.
	if [ "${FRESH:-0}" = 1 ] && [ -s "$AGENT_STATE_DIR/agent.db" ]; then
		nc_warn "an agent identity from an earlier installation exists in $AGENT_STATE_DIR; moving it aside so this host enrolls with the new control plane"
		systemctl stop nebulactrl-agent >/dev/null 2>&1 || true
		mv "$AGENT_STATE_DIR" "$AGENT_STATE_DIR.old-$(date +%s)"
	fi
	local healthy=0 why=''
	if dc up -d --remove-orphans --wait --wait-timeout 240 >/dev/null 2>&1; then
		if wait_ready; then healthy=1; else why="the control plane does not answer on $CP_URL_LOCAL/readyz"; fi
	else
		nc_warn "docker compose reported a problem; recent logs:"
		why="the control plane did not become healthy"
	fi
	if [ "$healthy" != 1 ]; then
		dc logs --tail 30 >&2 || true
		start_failed "$why"
	fi
	nc_ok "control plane is ready on $CP_URL_LOCAL"
}

# start_failed <why>: the new control plane is not healthy. An upgrade puts the previous
# version back (image, compose file, settings) and exits with EXIT_ROLLED_BACK; anything
# else stops here.
start_failed() {
	if [ "$UPGRADING" != 1 ] || [ -z "$PREV_IMAGE_ID" ] || [ -z "$PREV_IMAGE" ] || [ ! -s "$ENV_FILE.previous" ]; then
		nc_die "$1"
	fi
	NC_STEP="rolling back to the previous control plane"
	nc_log "Rolling back to $PREV_IMAGE"
	nc_warn "$1"
	cp -p "$ENV_FILE.previous" "$ENV_FILE"
	[ ! -s "$COMPOSE_FILE.previous" ] || cp -p "$COMPOSE_FILE.previous" "$COMPOSE_FILE"
	docker tag "$PREV_IMAGE_ID" "$PREV_IMAGE" >/dev/null 2>&1 || true
	if dc up -d --remove-orphans --wait --wait-timeout 240 >/dev/null 2>&1 && wait_ready; then
		NC_FAIL_NOTE="The new version did not become healthy ($1). The installer rolled back: the previous version ($PREV_IMAGE) is running again."
		printf '%serror:%s %s\n' "$NC_RED" "$NC_R" "$NC_FAIL_NOTE" >&2
		exit "$EXIT_ROLLED_BACK"
	fi
	dc logs --tail 30 >&2 || true
	NC_FAIL_NOTE="The new version did not become healthy ($1) and putting the previous version ($PREV_IMAGE) back did not work either: the control plane is down. See docker compose logs in $INSTALL_DIR and restore from the backup taken before the upgrade."
	nc_die "$NC_FAIL_NOTE"
}

step_backup() {
	NC_STEP="scheduling the control-plane backup"
	step_on "$NO_BACKUP" || { nc_info "backup schedule skipped (--no-backup)"; return 0; }
	nc_log "Daily control-plane backup"
	if state_has backup-seeded; then
		nc_ok "already scheduled (change it in Admin -> Control-plane backup)"
		return 0
	fi
	# Seed the instance backup (Admin -> Control-plane backup): daily 03:30 UTC, 14 kept, to a
	# local directory, only while nobody configured it yet. The first run follows within a minute.
	local out
	out=$(PGPASSWORD=$(env_get POSTGRES_PASSWORD) dc exec -T -e PGPASSWORD postgres psql -U nebulactrl -d nebulactrl -tA -v ON_ERROR_STOP=1 -c "
UPDATE instance_backup_settings
SET enabled = true, dest_kind = 'local', local_dir = 'control-plane', next_run_at = now(), updated_at = now()
WHERE id AND NOT enabled AND bucket = '' AND local_dir = '' AND next_run_at IS NULL AND last_test_at IS NULL
RETURNING enabled;" 2>&1) || { nc_warn "could not schedule the backup: $out"; return 0; }
	state_set backup-seeded
	nc_ok "daily 03:30 UTC to $BACKUP_HOST_DIR/control-plane (14 kept)"
	nc_info "this is a local copy, not off-site: add an S3-compatible bucket under Admin -> Control-plane backup"
}

# proxy_on_ports: the local proxy runs and publishes the configured HTTP and HTTPS ports.
proxy_on_ports() {
	local cur
	docker inspect -f '{{.State.Running}}' nebulactrl-traefik 2>/dev/null | grep -q true || return 1
	cur=" $(traefik_host_ports) "
	case "$cur" in *" $PROXY_HTTP_PORT "*) ;; *) return 1 ;; esac
	case "$cur" in *" $PROXY_HTTPS_PORT "*) ;; *) return 1 ;; esac
}

step_agent() {
	NC_STEP="installing the local agent"
	step_on "$NO_LOCAL_SERVER" || { nc_info "local server skipped (--no-local-server)"; return 0; }
	nc_log "Agent on this host (the first server)"
	local script args
	script=$(mktemp)
	curl -fsSL --retry 3 "$CP_URL_LOCAL/install-agent.sh" -o "$script" || nc_die "could not download the agent installer from $CP_URL_LOCAL/install-agent.sh"
	args=(--url "$CP_URL_LOCAL")
	# The unit is regenerated on every run, so the ports are always passed (80/443/9000 only when not the default).
	[ "$PROXY_HTTP_PORT" = 80 ] || args+=(--proxy-http-port "$PROXY_HTTP_PORT")
	[ "$PROXY_HTTPS_PORT" = 443 ] || args+=(--proxy-https-port "$PROXY_HTTPS_PORT")
	[ "$PROXY_ADMIN_PORT" = 9000 ] || args+=(--proxy-admin-port "$PROXY_ADMIN_PORT")
	if [ -s "$AGENT_STATE_DIR/agent.db" ]; then
		:  # already enrolled: this upgrades the binary and keeps the identity
	else
		args+=(--defer-enroll)
		enroll_token=$(env_get NEBULACTRL_LOCAL_ENROLL_TOKEN)
	fi
	# The agent binary is the one in the release image verified above, and install-agent.sh checks
	# its signature; an image of the operator's own has no signature.
	[ "$NC_RELEASE_VERIFIED" = 1 ] || args+=(--no-verify)
	# The enrollment token goes through the environment, not the command line: argv is readable
	# by every local user (/proc/<pid>/cmdline) while the agent downloads.
	NEBULACTRL_ENROLL_TOKEN=${enroll_token:-} sh "$script" "${args[@]}" >/dev/null || { rm -f "$script"; nc_die "the agent installer failed"; }
	rm -f "$script"
	# While the token is not accepted yet the agent exits and systemd restarts it; keep
	# the restarts well under the enrollment rate limit.
	mkdir -p /etc/systemd/system/nebulactrl-agent.service.d
	printf '[Service]\nRestartSec=12\n' >/etc/systemd/system/nebulactrl-agent.service.d/10-installer.conf
	systemctl daemon-reload
	nc_ok "agent installed ($AGENT_BIN)"
	local i
	nc_info "waiting for the agent to enroll ..."
	for i in $(seq 1 60); do
		[ -s "$AGENT_STATE_DIR/agent.db" ] && systemctl is-active --quiet nebulactrl-agent && break
		sleep 2
	done
	if [ -s "$AGENT_STATE_DIR/agent.db" ]; then nc_ok "enrolled as the first server"; else nc_warn "the agent has not enrolled yet; check: journalctl -u nebulactrl-agent"; fi
	if [ "$MODE" = domain ] || [ "$PROXY_MOVING" = 1 ]; then
		nc_info "waiting for the proxy (ports $PROXY_HTTP_PORT/$PROXY_HTTPS_PORT) ..."
		for i in $(seq 1 60); do
			proxy_on_ports && break
			sleep 2
		done
		if proxy_on_ports; then
			nc_ok "proxy running on ports $PROXY_HTTP_PORT/$PROXY_HTTPS_PORT"
		else
			nc_warn "the proxy is not running on ports $PROXY_HTTP_PORT/$PROXY_HTTPS_PORT yet; check: docker logs nebulactrl-traefik and journalctl -u nebulactrl-agent. A port that something else holds, or a proxy-ports setting made in the dashboard (Servers, Proxy), overrides these flags."
		fi
	fi
}

ssh_ports() {
	local ports='' p
	if have sshd; then ports=$(sshd -T 2>/dev/null | awk '$1 == "port" {print $2}' || true); fi
	# Also the port this very session came in on, and anything sshd listens on.
	if [ -n "${SSH_CONNECTION:-}" ]; then ports="$ports $(printf '%s' "$SSH_CONNECTION" | awk '{print $4}')"; fi
	ports="$ports $(ss -H -ltnp 2>/dev/null | awk '/"sshd"/ {n = split($4, a, ":"); print a[n]}' || true)"
	[ -n "$(printf '%s' "$ports" | tr -d ' ')" ] || ports=22
	printf '%s\n' $ports | grep -E '^[0-9]+$' | sort -un | tr '\n' ' '
}

step_firewall() {
	NC_STEP="configuring the firewall"
	step_on "$NO_FIREWALL" || { nc_info "firewall skipped (--no-firewall)"; return 0; }
	nc_log "Firewall"
	local sshp p ports
	sshp=$(ssh_ports | xargs)
	ports="$PROXY_HTTP_PORT/tcp $PROXY_HTTPS_PORT/tcp $PROXY_HTTPS_PORT/udp"
	[ "$MODE" = ip ] && ports="$ports $CP_PORT/tcp"
	if [ "$NC_PKG" = apt ]; then
		have ufw || pkg_install ufw
		local was_active=0
		ufw status 2>/dev/null | grep -q '^Status: active' && was_active=1
		if [ "$was_active" = 0 ]; then
			# SSH first, then everything else, then switch on: never lock yourself out.
			ufw default deny incoming >/dev/null
			ufw default allow outgoing >/dev/null
		fi
		for p in $sshp; do ufw allow "$p/tcp" comment 'NebulaCtrl: SSH' >/dev/null; done
		for p in $ports; do ufw allow "$p" comment 'NebulaCtrl' >/dev/null; done
		if [ "$was_active" = 0 ]; then
			local listening
			listening=$(ss -H -ltn 2>/dev/null | awk '{n = split($4, a, ":"); print a[n]}' | sort -un | { grep -Ev "^(80|443|8000|8080|9000|$CP_PORT|$PROXY_HTTP_PORT|$PROXY_HTTPS_PORT)\$" || true; } | tr '\n' ' ')
			if ufw --force enable >/dev/null 2>&1; then
				state_set ufw-enabled-by-installer
				nc_ok "ufw enabled: SSH ($sshp), $ports"
				if [ -n "$(printf '%s' "$listening" | tr -d ' ')" ]; then nc_info "ports that listen on this machine and are now blocked from the network: $listening"; fi
			else
				nc_warn "could not enable ufw (the kernel may lack firewall support, as in some containers). Rules were added; enable it with 'ufw enable' where supported."
			fi
		else
			nc_ok "ufw already active: added SSH ($sshp), $ports"
		fi
		nc_info "note: ports Docker publishes (for example TCP/UDP routes you add) bypass ufw; NebulaCtrl restricts them with allowlists instead"
	else
		have firewall-cmd || pkg_install firewalld
		# Starting or reloading firewalld flushes the packet-filter rules Docker installed,
		# which cuts every container off the network. So: add each rule to the running
		# firewall and to the permanent configuration (no reload), and when firewalld
		# had to be started, restart Docker afterwards so it re-creates its rules.
		local fw_was_running=1
		firewall-cmd --state >/dev/null 2>&1 || fw_was_running=0
		systemctl enable --now firewalld >/dev/null 2>&1 || true
		if firewall-cmd --state >/dev/null 2>&1; then
			for p in $sshp; do firewall-cmd -q --add-port="$p/tcp"; firewall-cmd -q --permanent --add-port="$p/tcp"; done
			for p in $ports; do firewall-cmd -q --add-port="$p"; firewall-cmd -q --permanent --add-port="$p"; done
			if [ "$fw_was_running" = 0 ]; then
				systemctl restart docker
				local i
				for i in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
				dc up -d --wait --wait-timeout 120 >/dev/null 2>&1 || nc_warn "the control plane did not come back after firewalld started; check: docker compose -f $COMPOSE_FILE ps"
			fi
			state_set firewalld-configured
			nc_ok "firewalld: SSH ($sshp), $ports"
		else
			nc_warn "firewalld is not running (the kernel may lack firewall support, as in some containers); rules were not applied"
		fi
	fi
}

step_fail2ban() {
	NC_STEP="installing fail2ban"
	step_on "$NO_FAIL2BAN" || { nc_info "fail2ban skipped (--no-fail2ban)"; return 0; }
	nc_log "fail2ban (SSH)"
	local sshp
	sshp=$(ssh_ports | tr ' ' ',' | sed 's/,$//')
	if ! have fail2ban-client; then
		if [ "$NC_PKG" = apt ]; then
			pkg_install fail2ban python3-systemd
		else
			pkg_install epel-release
			pkg_install fail2ban || nc_warn "fail2ban is not available from the enabled repositories"
		fi
	fi
	have fail2ban-client || { nc_warn "fail2ban could not be installed"; return 0; }
	mkdir -p /etc/fail2ban/jail.d
	cat >/etc/fail2ban/jail.d/nebulactrl-sshd.local <<EOF
# Written by the NebulaCtrl installer.
[sshd]
enabled = true
port = $sshp
backend = systemd
maxretry = 5
findtime = 10m
bantime = 1h
EOF
	systemctl enable fail2ban >/dev/null 2>&1 || true
	systemctl restart fail2ban >/dev/null 2>&1 || true
	sleep 2
	if systemctl is-active --quiet fail2ban; then nc_ok "fail2ban watches sshd (port $sshp)"; else nc_warn "fail2ban is installed but not running; check: journalctl -u fail2ban"; fi
	state_set fail2ban-jail
}

step_updates() {
	NC_STEP="enabling automatic security updates"
	step_on "$NO_AUTO_UPDATES" || { nc_info "automatic updates skipped (--no-auto-updates)"; return 0; }
	nc_log "Automatic security updates"
	if [ "$NC_PKG" = apt ]; then
		have unattended-upgrade || pkg_install unattended-upgrades
		cat >/etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
// Written by the NebulaCtrl installer.
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF
		nc_ok "unattended-upgrades enabled (security updates; no automatic reboot)"
	else
		pkg_install dnf-automatic
		local conf=/etc/dnf/automatic.conf
		if [ -r "$conf" ]; then
			sed -i -E 's/^#?\s*upgrade_type\s*=.*/upgrade_type = security/; s/^#?\s*apply_updates\s*=.*/apply_updates = yes/' "$conf"
		fi
		systemctl enable --now dnf-automatic.timer >/dev/null 2>&1 || nc_warn "could not enable dnf-automatic.timer"
		nc_ok "dnf-automatic enabled (security updates)"
	fi
	state_set auto-updates
}

step_hardening() {
	NC_STEP="running the Lynis hardening audit"
	step_on "$NO_HARDENING" || { nc_info "hardening skipped (--no-hardening)"; return 0; }
	nc_load_libs
	if [ "$(type -t nc_hardening_step 2>/dev/null)" != function ]; then
		nc_warn "the hardening library is not available; skipping the Lynis step (re-run later, or use the server's Security tab)"
		return 0
	fi
	nc_log "Lynis hardening audit"
	# Non-fatal: a failure only warns.
	nc_hardening_step || nc_warn "the hardening step reported a problem; see the output above"
}

setup_needed() {
	curl -fsS --max-time 5 "$CP_URL_LOCAL/api/v1/setup" 2>/dev/null | grep -q '"needsSetup":true'
}

step_summary() {
	NC_STEP="finishing"
	local tok setup_url sum=$INSTALL_DIR/INSTALL-SUMMARY.txt
	tok=$(env_get NEBULACTRL_SETUP_TOKEN)
	printf '\n'
	printf '%s================================================================%s\n' "$NC_B" "$NC_R"
	printf '%s NebulaCtrl is installed%s\n' "$NC_B" "$NC_R"
	printf '%s================================================================%s\n' "$NC_B" "$NC_R"
	printf ' Dashboard : %s\n' "$PUBLIC_URL"
	if [ "$MODE" = ip ] && [ -n "$BIND" ] && [ "$BIND" != 0.0.0.0 ]; then printf ' Bound to  : %s (remembered; --bind 0.0.0.0 opens every address)\n' "$BIND"; fi
	if [ -n "$DASHBOARD_ALLOW" ]; then printf ' Allowed   : %s (edit: Admin -> Dashboard access)\n' "$DASHBOARD_ALLOW"; fi
	if [ -n "$DASHBOARD_DNS01" ]; then printf ' Certificate: DNS-01 through %s (the token is not shown)\n' "$DASHBOARD_DNS01"; fi
	if step_on "$NO_LOCAL_SERVER" && { [ "$PROXY_HTTP_PORT" != 80 ] || [ "$PROXY_HTTPS_PORT" != 443 ]; }; then
		printf ' Proxy     : HTTP %s, HTTPS %s (next to another proxy). Move it back to 80/443 with:\n' "$PROXY_HTTP_PORT" "$PROXY_HTTPS_PORT"
		printf '             curl -fsSL %s/install.sh | sudo bash -s -- --proxy-http-port 80 --proxy-https-port 443   (or Servers -> Proxy)\n' "$CP_URL_LOCAL"
	fi
	if setup_needed; then
		setup_url="$PUBLIC_URL/setup?token=$tok"
		printf ' Setup URL : %s\n' "$setup_url"
		printf '             open it to create the admin account (the token works once, until setup is done)\n'
	else
		printf ' Setup     : already done; sign in at %s/login\n' "$PUBLIC_URL"
	fi
	printf ' Files     : %s  (.env holds every secret, mode 0600)\n' "$INSTALL_DIR"
	printf ' Backups   : %s/control-plane (daily 03:30 UTC)\n' "$BACKUP_HOST_DIR"
	printf ' Built-in  : S3, image registry and log store for every team, on this machine (data: %s)\n' "${INSTANCE_DIR:-Docker volumes}"
	printf '\n'
	printf '%s MUST DO NOW: back up the secret key%s\n' "$NC_Y" "$NC_R"
	printf '   sudo grep NEBULACTRL_SECRET_KEY %s\n' "$ENV_FILE"
	printf '   Store it in a password manager, apart from the backups. It exists nowhere else;\n'
	printf '   without it the database and its backups cannot be read.\n'
	printf '\n'
	printf '%s Off-site backup%s: the daily backup stays on this machine. Add an S3-compatible\n' "$NC_Y" "$NC_R"
	printf '   bucket under Admin -> Control-plane backup so a lost host does not lose your data.\n'
	printf '   The built-in S3 is on this machine too, so it is not off-site either.\n'
	if [ "$MODE" = ip ]; then
		printf '\n'
		printf '%s NOTICE: no domain, so the dashboard uses plain HTTP%s\n' "$NC_Y" "$NC_R"
		printf '   Passwords and sessions cross the network unencrypted, and servers you add\n'
		printf '   from other machines need --insecure-dev. Use it only on a trusted network, and\n'
		printf '   switch to HTTPS soon: re-run the installer with a domain whose A record points here:\n'
		printf '   curl -fsSL <url>/install.sh | sudo bash -s -- --domain <your.domain> --email <you@example.com>\n'
	fi
	if [ ${#NC_WARNINGS[@]} -gt 0 ]; then
		printf '\n%s Warnings (%s)%s\n' "$NC_Y" "${#NC_WARNINGS[@]}" "$NC_R"
		local w
		for w in "${NC_WARNINGS[@]}"; do printf '   - %s\n' "$w"; done
	fi
	printf '\n Re-run this installer any time to upgrade or repair. Uninstall: sudo bash %s/uninstall.sh\n' "$INSTALL_DIR"
	{
		printf 'NebulaCtrl installed %s\n' "$(date -u +%FT%TZ)"
		printf 'Dashboard: %s\n' "$PUBLIC_URL"
		setup_needed && printf 'Setup URL: %s\n' "$PUBLIC_URL/setup?token=$tok"
	} >"$sum"
	chmod 0600 "$sum"
}

main() {
	if [ "$UNINSTALL" = 1 ]; then
		nc_asset uninstall.sh | bash -s -- "${UNINSTALL_ARGS[@]}"
		exit $?
	fi
	step_os
	if [ "$CHECK_ONLY" = 0 ]; then step_packages; step_docker; else
		have docker || nc_info "Docker is not installed: the installer would install it"
	fi
	if [ "$CHECK_ONLY" = 1 ] && ! have ss; then nc_die "the check needs 'ss' (iproute2)"; fi
	if [ "$CHECK_ONLY" = 1 ] && ! have docker; then docker() { return 1; }; fi
	step_plan
	if [ "$CHECK_ONLY" = 0 ]; then step_release; else nc_info "release signature checks are skipped with --check"; fi
	step_remember
	step_prebackup
	step_daemon
	step_network
	step_files
	step_image
	step_start
	step_backup
	step_agent
	step_firewall
	step_fail2ban
	step_updates
	step_hardening
	step_summary
}

# NC_INSTALL_SOURCE_ONLY=1 loads the functions without running them (deploy/install/flags_test.go).
if [ "${NC_INSTALL_SOURCE_ONLY:-0}" != 1 ]; then main; fi
