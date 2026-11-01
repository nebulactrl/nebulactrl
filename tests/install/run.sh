#!/usr/bin/env bash
# Installer test harness: runs deploy/install/install.sh for real in disposable
# systemd machines (OrbStack: orbctl) and checks what it did.
#
#   tests/install/run.sh                       # debian12, ubuntu2404 and rocky9
#   (also: debian13, ubuntu2204, ubuntu2604, alma9)
#   tests/install/run.sh debian12              # one target
#   tests/install/run.sh --keep rocky9         # leave the machine for inspection
#   tests/install/run.sh --archive <file> ...  # reuse a `docker save` archive
#
# Per target (see tests/install/README.md):
#   1. fresh install (domain-less, IP mode), then assert.sh fresh
#   2. re-run: nothing recreated, secrets and .env unchanged (idempotency)
#   3. first-run: setup token, admin account, the local agent enrolls
#   4. switch to a domain: the dashboard is served through the host proxy
#   5. deploy an app on the control-plane host; "Accept workloads" off refuses
#   6. uninstall (keeps data), then uninstall --purge
# The machine is deleted at the end unless --keep is given. The image is built
# from the working tree once (docker build) and loaded into each machine.
set -Eeuo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
HERE=$ROOT/tests/install
IMAGE=${NC_TEST_IMAGE:-nebulactrl:instlocal}
ARCHIVE=''
KEEP=0
TARGETS=()
# OrbStack mounts the macOS home directory into every machine at the same path,
# so files under it need no copying.
LOGDIR=${NC_TEST_LOGDIR:-$HOME/.nc-inst-tmp}

while [ $# -gt 0 ]; do
	case "$1" in
	--keep) KEEP=1 ;;
	--archive) shift; ARCHIVE=$1 ;;
	-h | --help) sed -n '2,21p' "$0"; exit 0 ;;
	*) TARGETS+=("$1") ;;
	esac
	shift
done
[ ${#TARGETS[@]} -gt 0 ] || TARGETS=(debian12 ubuntu2404 rocky9)
command -v orbctl >/dev/null || { echo "orbctl (OrbStack) is required" >&2; exit 2; }
mkdir -p "$LOGDIR"

distro_of() {
	case "$1" in
	debian12) echo debian:bookworm ;;
	debian13) echo debian:trixie ;;
	ubuntu2204) echo ubuntu:jammy ;;
	ubuntu2404) echo ubuntu:noble ;;
	ubuntu2604) echo ubuntu:resolute ;;
	rocky9) echo rocky:9 ;;
	alma9) echo alma:9 ;;
	*) echo "unknown target $1" >&2; return 1 ;;
	esac
}

say() { printf '\n\033[1m### %s\033[0m\n' "$*"; }
FAILED=()

# ---- the image -----------------------------------------------------------------
if [ -z "$ARCHIVE" ]; then
	say "docker build $IMAGE"
	(cd "$ROOT" && docker build -q -t "$IMAGE" --build-arg VERSION=install-test .) >/dev/null
	ARCHIVE=$LOGDIR/image.tar
	docker save -o "$ARCHIVE" "$IMAGE"
fi
(cd "$ROOT" && go run ./tests/install/assemble) >"$LOGDIR/install-assembled.sh"
bash -n "$LOGDIR/install-assembled.sh"
if command -v shellcheck >/dev/null; then
	( shopt -s nullglob; shellcheck -s bash -S error "$ROOT/deploy/install/install.sh" "$ROOT/deploy/install/uninstall.sh" "$ROOT"/deploy/install/lib/*.sh ) || { echo "shellcheck errors"; exit 1; }
fi

# ---- one target ------------------------------------------------------------------
run_target() {
	local t=$1 name=nc-inst-$1 log=$LOGDIR/$1.log
	local distro; distro=$(distro_of "$t")
	say "$t ($distro) on machine $name; log $log"
	: >"$log"
	orbctl delete -f "$name" >/dev/null 2>&1 || true
	orbctl create "$distro" "$name" >>"$log" 2>&1
	# shellcheck disable=SC2329
	cleanup() { [ "$KEEP" = 1 ] || orbctl delete -f "$name" >/dev/null 2>&1 || true; }
	rx() { orbctl run -m "$name" -u root "$@"; }
	rx true || { echo "machine did not start"; cleanup; return 1; }
	local ip; ip=$(rx bash -c "ip -4 -o addr show scope global | awk '{print \$4}' | cut -d/ -f1 | head -1")
	echo "machine address $ip" | tee -a "$log"


	local common=(--image "$IMAGE" --image-archive $ARCHIVE --public-ip "$ip" --no-hardening)
	[ "${NC_TEST_HARDENING:-0}" = 1 ] && common=(--image "$IMAGE" --image-archive $ARCHIVE --public-ip "$ip")
	local rc=0

	phase() { # phase <title> <command...>
		local title=$1; shift
		printf '\n--- %s ---\n' "$title" | tee -a "$log"
		if "$@" >>"$log" 2>&1; then echo "    ok   $title"; else echo "    FAIL $title (see $log)"; rc=1; fi
	}

	rx bash -c 'find /etc/ssh -name "sshd_config*" -type f -exec sha256sum {} + 2>/dev/null | sort -k2 > /tmp/ssh.before; true' >>"$log" 2>&1 || true

	rx bash -c 'for u in ssh sshd ssh.socket; do echo $u=$(systemctl is-active $u 2>&1 | head -1); done > /tmp/ssh.state' >>"$log" 2>&1 || true
	# 1. Fresh install through the assembled script (what `curl | bash` runs).
	phase "install (assembled script, piped like curl | bash)" \
		bash -c "set -o pipefail; orbctl run -m $name -u root bash -c 'cat $LOGDIR/install-assembled.sh | bash -s -- ${common[*]}' 2>&1 | tee $LOGDIR/$t.install.out"
	rx cat /opt/nebulactrl/.env >"$LOGDIR/$t.env" 2>/dev/null || true
	phase "installer output has no secret" bash -c "
		for k in POSTGRES_PASSWORD NEBULACTRL_SECRET_KEY NEBULACTRL_LOCAL_ENROLL_TOKEN; do
			v=\$(grep -m1 \"^\$k=\" $LOGDIR/$t.env | cut -d= -f2-)
			[ -n \"\$v\" ] || { echo \"\$k empty\"; exit 1; }
			if grep -qF -- \"\$v\" $LOGDIR/$t.install.out; then echo \"\$k leaked into the installer output\"; exit 1; fi
		done"
	phase "assert fresh" rx bash $HERE/assert.sh fresh "$ip"
	if [ "${NC_TEST_HARDENING:-0}" = 1 ]; then
		phase "hardening ran and the index rose" bash -c "
			b=\$(grep -m1 'Hardening index before:' $LOGDIR/$t.install.out | awk '{print \$NF}')
			a=\$(grep -m1 'Hardening index after:' $LOGDIR/$t.install.out | awk '{print \$4}')
			echo \"before=\$b after=\$a\"; [ -n \"\$b\" ] && [ -n \"\$a\" ] && [ \"\$a\" -gt \"\$b\" ]"
		phase "assert hardened" rx bash $HERE/assert.sh hardened "$ip"
	fi
	rx bash -c 'docker ps -q | sort > /tmp/ids.before; cp /opt/nebulactrl/.env /tmp/env.before; docker inspect -f "{{.Id}}" nebulactrl-nebulactrl-1 nebulactrl-postgres-1 > /tmp/cids.before' >>"$log" 2>&1

	# 2. Re-run from the checkout layout: idempotent, nothing recreated.
	phase "re-run (checkout layout)" rx bash $ROOT/deploy/install/install.sh "${common[@]}"
	phase "assert re-run" rx bash $HERE/assert.sh rerun "$ip"

	# 3. First run: setup token, admin, local server.
	phase "assert first run" rx bash $HERE/assert.sh firstrun "$ip"

	# 4. Domain mode through the host proxy.
	phase "switch to a domain" rx bash $ROOT/deploy/install/install.sh "${common[@]}" --domain nebula.test --email ops@example.com
	phase "assert domain" rx bash $HERE/assert.sh domain "$ip"

	# 5. Apps on the control-plane host.
	phase "assert apps on the control-plane host" rx bash $HERE/assert.sh apps "$ip"

	# 6. Uninstall.
	phase "uninstall (keeps data)" rx bash /opt/nebulactrl/uninstall.sh --yes --revert-host
	phase "assert uninstalled" rx bash $HERE/assert.sh uninstalled "$ip"
	phase "reinstall over the kept data" rx bash $ROOT/deploy/install/install.sh "${common[@]}" --domain nebula.test --email ops@example.com
	phase "assert reinstall" rx bash $HERE/assert.sh reinstalled "$ip"
	phase "uninstall --purge --remove-workloads" rx bash /opt/nebulactrl/uninstall.sh --yes --purge --remove-workloads --remove-backups
	phase "assert purged" rx bash $HERE/assert.sh purged "$ip"
	phase "ssh configuration untouched" rx bash -c 'find /etc/ssh -name "sshd_config*" -type f -exec sha256sum {} + 2>/dev/null | sort -k2 | diff /tmp/ssh.before -'

	cleanup
	return $rc
}

for t in "${TARGETS[@]}"; do
	if ! run_target "$t"; then FAILED+=("$t"); fi
done
say "summary"
if [ ${#FAILED[@]} -gt 0 ]; then echo "FAILED: ${FAILED[*]} (logs in $LOGDIR)"; exit 1; fi
echo "all targets passed: ${TARGETS[*]}"
