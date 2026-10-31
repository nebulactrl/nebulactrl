import { defineConfig } from "@playwright/test";

// The smoke suite drives a running stack (`make dev` plus the agent1 dev
// profile); it does not start servers itself.
export default defineConfig({
	testDir: "./e2e",
	timeout: 180_000,
	retries: 0,
	reporter: [["list"]],
	use: {
		baseURL: process.env.E2E_BASE_URL ?? "http://localhost:5173",
		actionTimeout: 20_000,
		trace: "retain-on-failure",
	},
});
