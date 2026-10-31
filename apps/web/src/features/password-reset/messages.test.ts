import { describe, expect, test } from "bun:test";
import {
	ASK_ADMIN_NOTE,
	DEAD_LINK_TEXT,
	forgotFailure,
	passwordProblem,
	RESET_COMMAND,
	resetFailure,
	UNAVAILABLE_NOTE,
} from "./messages";

describe("the reset hint for installs without email", () => {
	test("names the host command a solo admin runs", () => {
		expect(RESET_COMMAND).toBe("nebulactrl-server reset-password <email>");
	});
	test("does not tell people to ask for something only some can give", () => {
		expect(ASK_ADMIN_NOTE).not.toContain("team admin");
		expect(UNAVAILABLE_NOTE).not.toContain(ASK_ADMIN_NOTE);
	});
	test("a dead link is worded once for the page and the submit", () => {
		expect(resetFailure("invalid_reset_token", 400, "x")).toBe(DEAD_LINK_TEXT);
	});
});

describe("passwordProblem", () => {
	test("needs ten characters", () => {
		expect(passwordProblem("short", "")).toBe("At least 10 characters.");
		expect(passwordProblem("long enough pass", "")).toBeUndefined();
	});
	test("flags a confirmation that differs, but not an empty one", () => {
		expect(passwordProblem("long enough pass", "long enough pasS")).toBe(
			"The two passwords do not match.",
		);
		expect(passwordProblem("long enough pass", "")).toBeUndefined();
		expect(passwordProblem("long enough pass", "long enough pass")).toBe(
			undefined,
		);
	});
});

describe("resetFailure", () => {
	test("names a dead link and says what to do", () => {
		expect(resetFailure("invalid_reset_token", 400, "x")).toContain(
			"Ask for a new one",
		);
	});
	test("rate limits and the rest", () => {
		expect(resetFailure(undefined, 429, "x")).toContain("Wait a minute");
		expect(resetFailure(undefined, 500, "boom")).toBe("boom");
	});
});

describe("forgotFailure", () => {
	test("a limited request is not blamed on the address", () => {
		expect(forgotFailure(429, "x")).toContain("Too many reset requests");
		expect(forgotFailure(500, "boom")).toBe("boom");
	});
});
