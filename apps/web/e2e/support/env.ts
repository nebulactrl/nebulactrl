// What the specs know about the stack they run against. tests/e2e/run.sh sets
// all of these for the stack it starts; the defaults fit `make dev` with the
// agent1 dev profile (docs in tests/README.md, "Tier 4").

export const e2e = {
	email: process.env.E2E_EMAIL ?? "admin@example.com",
	password: process.env.E2E_PASSWORD ?? "correct-horse-battery",
	/** NEBULACTRL_SETUP_TOKEN of a fresh control plane (empty: none required). */
	setupToken: process.env.E2E_SETUP_TOKEN ?? "",
	/** Name of the server the services run on; created by 01-setup when missing. */
	server: process.env.E2E_SERVER ?? "agent1",
	/** The https port of the agent's Traefik on 127.0.0.1 (dev agent1: 8443). */
	ingressPort: Number(process.env.E2E_INGRESS_PORT ?? "8443"),
	/** tests/e2e/run.sh: `run.sh agent TOKEN` starts the agent for a new server. */
	stack: process.env.E2E_STACK ?? "",
};

/** The signed-in browser state 01-setup writes for the other flows (cleaned with test-results). */
export const AUTH_FILE = "test-results/.auth/admin.json";

/** A small HTTP server that prints the request and `Name: $WHOAMI_NAME`; pinned so runs are alike. */
export const WHOAMI = "traefik/whoami:v1.11.0";

/** nginx with a shell (the terminal needs one) that logs every request to stdout; the agent pulls the same tag for its pages server. */
export const NGINX = "nginx:1.30.6-alpine-slim";
