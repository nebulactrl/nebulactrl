import { describe, expect, test } from "bun:test";
import {
	EXPIRED_TEXT,
	isNoPendingLogin,
	secondFactorFailure,
} from "./second-factor-state";

describe("secondFactorFailure", () => {
	test("an expired sign-in leaves only Back", () => {
		expect(secondFactorFailure(401, "no_pending_login", "x")).toEqual({
			text: EXPIRED_TEXT,
			expired: true,
		});
	});
	test("a locked sign-in is over too", () => {
		const failure = secondFactorFailure(429, "two_factor_locked", "x");
		expect(failure.expired).toBe(true);
		expect(failure.text).toContain("Wait a few minutes");
	});
	test("a wrong code or a rate limit keeps the form", () => {
		expect(secondFactorFailure(401, undefined, "x").expired).toBe(false);
		expect(secondFactorFailure(401, undefined, "x").text).toContain(
			"not valid",
		);
		expect(secondFactorFailure(429, undefined, "x").expired).toBe(false);
	});
	test("anything else shows the server's words", () => {
		expect(secondFactorFailure(500, undefined, "boom")).toEqual({
			text: "boom",
			expired: false,
		});
	});
});

describe("isNoPendingLogin", () => {
	test("a 401 or the named code means nothing is pending", () => {
		expect(isNoPendingLogin(401, undefined)).toBe(true);
		expect(isNoPendingLogin(400, "no_pending_login")).toBe(true);
	});
	test("a network or server failure does not", () => {
		expect(isNoPendingLogin(500, undefined)).toBe(false);
		expect(isNoPendingLogin(undefined, undefined)).toBe(false);
	});
});
