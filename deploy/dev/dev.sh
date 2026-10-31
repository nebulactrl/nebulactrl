#!/usr/bin/env bash
# Runs the control plane (Go) and the web dev server (Vite) together.
# Invoked by `make dev`. Ctrl-C stops both; if either exits, the other is stopped.
set -u

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

if [ ! -f .env ]; then
	echo "error: .env not found. Run 'make setup' first (it creates one from .env.example)." >&2
	exit 1
fi

# Export everything in .env into the environment of both children. The file is
# sourced by the shell (not parsed by make), so quoting works as in any dotenv.
set -a
# shellcheck disable=SC1091
. ./.env
set +a

if [ -z "${NEBULACTRL_SECRET_KEY:-}" ]; then
	echo "error: NEBULACTRL_SECRET_KEY is empty in .env. Run 'make keygen' and paste the output." >&2
	exit 1
fi

mkdir -p bin
go build -o bin/nebulactrl-server ./cmd/server || exit 1

# Job control gives every background job its own process group, so we can stop
# a job together with the children it spawned (bun -> vite) using kill -- -PGID.
set -m

bin/nebulactrl-server &
server=$!
(cd apps/web && exec bun run dev) &
web=$!

stop() {
	trap - INT TERM
	kill -TERM -- "-$server" 2>/dev/null
	kill -TERM -- "-$web" 2>/dev/null
	wait 2>/dev/null
}
trap 'stop; exit 0' INT TERM

echo "control plane: http://localhost:${NEBULACTRL_LISTEN#:}   web: http://localhost:5173 (proxies /api, /agent, /mcp)"

while kill -0 "$server" 2>/dev/null && kill -0 "$web" 2>/dev/null; do
	sleep 1
done

stop
exit 1
