import { afterEach, describe, expect, test } from "bun:test";
import { whenIdle } from "./when-idle";

const realRic = window.requestIdleCallback;
const realCic = window.cancelIdleCallback;
const realSetTimeout = window.setTimeout;
const realClearTimeout = window.clearTimeout;

afterEach(() => {
	window.requestIdleCallback = realRic;
	window.cancelIdleCallback = realCic;
	window.setTimeout = realSetTimeout;
	window.clearTimeout = realClearTimeout;
});

/** A browser that idles when the test says so, and drops a cancelled request. */
function stubIdleCallbacks() {
	const pending = new Map<number, () => void>();
	let next = 1;
	window.requestIdleCallback = ((task: () => void) => {
		pending.set(next, task);
		return next++;
	}) as typeof window.requestIdleCallback;
	window.cancelIdleCallback = ((handle: number) => {
		pending.delete(handle);
	}) as typeof window.cancelIdleCallback;
	return {
		idle() {
			for (const [handle, task] of [...pending]) {
				pending.delete(handle);
				task();
			}
		},
	};
}

/** A virtual clock behind setTimeout, so a delay is observed instead of waited for. */
function stubClock() {
	const timers = new Map<number, { at: number; task: () => void }>();
	let now = 0;
	let next = 1;
	window.setTimeout = ((task: () => void, ms = 0) => {
		timers.set(next, { at: now + ms, task });
		return next++;
	}) as unknown as typeof window.setTimeout;
	window.clearTimeout = ((handle?: number) => {
		timers.delete(handle ?? -1);
	}) as typeof window.clearTimeout;
	return {
		advance(ms: number) {
			now += ms;
			for (const [handle, timer] of [...timers]) {
				if (timer.at > now) continue;
				timers.delete(handle);
				timer.task();
			}
		},
	};
}

describe("whenIdle with requestIdleCallback", () => {
	test("runs the task once, when the browser is idle", () => {
		const browser = stubIdleCallbacks();
		let ran = 0;
		whenIdle(() => ran++);
		expect(ran).toBe(0);
		browser.idle();
		browser.idle();
		expect(ran).toBe(1);
	});

	test("what it returns cancels the task", () => {
		const browser = stubIdleCallbacks();
		let ran = 0;
		const cancel = whenIdle(() => ran++);
		cancel();
		browser.idle();
		expect(ran).toBe(0);
	});
});

describe("whenIdle without requestIdleCallback (Safari)", () => {
	function safari() {
		// @ts-expect-error simulating Safari
		window.requestIdleCallback = undefined;
		return stubClock();
	}

	test("runs the task once after the timeout", () => {
		const clock = safari();
		let ran = 0;
		whenIdle(() => ran++, 100);
		clock.advance(99);
		expect(ran).toBe(0);
		clock.advance(1);
		clock.advance(500);
		expect(ran).toBe(1);
	});

	test("what it returns cancels the task", () => {
		const clock = safari();
		let ran = 0;
		const cancel = whenIdle(() => ran++, 100);
		cancel();
		clock.advance(1_000);
		expect(ran).toBe(0);
	});

	test("never waits more than 200 ms, whatever the timeout", () => {
		const clock = safari();
		let ran = 0;
		whenIdle(() => ran++, 5_000);
		clock.advance(200);
		expect(ran).toBe(1);
	});
});
