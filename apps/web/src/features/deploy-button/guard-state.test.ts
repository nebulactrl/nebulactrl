import { describe, expect, test } from "bun:test";
import {
	canPressKey,
	GUARD_CLOSED,
	type GuardEvent,
	type GuardState,
	guardReducer,
} from "./guard-state";

const run = (events: readonly GuardEvent[], from: GuardState = GUARD_CLOSED) =>
	events.reduce(guardReducer, from);

describe("guardReducer", () => {
	test("lift arms a closed guard", () => {
		expect(run(["lift"])).toEqual({ armed: true, sent: false });
	});

	test("outside, escape, timeout and lower each close a lifted guard", () => {
		for (const event of ["outside", "escape", "timeout", "lower"] as const) {
			expect(run(["lift", event])).toEqual(GUARD_CLOSED);
		}
	});

	test("pressing closes the cover at once and marks the deploy as sent", () => {
		expect(run(["lift", "press"])).toEqual({ armed: false, sent: true });
	});

	test("a press needs a lifted cover", () => {
		expect(run(["press"])).toEqual(GUARD_CLOSED);
		expect(run(["lift", "escape", "press"])).toEqual(GUARD_CLOSED);
	});

	test("an outside press while sent changes nothing", () => {
		const sent = run(["lift", "press"]);
		expect(run(["outside", "escape", "timeout", "lower"], sent)).toEqual(sent);
	});

	test("the guard cannot be lifted again while sent", () => {
		expect(run(["lift", "press", "lift"])).toEqual({
			armed: false,
			sent: true,
		});
	});

	test("resolved clears sent and the guard can be lifted again", () => {
		expect(run(["lift", "press", "resolved"])).toEqual(GUARD_CLOSED);
		expect(run(["lift", "press", "resolved", "lift"])).toEqual({
			armed: true,
			sent: false,
		});
	});

	test("events that change nothing return the same state object", () => {
		expect(guardReducer(GUARD_CLOSED, "outside")).toBe(GUARD_CLOSED);
		expect(guardReducer(GUARD_CLOSED, "resolved")).toBe(GUARD_CLOSED);
	});
});

describe("canPressKey", () => {
	test("a closed guard has no key to press, even for a scripted click", () => {
		expect(canPressKey(GUARD_CLOSED, false)).toBe(false);
	});

	test("a lifted cover over an enabled key can be pressed", () => {
		expect(canPressKey(run(["lift"]), false)).toBe(true);
	});

	test("a disabled key or a deploy in flight cannot be pressed", () => {
		expect(canPressKey(run(["lift"]), true)).toBe(false);
		expect(canPressKey({ armed: true, sent: true }, false)).toBe(false);
	});
});
