#!/bin/sh
# Runs the Docker-backed integration tests: `tests/run.sh smoke|full [go test args]`
# (make test-smoke, make test-full).
#
#   smoke  the core scenarios (enroll, deploy, rolling, health, rollback, secrets,
#          IDOR, network isolation) on one shared control plane and agent; ~2 min
#   full   everything, including templates, Railpack, backups, reconnect/partition
#          and the proxy tests; exclusive tests run serially, the rest in parallel
#
# The harness agent manages the fixed-name container `nebulactrl-traefik` and the
# `nebulactrl-edge` network, exactly like every real agent. The dev stack's
# agent1 uses the same names on the same Docker daemon, so two agents would fight
# over one Traefik. This script therefore stops agent1 for the run, lets the
# harness take over the proxy container (NEBULACTRL_TEST_PROXY_TAKEOVER=1), and
# brings agent1 back afterwards (its own proxy manager recreates Traefik with the
# dev ports on start).
#
# `full --shard I/N` runs only the I-th of N slices of the top-level tests (the
# CI matrix of .github/workflows/full.yml uses 4). The slices are balanced by the
# recorded durations in tests/integration/durations.json (tools/shardtests), cover
# every test exactly once, and cannot be combined with -run. Without --shard
# everything runs.
#
# Further arguments are passed to `go test` (e.g. -run TestRollback -v).
#
# Product timers the tests would otherwise wait out in real time are shortened by
# the harness (tests/integration/harness_timers_test.go); the same
# NEBULACTRL_* variable in the environment of this script overrides the short value.
set -eu
cd "$(dirname "$0")/.."

TIER="${1:-}"
case "$TIER" in
smoke | full) shift ;;
*)
	echo "usage: tests/run.sh smoke|full [--shard I/N] [go test args]" >&2
	exit 2
	;;
esac
export NEBULACTRL_TEST_TIER="$TIER"

SHARD=""
case "${1:-}" in
--shard)
	[ $# -ge 2 ] || { echo "tests: --shard needs I/N (e.g. --shard 1/4)" >&2; exit 2; }
	SHARD="$2"
	shift 2
	;;
--shard=*)
	SHARD="${1#--shard=}"
	shift
	;;
esac

# The shard's tests as a -run regex, computed before anything is stopped or
# locked: a bad shard fails fast. `go test -list` needs neither a database nor
# Docker (TestMain returns early for it).
SHARD_RUN=""
if [ -n "$SHARD" ]; then
	for a in "$@"; do
		case "$a" in
		-run | -run=* | -test.run | -test.run=*)
			echo "tests: --shard selects the tests itself; do not pass -run" >&2
			exit 2
			;;
		esac
	done
	SHARD_RUN="$(go test -count=1 -tags integration -list '.*' ./tests/integration/ \
		| go run ./tools/shardtests -shard "$SHARD" -durations tests/integration/durations.json -summary)" || exit 2
	echo "tests: shard $SHARD runs $SHARD_RUN"
fi

# Two runs share one Docker daemon, the fixed-name Traefik container and the
# dev agent1; they must not overlap. mkdir is the portable atomic lock (macOS
# has no flock(1)); a lock whose owner is gone is taken over.
LOCKDIR="${TMPDIR:-/tmp}/nebulactrl-integration.lock"
while ! mkdir "$LOCKDIR" 2>/dev/null; do
	owner="$(cat "$LOCKDIR/pid" 2>/dev/null || true)"
	if [ -n "$owner" ] && ! kill -0 "$owner" 2>/dev/null; then
		rm -rf "$LOCKDIR"
		continue
	fi
	echo "tests: another integration run (pid ${owner:-?}) holds the lock; waiting"
	sleep 5
done
echo $$ > "$LOCKDIR/pid"
release_lock() { rm -rf "$LOCKDIR"; }
trap release_lock EXIT INT TERM

_caller_multi="${NEBULACTRL_TEST_MULTI-}"
_caller_db="${NEBULACTRL_TEST_DATABASE_URL-}"
if [ -f .env ]; then
	set -a
	# shellcheck disable=SC1091
	. ./.env
	set +a
fi
# Values given on the command line win over .env.
[ -z "$_caller_multi" ] || NEBULACTRL_TEST_MULTI="$_caller_multi"
[ -z "$_caller_db" ] || NEBULACTRL_TEST_DATABASE_URL="$_caller_db"
export NEBULACTRL_TEST_MULTI NEBULACTRL_TEST_DATABASE_URL

COMPOSE="docker compose -f deploy/dev/docker-compose.yml"
[ -f .env ] && COMPOSE="docker compose --env-file .env -f deploy/dev/docker-compose.yml"

AGENT1_WAS_RUNNING=""
if [ -n "$($COMPOSE --profile agent ps --status running -q agent1 2>/dev/null || true)" ]; then
	AGENT1_WAS_RUNNING=1
	echo "tests: stopping dev agent1 for the run (it is restored afterwards)"
	$COMPOSE --profile agent stop agent1 >/dev/null
	docker rm -f nebulactrl-traefik >/dev/null 2>&1 || true
	docker rm -f nebulactrl-pages >/dev/null 2>&1 || true
fi

restore() {
	rc=$?
	if [ -n "$AGENT1_WAS_RUNNING" ]; then
		echo "tests: restoring dev agent1"
		$COMPOSE --profile agent up -d agent1 >/dev/null 2>&1 \
			|| echo "tests: WARNING could not restart agent1; run: $COMPOSE --profile agent up -d agent1" >&2
	fi
	release_lock
	exit $rc
}
trap restore EXIT INT TERM

if [ -n "$AGENT1_WAS_RUNNING" ]; then
	export NEBULACTRL_TEST_PROXY_TAKEOVER=1
fi

# Smoke is a few minutes; full runs templates, builds and backups. -parallel
# caps the shared-harness group (about 20 tests); Docker, not the CPU, is the limit.
case "$TIER" in
full) TIMEOUT=60m ;;
*) TIMEOUT=10m ;;
esac
if [ -n "$SHARD_RUN" ]; then
	go test -count=1 -tags integration -timeout "$TIMEOUT" -parallel 16 -run "$SHARD_RUN" ./tests/integration/... "$@"
else
	go test -count=1 -tags integration -timeout "$TIMEOUT" -parallel 16 ./tests/integration/... "$@"
fi
