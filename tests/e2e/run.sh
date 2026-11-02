#!/usr/bin/env bash
# Browser e2e tier: brings up a real stack, runs the Playwright flows against it
# and tears everything down again (make test-e2e).
#
#   tests/e2e/run.sh [playwright args]     e.g. tests/e2e/run.sh e2e/02-deploy.spec.ts
#   tests/e2e/run.sh agent TOKEN           internal: start the enrolled agent (flow 1 calls it)
#
# The stack, fresh on every run:
#
#   - Postgres database nebulactrl_e2e_<pid>_<n> (a name of its own per run, so a
#     control plane an earlier run left behind never finds this run's database),
#     created empty on the server that NEBULACTRL_TEST_DATABASE_URL points at (the
#     throwaway one of scripts/dev/local-test.sh, the service container in CI) and
#     dropped again
#   - the control plane, built from this checkout with the built web app embedded
#     (go build -overlay: internal/webui/dist is not touched), on a free port
#   - a docker-in-docker daemon (container nebula-e2e-dind) and one real agent in
#     its network namespace, enrolled against the control plane by the first
#     flow, like deploy/dev's `multi` profile. The agent manages the fixed-name
#     container `nebulactrl-traefik` and the network `nebulactrl-edge`; on a
#     private daemon it can never collide with a dev agent1 or tests/run.sh
#
# Playwright receives E2E_BASE_URL, E2E_INGRESS_PORT (the agent's Traefik, https on
# 127.0.0.1: a service with the domain app.localhost is https://app.localhost:<port>/,
# *.localhost is loopback), E2E_EMAIL / E2E_PASSWORD / E2E_SETUP_TOKEN (first-run
# setup), E2E_SERVER (the server's name) and E2E_STACK (this script, to start the
# agent). Without this script the specs run against a dev stack, see
# apps/web/e2e/support/env.ts.
#
# Other knobs: E2E_SKIP_WEB_BUILD=1 reuses apps/web/dist; E2E_PORT / E2E_INGRESS_PORT
# pin the ports; E2E_KEEP=1 leaves the stack running (remove it with
# `tests/e2e/run.sh down`).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

DIND=nebula-e2e-dind
# The multi-arch index digest of docker:29-dind, pinned like the other images of the repo.
DIND_IMAGE=docker:29-dind@sha256:9e79b879b177fbd72c34376a3dd3d91747da5db49e1f560f76aea59561471977
AGENT=nebula-e2e-agent
AGENT_IMAGE=nebula-e2e-agent:dev
DB_PREFIX=nebulactrl_e2e
DB_NAME="${DB_PREFIX}_$$_$RANDOM"
LOCK="${TMPDIR:-/tmp}/nebula-e2e.lock"

log() { echo "e2e: $*" >&2; }

free_port() {
	local p
	while :; do
		p=$((20000 + RANDOM % 20000))
		if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
			echo "$p"
			return
		fi
	done
}

dind_exec() { docker exec "$DIND" docker -H tcp://127.0.0.1:2375 "$@"; }

# ---------------------------------------------------------------------------
# agent TOKEN: the control plane's address and the agent image come from the
# state file the run wrote.
# ---------------------------------------------------------------------------
start_agent() {
	local token="$1"
	[ -n "${E2E_STATE:-}" ] && [ -f "$E2E_STATE" ] || { log "no running stack (E2E_STATE unset)"; exit 2; }
	# shellcheck disable=SC1090
	. "$E2E_STATE"
	docker rm -f "$AGENT" >/dev/null 2>&1 || true
	docker run -d --name "$AGENT" --network "container:$DIND" \
		-e DOCKER_HOST=tcp://127.0.0.1:2375 \
		-e NEBULACTRL_URL="ws://host.docker.internal:$CP_PORT" \
		-e NEBULACTRL_ENROLL_TOKEN="$token" \
		-e NEBULACTRL_STATE_DIR=/var/lib/nebulactrl-agent \
		-e NEBULACTRL_PROXY_HTTP_PORT=80 \
		-e NEBULACTRL_PROXY_HTTPS_PORT=443 \
		-e NEBULACTRL_INSECURE_DEV=true \
		-e NEBULACTRL_UPTIME_INTERVAL=5s \
		"$AGENT_IMAGE" >/dev/null
}

stop_stack() {
	docker rm -fv "$AGENT" "$DIND" >/dev/null 2>&1 || true
	docker rmi -f "$AGENT_IMAGE" >/dev/null 2>&1 || true
	if [ -n "${ADMIN_URL:-}" ]; then
		# From the repo root: the teardown runs in apps/web, where the package is not found.
		(cd "$root" && go run ./tests/e2e/pgdb sweep "$ADMIN_URL" "$DB_PREFIX") >/dev/null 2>&1 || true
	fi
}

case "${1:-}" in
agent)
	[ $# -eq 2 ] || { echo "usage: tests/e2e/run.sh agent TOKEN" >&2; exit 2; }
	start_agent "$2"
	exit 0
	;;
down)
	for d in "${TMPDIR:-/tmp}"/nebula-e2e.??????; do
		[ -f "$d/server.pid" ] || continue
		kill "$(cat "$d/server.pid")" 2>/dev/null || true
		rm -rf "$d"
	done
	ADMIN_URL="${NEBULACTRL_TEST_DATABASE_URL:-}" stop_stack
	exit 0
	;;
esac

command -v docker >/dev/null || { echo "e2e: docker is required" >&2; exit 2; }
command -v bun >/dev/null || { echo "e2e: bun is required" >&2; exit 2; }
command -v curl >/dev/null || { echo "e2e: curl is required" >&2; exit 2; }
ADMIN_URL="${NEBULACTRL_TEST_DATABASE_URL:-}"
[ -n "$ADMIN_URL" ] || { echo "e2e: NEBULACTRL_TEST_DATABASE_URL is required (a database of a Postgres server nebulactrl may create databases on)" >&2; exit 2; }

# One run at a time: the container names are fixed. mkdir is the portable lock;
# a lock whose owner is gone is taken over.
while ! mkdir "$LOCK" 2>/dev/null; do
	owner="$(cat "$LOCK/pid" 2>/dev/null || true)"
	if [ -n "$owner" ] && ! kill -0 "$owner" 2>/dev/null; then
		rm -rf "$LOCK"
		continue
	fi
	log "another e2e run (pid ${owner:-?}) holds the lock; waiting"
	sleep 5
done
echo $$ >"$LOCK/pid"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/nebula-e2e.XXXXXX")"
SERVER_PID=""
ARTIFACTS="$root/apps/web/test-results/stack"

teardown() {
	rc=$?
	trap - EXIT INT TERM
	if [ "${E2E_KEEP:-}" != 1 ] && [ -n "$SERVER_PID" ]; then
		kill "$SERVER_PID" 2>/dev/null || true
		wait "$SERVER_PID" 2>/dev/null || true
	fi
	# Logs of the stack next to the Playwright traces (Playwright empties
	# test-results when it starts, so they are copied afterwards).
	if [ "$rc" -ne 0 ]; then
		mkdir -p "$ARTIFACTS"
		cp "$WORK/server.log" "$ARTIFACTS/control-plane.log" 2>/dev/null || true
		docker logs "$AGENT" >"$ARTIFACTS/agent.log" 2>&1 || true
		docker logs "$DIND" >"$ARTIFACTS/dind.log" 2>&1 || true
	fi
	if [ "${E2E_KEEP:-}" = 1 ]; then
		log "E2E_KEEP=1: stack left running ($WORK; state file $WORK/state.env); remove it with tests/e2e/run.sh down"
	else
		stop_stack
		rm -rf "$WORK"
	fi
	rm -rf "$LOCK"
	exit "$rc"
}
trap teardown EXIT INT TERM

# A crashed earlier run leaves its containers behind.
stop_stack

# ---------------------------------------------------------------------------
# Builds
# ---------------------------------------------------------------------------
if [ "${E2E_SKIP_WEB_BUILD:-}" != 1 ]; then
	log "building the web app"
	(cd apps/web && bun run build --outDir "$WORK/web" --emptyOutDir >"$WORK/web-build.log" 2>&1) \
		|| { cat "$WORK/web-build.log" >&2; exit 1; }
else
	mkdir -p "$WORK/web"
	cp -R apps/web/dist/. "$WORK/web/"
fi

log "building the control plane and the agent"
# The built web app replaces internal/webui/dist for this build only (go:embed
# honours -overlay): files of the old build are removed, the new ones added.
(cd "$WORK/web" && find . -type f | sed 's|^\./||' | sort) >"$WORK/web.files"
(cd internal/webui/dist && find . -type f ! -name .keep | sed 's|^\./||' | sort) >"$WORK/dist.files"
{
	printf '{"Replace":{'
	sep=""
	while IFS= read -r f; do
		printf '%s"%s":"%s"' "$sep" "$root/internal/webui/dist/$f" "$WORK/web/$f"
		sep=,
	done <"$WORK/web.files"
	while IFS= read -r f; do
		printf '%s"%s":""' "$sep" "$root/internal/webui/dist/$f"
		sep=,
	done < <(comm -13 "$WORK/web.files" "$WORK/dist.files")
	printf '}}\n'
} >"$WORK/overlay.json"
[ -s "$WORK/web/index.html" ] || { echo "e2e: the web build has no index.html" >&2; exit 1; }
go build -overlay "$WORK/overlay.json" -trimpath -o "$WORK/nebulactrl-server" ./cmd/server

case "$(docker info --format '{{.Architecture}}')" in
aarch64 | arm64) arch=arm64 ;;
x86_64 | amd64) arch=amd64 ;;
*) echo "e2e: unsupported Docker architecture" >&2; exit 2 ;;
esac
mkdir -p "$WORK/agent"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$WORK/agent/nebulactrl-agent" ./cmd/agent
# The same final image stage as deploy/agent/Dockerfile.dev (the Docker CLI
# the agent shells out to), with the binary built above instead of in Docker.
base="$(grep -oE '^FROM docker:29-cli@sha256:[0-9a-f]{64}' deploy/agent/Dockerfile.dev | cut -d' ' -f2)"
[ -n "$base" ] || { echo "e2e: no pinned docker:29-cli base in deploy/agent/Dockerfile.dev" >&2; exit 1; }
cat >"$WORK/agent/Dockerfile" <<EOF
FROM $base
RUN apk add --no-cache git ca-certificates
COPY nebulactrl-agent /usr/local/bin/nebulactrl-agent
ENTRYPOINT ["/usr/local/bin/nebulactrl-agent"]
EOF
docker build -q -t "$AGENT_IMAGE" "$WORK/agent" >/dev/null

# ---------------------------------------------------------------------------
# The stack
# ---------------------------------------------------------------------------
DB_URL="$(go run ./tests/e2e/pgdb create "$ADMIN_URL" "$DB_NAME")"

CP_PORT="${E2E_PORT:-$(free_port)}"
INGRESS_PORT="${E2E_INGRESS_PORT:-$(free_port)}"
STATUS_PORT="$(free_port)"
SECRET_KEY="$("$WORK/nebulactrl-server" keygen)"
SETUP_TOKEN="e2e-setup-$RANDOM$RANDOM"
EMAIL="admin@example.com"
PASSWORD="correct-horse-battery"

log "starting the docker-in-docker daemon (the agent's Traefik, https, published on 127.0.0.1:$INGRESS_PORT)"
docker run -d --name "$DIND" --privileged --add-host host.docker.internal:host-gateway \
	-e DOCKER_TLS_CERTDIR= -p "127.0.0.1:$INGRESS_PORT:443" "$DIND_IMAGE" >/dev/null
for _ in $(seq 1 60); do
	dind_exec info >/dev/null 2>&1 && break
	sleep 1
done
dind_exec info >/dev/null 2>&1 || { echo "e2e: the docker-in-docker daemon did not come up" >&2; exit 1; }

log "starting the control plane on 127.0.0.1:$CP_PORT"
NEBULACTRL_DATABASE_URL="$DB_URL" \
	NEBULACTRL_SECRET_KEY="$SECRET_KEY" \
	NEBULACTRL_LISTEN=":$CP_PORT" \
	NEBULACTRL_STATUS_LISTEN="127.0.0.1:$STATUS_PORT" \
	NEBULACTRL_PUBLIC_URL="http://127.0.0.1:$CP_PORT" \
	NEBULACTRL_SETUP_TOKEN="$SETUP_TOKEN" \
	NEBULACTRL_UPDATE_CHECK=false \
	NEBULACTRL_LOG_LEVEL=info \
	NEBULACTRL_UPTIME_INTERVAL=5s \
	"$WORK/nebulactrl-server" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
echo "$SERVER_PID" >"$WORK/server.pid"
for _ in $(seq 1 60); do
	curl -fsS "http://127.0.0.1:$CP_PORT/api/v1/setup" >/dev/null 2>&1 && break
	kill -0 "$SERVER_PID" 2>/dev/null || { cat "$WORK/server.log" >&2; echo "e2e: the control plane exited" >&2; exit 1; }
	sleep 1
done
curl -fsS "http://127.0.0.1:$CP_PORT/api/v1/setup" >/dev/null || { cat "$WORK/server.log" >&2; echo "e2e: the control plane did not come up" >&2; exit 1; }

# Everything Playwright needs, also usable by hand against a stack kept with
# E2E_KEEP=1: set -a; . STATE_FILE; set +a; cd apps/web && bunx playwright test
cat >"$WORK/state.env" <<EOF
CP_PORT=$CP_PORT
AGENT_IMAGE=$AGENT_IMAGE
E2E_STATE=$WORK/state.env
E2E_STACK=$root/tests/e2e/run.sh
E2E_BASE_URL=http://127.0.0.1:$CP_PORT
E2E_INGRESS_PORT=$INGRESS_PORT
E2E_EMAIL=$EMAIL
E2E_PASSWORD=$PASSWORD
E2E_SETUP_TOKEN=$SETUP_TOKEN
E2E_SERVER=e2e-agent
EOF

# Backups, uptime and the like are product features the flows exercise as they
# are; nothing about the stack is mocked.
set -a
# shellcheck disable=SC1090
. "$WORK/state.env"
set +a

log "running Playwright"
cd apps/web
bunx playwright test "$@"
