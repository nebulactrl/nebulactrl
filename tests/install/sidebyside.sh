#!/usr/bin/env bash
# Side-by-side installer scenario, in one disposable
# systemd machine (OrbStack: orbctl). It checks what the installer does on a host that
# already runs another proxy on 80/443 (a busybox container plays Coolify's Traefik):
#
#   1. install with --port 8100 --proxy-http-port 8080 --proxy-https-port 8443
#      --dashboard-allow <tailnet> --dashboard-dns01 cloudflare --dns-token-file ...
#   2. assert sidebyside: the other proxy is untouched, NebulaCtrl's proxy and dashboard
#      are on the spare ports, the allow list is enforced and editable (lock-out guard),
#      the DNS-01 token is sealed and appears nowhere, an app answers on :8443
#   3. a re-run changes nothing; a re-run that asks for 80/443 while the other proxy
#      still holds them is refused and changes nothing
#   4. stop the other proxy, re-run with 80/443: the proxy moves, the URL loses its port
#   5. uninstall, then IP mode with --bind <address>: published on that address only,
#      remembered by a re-run
# The machine is deleted at the end unless --keep is given.
#
#   tests/install/sidebyside.sh [--keep] [--archive image.tar] [target]   (default ubuntu2604)
set -Eeuo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
HERE=$ROOT/tests/install
IMAGE=${NC_TEST_IMAGE:-nebulactrl:instlocal}
ARCHIVE=''
KEEP=0
TARGET=ubuntu2604
LOGDIR=${NC_TEST_LOGDIR:-$HOME/.nc-inst-tmp}
CANARY='cfTok_canary-0123456789abcdefghij'

while [ $# -gt 0 ]; do
	case "$1" in
	--keep) KEEP=1 ;;
	--archive) shift; ARCHIVE=$1 ;;
	-h | --help) sed -n '2,19p' "$0"; exit 0 ;;
	*) TARGET=$1 ;;
	esac
	shift
done
command -v orbctl >/dev/null || { echo "orbctl (OrbStack) is required" >&2; exit 2; }
mkdir -p "$LOGDIR"

case "$TARGET" in
debian12) DISTRO=debian:bookworm ;;
debian13) DISTRO=debian:trixie ;;
ubuntu2204) DISTRO=ubuntu:jammy ;;
ubuntu2404) DISTRO=ubuntu:noble ;;
ubuntu2604) DISTRO=ubuntu:resolute ;;
*) echo "unknown target $TARGET" >&2; exit 2 ;;
esac
NAME=nc-inst-sbs-$TARGET
LOG=$LOGDIR/sbs-$TARGET.log
say() { printf '\n\033[1m### %s\033[0m\n' "$*"; }

if [ -z "$ARCHIVE" ]; then
	say "docker build $IMAGE"
	(cd "$ROOT" && docker build -q -t "$IMAGE" --build-arg VERSION=install-test .) >/dev/null
	ARCHIVE=$LOGDIR/image.tar
	docker save -o "$ARCHIVE" "$IMAGE"
fi
(cd "$ROOT" && go run ./tests/install/assemble) >"$LOGDIR/install-assembled.sh"
bash -n "$LOGDIR/install-assembled.sh"

say "$TARGET ($DISTRO) on machine $NAME; log $LOG"
: >"$LOG"
orbctl delete -f "$NAME" >/dev/null 2>&1 || true
orbctl create "$DISTRO" "$NAME" >>"$LOG" 2>&1
cleanup() { [ "$KEEP" = 1 ] || orbctl delete -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT
rx() { orbctl run -m "$NAME" -u root "$@"; }
rx true || { echo "machine did not start"; exit 1; }
IP=$(rx bash -c "ip -4 -o addr show scope global | awk '{print \$4}' | cut -d/ -f1 | head -1")
echo "machine address $IP" | tee -a "$LOG"
RC=0

phase() { # phase <title> <command...>
	local title=$1; shift
	printf '\n--- %s ---\n' "$title" >>"$LOG"
	if "$@" >>"$LOG" 2>&1; then echo "    ok   $title"; else echo "    FAIL $title (see $LOG)"; RC=1; fi
}
expect_fail() { # expect_fail <title> <message> <command...>: the command must fail and print the message
	local title=$1 msg=$2; shift 2
	local out rc=0
	out=$("$@" 2>&1) || rc=$?
	printf '\n--- %s ---\n%s\n' "$title" "$out" >>"$LOG"
	if [ "$rc" != 0 ] && printf '%s' "$out" | grep -qF -- "$msg"; then echo "    ok   $title"; else echo "    FAIL $title (exit $rc, wanted: $msg; see $LOG)"; RC=1; fi
}
install_sh() { # install_sh <options...>: the assembled script, piped like curl | bash
	orbctl run -m "$NAME" -u root bash -c "cat $LOGDIR/install-assembled.sh | bash -s -- $*"
}
IMG=(--image "$IMAGE" --image-archive "$ARCHIVE" --public-ip "$IP")

# ---- the host: Docker, and another proxy on 80/443 ------------------------------------
say "Docker and the stand-in for Coolify's proxy"
rx bash -c 'command -v curl >/dev/null || (apt-get update -qq && apt-get install -y -qq curl ca-certificates)' >>"$LOG" 2>&1
rx bash -c 'curl -fsSL https://get.docker.com | sh' >>"$LOG" 2>&1
rx docker version -f '{{.Server.Version}}' | tee -a "$LOG"
rx bash -c 'docker run -d --name coolify-proxy-fake --restart unless-stopped -p 80:80 -p 443:443 -p 8000:8000 busybox sh -c "mkdir -p /www && echo coolify-proxy > /www/index.html && httpd -p 80 -h /www && httpd -p 8000 -h /www && exec httpd -f -p 443 -h /www"' >>"$LOG" 2>&1
rx bash -c "umask 077; printf '%s' '$CANARY' > /root/cf-token"
phase "the stand-in proxy answers on 80, 443 and 8000" rx bash -c 'for p in 80 443 8000; do curl -fsS http://127.0.0.1:$p/ | grep -q coolify-proxy || exit 1; done'

# ---- 1. side-by-side install --------------------------------------------------------------
SBS=(--no-firewall --no-fail2ban --no-hardening --port 8100 --proxy-http-port 8080 --proxy-https-port 8443
	--domain nebula.test --email ops@example.com
	--dashboard-allow 100.64.0.0/10,fd7a:115c:a1e0::/48 --dashboard-dns01 cloudflare --dns-token-file /root/cf-token)
say "install (side by side)"
phase "check: the plan passes and changes nothing" install_sh "${IMG[*]}" "${SBS[*]}" --check
phase "install with the side-by-side flags" bash -c "set -o pipefail; orbctl run -m $NAME -u root bash -c 'cat $LOGDIR/install-assembled.sh | bash -s -- ${IMG[*]} ${SBS[*]}' 2>&1 | tee $LOGDIR/sbs.install.out"
phase "the installer output never shows the DNS token" bash -c "! grep -qF '$CANARY' $LOGDIR/sbs.install.out"
phase "the installer did not touch the other proxy" rx bash -c 'for p in 80 443 8000; do curl -fsS http://127.0.0.1:$p/ | grep -q coolify-proxy || exit 1; done'
phase "assert sidebyside" rx env NC_CANARY="$CANARY" bash "$HERE/assert.sh" sidebyside "$IP"
rx bash -c 'docker inspect -f "{{.Id}}" nebulactrl-nebulactrl-1 nebulactrl-postgres-1 nebulactrl-traefik > /tmp/cids.sbs; cp /opt/nebulactrl/.env /tmp/env.sbs; cp /opt/nebulactrl/install.conf /tmp/conf.sbs' >>"$LOG" 2>&1

# ---- 2. re-run, then a refused move ------------------------------------------------------------
say "re-run and a move onto ports that are taken"
phase "re-run with the same flags" install_sh "${IMG[*]}" "${SBS[*]}"
phase "a re-run changes nothing (.env, conf, containers)" rx bash -c 'cmp /tmp/env.sbs /opt/nebulactrl/.env && cmp /tmp/conf.sbs /opt/nebulactrl/install.conf && [ "$(docker inspect -f "{{.Id}}" nebulactrl-nebulactrl-1 nebulactrl-postgres-1 nebulactrl-traefik)" = "$(cat /tmp/cids.sbs)" ]'
phase "a plain re-run (no flags) remembers the layout" install_sh "${IMG[*]}" --no-firewall --no-fail2ban --no-hardening
phase "...and still changes nothing" rx bash -c 'cmp /tmp/env.sbs /opt/nebulactrl/.env && [ "$(docker inspect -f "{{.Id}}" nebulactrl-nebulactrl-1 nebulactrl-postgres-1 nebulactrl-traefik)" = "$(cat /tmp/cids.sbs)" ]'
expect_fail "asking for 80/443 while another proxy holds them is refused" "the proxy's ports are not free" \
	install_sh "${IMG[*]}" --no-firewall --no-fail2ban --no-hardening --proxy-http-port 80 --proxy-https-port 443
phase "the refusal changed nothing" rx bash -c 'docker port nebulactrl-traefik | grep -q "0.0.0.0:8443" && curl -fsS http://127.0.0.1:80/ | grep -q coolify-proxy && cmp /tmp/env.sbs /opt/nebulactrl/.env'

# ---- 3. cutover: the other proxy stops, NebulaCtrl's moves to 80/443 --------------------------------
say "move the proxy to 80/443"
rx docker stop coolify-proxy-fake >>"$LOG" 2>&1
phase "re-run with the default ports" install_sh "${IMG[*]}" --no-firewall --no-fail2ban --no-hardening --proxy-http-port 80 --proxy-https-port 443
phase "assert moveback" rx env NC_CANARY="$CANARY" bash "$HERE/assert.sh" moveback "$IP"

# ---- 4. IP mode bound to one address -----------------------------------------------------------------
say "uninstall, then --bind"
phase "uninstall --purge --remove-workloads" rx bash /opt/nebulactrl/uninstall.sh --yes --purge --remove-workloads --remove-backups
phase "install in IP mode with --bind" install_sh "${IMG[*]}" --no-firewall --no-fail2ban --no-hardening --port 8100 --bind "$IP"
phase "assert bind" rx bash "$HERE/assert.sh" bind "$IP"
phase "a plain re-run keeps the bind address" install_sh "${IMG[*]}" --no-firewall --no-fail2ban --no-hardening
phase "assert bind (after the re-run)" rx bash "$HERE/assert.sh" bind "$IP"
phase "uninstall --purge --remove-workloads" rx bash /opt/nebulactrl/uninstall.sh --yes --purge --remove-workloads --remove-backups
rx docker rm -f coolify-proxy-fake >/dev/null 2>&1 || true

say "summary"
if [ "$RC" != 0 ]; then echo "FAILED (log $LOG)"; exit 1; fi
echo "side-by-side scenario passed on $TARGET"
