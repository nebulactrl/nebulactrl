import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { errorOnce, resetLogOnce, warnOnce } from "./dev";

const env = process.env as Record<string, string | undefined>;
const realNodeEnv = env.NODE_ENV;

let warn: ReturnType<typeof spyOn<Console, "warn">>;
let error: ReturnType<typeof spyOn<Console, "error">>;
beforeEach(() => {
	resetLogOnce();
	warn = spyOn(console, "warn").mockImplementation(() => {});
	error = spyOn(console, "error").mockImplementation(() => {});
});
afterEach(() => {
	warn.mockRestore();
	error.mockRestore();
	env.NODE_ENV = realNodeEnv;
	resetLogOnce();
});

describe("warnOnce", () => {
	test("logs a key once, and a different key separately", () => {
		env.NODE_ENV = "development";
		warnOnce("a", "first");
		warnOnce("a", "first again");
		warnOnce("b", "second");
		expect(warn.mock.calls).toEqual([["first"], ["second"]]);
	});

	test("is silent in production builds and does not burn the key", () => {
		env.NODE_ENV = "production";
		warnOnce("a", "hidden");
		expect(warn).not.toHaveBeenCalled();
		env.NODE_ENV = "development";
		warnOnce("a", "shown");
		expect(warn.mock.calls).toEqual([["shown"]]);
	});

	test("treats an unreadable environment as production", () => {
		const descriptor = Object.getOwnPropertyDescriptor(process, "env");
		if (!descriptor?.configurable) return;
		Object.defineProperty(process, "env", {
			configurable: true,
			get() {
				throw new ReferenceError("process is not defined");
			},
		});
		try {
			warnOnce("no-process", "never");
			// Without a readable mode it is treated as production: nothing logs.
			expect(warn).not.toHaveBeenCalled();
		} finally {
			Object.defineProperty(process, "env", descriptor);
		}
	});
});

describe("errorOnce", () => {
	test("logs once per key in every build", () => {
		env.NODE_ENV = "production";
		errorOnce("shader", "compile failed");
		errorOnce("shader", "compile failed");
		errorOnce("other", "link failed");
		expect(error.mock.calls).toEqual([["compile failed"], ["link failed"]]);
	});

	test("warn and error keys share one seen set", () => {
		env.NODE_ENV = "development";
		errorOnce("k", "boom");
		warnOnce("k", "warned");
		expect(warn).not.toHaveBeenCalled();
	});
});

describe("resetLogOnce", () => {
	test("lets a key log again", () => {
		env.NODE_ENV = "development";
		warnOnce("k", "one");
		resetLogOnce();
		warnOnce("k", "two");
		expect(warn.mock.calls).toEqual([["one"], ["two"]]);
	});
});
