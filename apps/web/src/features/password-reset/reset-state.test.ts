import { describe, expect, test } from "bun:test";
import { resetLinkState, tokenOutcome } from "./reset-state";

describe("resetLinkState", () => {
	test("waits for the check", () => {
		expect(
			resetLinkState({ pending: true, failed: false, code: undefined }),
		).toBe("checking");
	});
	test("a link the server rejects is dead", () => {
		expect(
			resetLinkState({
				pending: false,
				failed: true,
				code: "invalid_reset_token",
			}),
		).toBe("dead");
	});
	test("a failed check that is not the link's fault keeps the form", () => {
		expect(
			resetLinkState({ pending: false, failed: true, code: undefined }),
		).toBe("ok");
		expect(
			resetLinkState({ pending: false, failed: false, code: undefined }),
		).toBe("ok");
	});
});

describe("tokenOutcome", () => {
	test("says nothing when the account has no tokens", () => {
		expect(tokenOutcome({ revokedTokens: 0, keptTokens: [] })).toEqual({
			revoked: null,
			kept: null,
		});
	});
	test("names the tokens that still work and where to revoke them", () => {
		const { kept } = tokenOutcome({
			revokedTokens: 0,
			keptTokens: [{ id: "a" }, { id: "b" }],
		});
		expect(kept).toContain("2 API tokens still work");
		expect(kept).toContain("Account settings → API tokens");
		expect(
			tokenOutcome({ revokedTokens: 0, keptTokens: [{ id: "a" }] }).kept,
		).toContain("1 API token still works");
	});
	test("counts the revoked ones", () => {
		expect(tokenOutcome({ revokedTokens: 3, keptTokens: [] }).revoked).toBe(
			"3 API tokens revoked.",
		);
	});
});
