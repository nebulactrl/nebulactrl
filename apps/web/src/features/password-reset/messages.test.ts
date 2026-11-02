import { describe, expect, test } from "bun:test";
import {
	DEAD_LINK_TEXT,
	forgotFailure,
	passwordProblem,
	resetFailure,
} from "./messages";

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
	test("a dead link is one text for the page and the submit, and says what to do", () => {
		const text = resetFailure("invalid_reset_token", 400, "x");
		expect(text).toBe(DEAD_LINK_TEXT);
		expect(text).toContain("Ask for a new one");
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
