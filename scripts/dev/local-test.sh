#!/usr/bin/env bash
# Runs a command (tests, coverage) on this machine without overloading it: at
# most NEBULA_LOCAL_SLOTS (default 2) commands run at once machine-wide, each at
# low priority, Go with at most two packages in parallel, against a throwaway
# in-memory Postgres (container nebulactrl-test-postgres, port 55434) so the
# dev database is never touched. Every docker call has a deadline: a stalled
# Docker engine fails the run instead of holding a slot forever.
#
#   scripts/dev/local-test.sh -- go test ./internal/services/...
#   scripts/dev/local-test.sh -- bash -c 'cd apps/web && bun test src/features/logs'
set -euo pipefail

[[ "${1:-}" == "--" ]] && shift
if [[ $# -eq 0 ]]; then
	echo "usage: $0 -- COMMAND..." >&2
	exit 2
fi

root="$(git rev-parse --show-toplevel)"
slots="${NEBULA_LOCAL_SLOTS:-2}"
lockdir=/tmp/nebula-local-test-slots
mkdir -p "$lockdir"

# Take a free slot; a slot whose owner died is free again.
slot=""
while [[ -z "$slot" ]]; do
	for i in $(seq 1 "$slots"); do
		if mkdir "$lockdir/$i" 2>/dev/null; then
			echo $$ >"$lockdir/$i/pid"
			slot=$i
			break
		fi
		owner="$(cat "$lockdir/$i/pid" 2>/dev/null || true)"
		if [[ -n "$owner" ]] && ! kill -0 "$owner" 2>/dev/null; then rm -rf "${lockdir:?}/$i"; fi
	done
	[[ -n "$slot" ]] || sleep 2
done
trap 'rm -rf "${lockdir:?}/$slot"' EXIT

fail() {
	echo "local-test: $*" >&2
	exit 1
}
dk() { timeout 30 docker "$@"; }

# The throwaway test database: started once, in memory, durability off. One
# wrapper at a time sets it up, so two never replace each other's container.
until mkdir "$lockdir/db" 2>/dev/null; do
	owner="$(cat "$lockdir/db/pid" 2>/dev/null || true)"
	if [[ -n "$owner" ]] && ! kill -0 "$owner" 2>/dev/null; then rm -rf "${lockdir:?}/db"; fi
	sleep 1
done
echo $$ >"$lockdir/db/pid"
trap 'rm -rf "${lockdir:?}/$slot"; [[ "$(cat "$lockdir/db/pid" 2>/dev/null)" == $$ ]] && rm -rf "${lockdir:?}/db"' EXIT
running="$(dk inspect -f '{{.State.Running}}' nebulactrl-test-postgres 2>/dev/null)" || [[ $? -ne 124 ]] || fail "docker did not answer in 30s; is the engine stalled?"
if [[ "$running" != true ]]; then
	dk rm -f nebulactrl-test-postgres >/dev/null 2>&1 || true
	dk run -d --name nebulactrl-test-postgres -p 127.0.0.1:55434:5432 \
		--tmpfs /var/lib/postgresql/data:rw,size=4g --shm-size=1g \
		-e POSTGRES_USER=nebulactrl -e POSTGRES_PASSWORD=nebulactrl -e POSTGRES_DB=nebulactrl_test \
		postgres:17 -c fsync=off -c synchronous_commit=off -c full_page_writes=off \
		-c max_connections=300 -c max_locks_per_transaction=256 >/dev/null || fail "could not start the test Postgres"
fi
ready=""
for _ in $(seq 1 30); do
	if dk exec nebulactrl-test-postgres pg_isready -U nebulactrl -d nebulactrl_test >/dev/null 2>&1; then
		ready=1
		break
	fi
	sleep 1
done
[[ -n "$ready" ]] || fail "the test Postgres is not ready"
rm -rf "${lockdir:?}/db"
export NEBULACTRL_TEST_DATABASE_URL="postgres://nebulactrl:nebulactrl@127.0.0.1:55434/nebulactrl_test?sslmode=disable"
export GOFLAGS="${GOFLAGS:-} -p=2"

cd "$root"
nice -n 10 "$@"
