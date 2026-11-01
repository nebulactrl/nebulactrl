import { describe, expect, test } from "bun:test";
import { lockedCredentialKeys } from "./credentials";
import { REVEAL_MAX_MS, secondsLeft, startReveal } from "./reveal";

const NOW = 1_000_000;
const iso = (ms: number) => new Date(ms).toISOString();

describe("reveal lifetime (fake clock)", () => {
	test("lives until the server's expiry", () => {
		const reveal = startReveal(
			{
				key: "DATABASE_URL",
				value: "postgres://x",
				expiresAt: iso(NOW + 30_000),
			},
			NOW,
		);
		expect(reveal.clearAt).toBe(NOW + 30_000);
	});
	test("never outlives the client cap, whatever the server says", () => {
		const reveal = startReveal(
			{ key: "K", value: "v", expiresAt: iso(NOW + 10 * 60_000) },
			NOW,
		);
		expect(reveal.clearAt).toBe(NOW + REVEAL_MAX_MS);
	});
	test("an unreadable expiry falls back to the cap", () => {
		const reveal = startReveal(
			{ key: "K", value: "v", expiresAt: "soon" },
			NOW,
		);
		expect(reveal.clearAt).toBe(NOW + REVEAL_MAX_MS);
	});
	test("the countdown rounds up and stops at zero", () => {
		const reveal = startReveal(
			{ key: "K", value: "v", expiresAt: iso(NOW + 5_000) },
			NOW,
		);
		expect(secondsLeft(reveal, NOW)).toBe(5);
		expect(secondsLeft(reveal, NOW + 4_100)).toBe(1);
		expect(secondsLeft(reveal, NOW + 9_000)).toBe(0);
	});
});

describe("lockedCredentialKeys", () => {
	const view = {
		deployed: true,
		credentialKeys: ["POSTGRES_PASSWORD", "DATABASE_URL"],
	};
	test("locks the credential variables of a deployed database", () => {
		const locked = lockedCredentialKeys(view);
		expect(locked.has("POSTGRES_PASSWORD")).toBe(true);
		expect(locked.has("PGOPTIONS")).toBe(false);
	});
	test("locks nothing before the first deploy or for a non-managed service", () => {
		expect(lockedCredentialKeys({ ...view, deployed: false }).size).toBe(0);
		expect(lockedCredentialKeys(undefined).size).toBe(0);
		expect(
			lockedCredentialKeys({ deployed: true, credentialKeys: null }).size,
		).toBe(0);
	});
});
