import { defineConfig } from "@playwright/test";
import { AUTH_FILE } from "./e2e/support/env";

// The flows drive a real stack: a control plane serving the built web app, a
// Postgres database and an enrolled agent on a Docker daemon. `make test-e2e`
// (tests/e2e/run.sh) builds and starts all of that and passes its addresses in
// E2E_* variables (see e2e/support/env.ts); with E2E_BASE_URL set by hand the
// same specs run against any stack (`make dev` plus an agent, defaults below).
//
// One worker, in file order: the flows share one control plane, one agent and
// one Docker daemon, and 01-setup creates the account and the server the rest
// sign in to. Each flow creates its own project and deletes it again.
export default defineConfig({
	testDir: "./e2e",
	timeout: 240_000,
	expect: { timeout: 20_000 },
	// One retry in CI: a flow that only passes on retry is reported as flaky in
	// the HTML report (kept with its trace), not hidden.
	retries: process.env.CI ? 1 : 0,
	forbidOnly: !!process.env.CI,
	workers: 1,
	fullyParallel: false,
	reporter: process.env.CI
		? [["list"], ["html", { open: "never" }]]
		: [["list"]],
	use: {
		baseURL: process.env.E2E_BASE_URL ?? "http://localhost:5173",
		// The agent's Traefik serves a self-signed certificate for services with a
		// domain; the flows open those pages in the browser.
		ignoreHTTPSErrors: true,
		actionTimeout: 20_000,
		trace: "retain-on-failure",
		screenshot: "only-on-failure",
	},
	projects: [
		// First run: account, sign-in and the agent's enrollment. Every other
		// project depends on it and starts signed in as that account.
		{ name: "setup", testMatch: /01-setup\.spec\.ts/ },
		{
			name: "flows",
			testMatch: /\d\d-.*\.spec\.ts/,
			testIgnore: /01-setup\.spec\.ts/,
			dependencies: ["setup"],
			use: { storageState: AUTH_FILE },
		},
		// The dither catalogue exists only in the Vite dev server (the route is a
		// 404 in a production build), so it runs on request:
		// E2E_DEV_SERVER=1 bunx playwright test --project dither
		...(process.env.E2E_DEV_SERVER === "1"
			? [{ name: "dither", testMatch: /dither-states\.spec\.ts/ }]
			: []),
	],
});
