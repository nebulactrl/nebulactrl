import { describe, expect, test } from "bun:test";
import { type PlayEnv, playOnce, type Step } from "./play-once.ts";

/** A clock and a viewport the test drives: timers fire in order as time advances. */
function fakeEnv(reduced = false) {
	let now = 0;
	let next = 0;
	const timers = new Map<number, { at: number; run: () => void }>();
	const watchers = new Set<(inView: boolean) => void>();
	const env: PlayEnv = {
		setTimeout(run, ms) {
			next += 1;
			timers.set(next, { at: now + ms, run });
			return next;
		},
		clearTimeout(id) {
			timers.delete(id as number);
		},
		observe(_el, _threshold, onChange) {
			watchers.add(onChange);
			return () => watchers.delete(onChange);
		},
		reduced: () => reduced,
	};
	return {
		env,
		advance(ms: number) {
			const target = now + ms;
			for (;;) {
				const due = [...timers.entries()]
					.filter(([, t]) => t.at <= target)
					.sort((a, b) => a[1].at - b[1].at)[0];
				if (!due) break;
				timers.delete(due[0]);
				now = due[1].at;
				due[1].run();
			}
			now = target;
		},
		setInView(value: boolean) {
			for (const watcher of [...watchers]) watcher(value);
		},
		pending: () => timers.size,
		watching: () => watchers.size,
	};
}

const el = {} as Element;

/** Steps that log their name, so a test can read what ran. */
function story(log: string[], name: string): Step[] {
	return [
		{ at: 0, run: () => log.push(`${name}:0`) },
		{ at: 100, run: () => log.push(`${name}:100`) },
		{ at: 300, run: () => log.push(`${name}:300`) },
	];
}

describe("playOnce", () => {
	test("nothing runs until the element is in view; then the steps run in order, once", () => {
		const clock = fakeEnv();
		const log: string[] = [];
		playOnce(el, story(log, "a"), { end: () => log.push("end") }, clock.env);
		clock.advance(1000);
		expect(log).toEqual([]);
		clock.setInView(true);
		clock.advance(150);
		expect(log).toEqual(["a:0", "a:100"]);
		clock.advance(1000);
		expect(log).toEqual(["a:0", "a:100", "a:300"]);
		// It never plays again, however often it comes back into view.
		clock.setInView(false);
		clock.setInView(true);
		clock.advance(1000);
		expect(log).toHaveLength(3);
		expect(clock.pending()).toBe(0);
		expect(clock.watching()).toBe(0);
	});

	test("under reduced motion it draws the end state at once and plays nothing", () => {
		const clock = fakeEnv(true);
		const log: string[] = [];
		playOnce(el, story(log, "a"), { end: () => log.push("end") }, clock.env);
		expect(log).toEqual(["end"]);
		expect(clock.watching()).toBe(0);
		clock.setInView(true);
		clock.advance(1000);
		expect(log).toEqual(["end"]);
	});

	test("one story plays at a time: a second in view waits for the first to finish", () => {
		const clock = fakeEnv();
		const log: string[] = [];
		playOnce(el, story(log, "a"), { end: () => {} }, clock.env);
		playOnce(el, story(log, "b"), { end: () => {} }, clock.env);
		clock.setInView(true);
		clock.advance(299);
		expect(log).toEqual(["a:0", "a:100"]);
		clock.advance(1);
		// The first one is over at its last step (its length), and the second starts then.
		clock.advance(1);
		expect(log).toContain("b:0");
		expect(log.indexOf("b:0")).toBeGreaterThan(log.indexOf("a:300"));
		clock.advance(1000);
		expect(log.filter((l) => l.startsWith("b:"))).toHaveLength(3);
	});

	test("a story's own length says when the next may start", () => {
		const clock = fakeEnv();
		const log: string[] = [];
		playOnce(el, story(log, "a"), { length: 1000, end: () => {} }, clock.env);
		playOnce(el, story(log, "b"), { end: () => {} }, clock.env);
		clock.setInView(true);
		clock.advance(999);
		expect(log.some((l) => l.startsWith("b:"))).toBe(false);
		clock.advance(2);
		expect(log).toContain("b:0");
		clock.advance(1000);
	});

	test("stop cancels what is left and lets the next story play", () => {
		const clock = fakeEnv();
		const log: string[] = [];
		const stopA = playOnce(el, story(log, "a"), { end: () => {} }, clock.env);
		playOnce(el, story(log, "b"), { end: () => {} }, clock.env);
		clock.setInView(true);
		clock.advance(50);
		stopA();
		clock.advance(0);
		expect(log).toEqual(["a:0", "b:0"]);
		clock.advance(1000);
		expect(log).toEqual(["a:0", "b:0", "b:100", "b:300"]);
		expect(clock.pending()).toBe(0);
	});

	test("stopped before it was ever in view, it never plays", () => {
		const clock = fakeEnv();
		const log: string[] = [];
		const stop = playOnce(el, story(log, "a"), { end: () => {} }, clock.env);
		stop();
		clock.setInView(true);
		clock.advance(1000);
		expect(log).toEqual([]);
		expect(clock.watching()).toBe(0);
	});
});
