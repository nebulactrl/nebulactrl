import { describe, expect, test } from "bun:test";
import {
	clampFps,
	createFrameEntry,
	createScheduler,
	frameState,
	isFrameActive,
	MAX_FPS,
	MAX_FRAME_DELTA,
} from "./motion";

/** A manual rAF: callbacks run only when the test advances the clock. */
function fakeClock() {
	let now = 0;
	let nextHandle = 1;
	const queue = new Map<number, (now: number) => void>();
	return {
		env: {
			request(callback: (now: number) => void) {
				const handle = nextHandle++;
				queue.set(handle, callback);
				return handle;
			},
			cancel(handle: number) {
				queue.delete(handle);
			},
		},
		/** Fire one animation frame `ms` after the previous one. */
		frame(ms: number) {
			now += ms;
			const pending = [...queue.entries()];
			queue.clear();
			for (const [, callback] of pending) callback(now);
		},
		pending: () => queue.size,
	};
}

const open = {
	enabled: true,
	inView: true,
	documentVisible: true,
	reducedMotion: false,
};

describe("isFrameActive / frameState", () => {
	test("runs only when every gate is open", () => {
		expect(isFrameActive(open)).toBe(true);
		expect(isFrameActive({ ...open, enabled: false })).toBe(false);
		expect(isFrameActive({ ...open, inView: false })).toBe(false);
		expect(isFrameActive({ ...open, documentVisible: false })).toBe(false);
		expect(isFrameActive({ ...open, reducedMotion: true })).toBe(false);
	});

	test("reduced motion and disabled are static, hidden is paused", () => {
		expect(frameState(open)).toBe("running");
		expect(frameState({ ...open, reducedMotion: true })).toBe("static");
		expect(frameState({ ...open, enabled: false })).toBe("static");
		expect(frameState({ ...open, inView: false })).toBe("paused");
		expect(frameState({ ...open, documentVisible: false })).toBe("paused");
	});
});

describe("clampFps", () => {
	test("defaults, floors and caps", () => {
		expect(clampFps(undefined)).toBe(24);
		expect(clampFps(120)).toBe(MAX_FPS);
		expect(clampFps(0)).toBe(1);
		expect(clampFps(Number.NaN)).toBe(24);
	});
});

describe("scheduler", () => {
	test("idle until an entry is active, and sleeps again after", () => {
		const clock = fakeClock();
		const scheduler = createScheduler(clock.env);
		const entry = createFrameEntry(30, () => {});
		scheduler.add(entry);
		expect(scheduler.isRunning()).toBe(false);
		scheduler.setActive(entry, true);
		expect(scheduler.isRunning()).toBe(true);
		scheduler.setActive(entry, false);
		expect(scheduler.isRunning()).toBe(false);
		expect(clock.pending()).toBe(0);
	});

	test("first frame is delta 0, then time advances by elapsed seconds", () => {
		const clock = fakeClock();
		const scheduler = createScheduler(clock.env);
		const frames: Array<[number, number]> = [];
		const entry = createFrameEntry(30, (t, d) => frames.push([t, d]));
		scheduler.add(entry);
		scheduler.setActive(entry, true);
		clock.frame(16);
		expect(frames).toEqual([[0, 0]]);
		clock.frame(34);
		const last = frames[frames.length - 1];
		expect(last?.[1]).toBeCloseTo(0.034, 5);
		expect(last?.[0]).toBeCloseTo(0.034, 5);
	});

	test("caps the frame rate: a 60 Hz rAF at 24 fps ticks about every other frame", () => {
		const clock = fakeClock();
		const scheduler = createScheduler(clock.env);
		let ticks = 0;
		const entry = createFrameEntry(24, () => ticks++);
		scheduler.add(entry);
		scheduler.setActive(entry, true);
		for (let i = 0; i < 60; i++) clock.frame(1000 / 60);
		// 1 s at 24 fps, plus the first immediate frame.
		expect(ticks).toBeGreaterThanOrEqual(22);
		expect(ticks).toBeLessThanOrEqual(26);
	});

	test("a long frame counts as at most 0.1 s", () => {
		const clock = fakeClock();
		const scheduler = createScheduler(clock.env);
		const entry = createFrameEntry(30, () => {});
		scheduler.add(entry);
		scheduler.setActive(entry, true);
		clock.frame(16);
		clock.frame(5000);
		expect(entry.time).toBeCloseTo(MAX_FRAME_DELTA, 5);
	});

	test("pausing holds time and resuming does not jump", () => {
		const clock = fakeClock();
		const scheduler = createScheduler(clock.env);
		const entry = createFrameEntry(30, () => {});
		scheduler.add(entry);
		scheduler.setActive(entry, true);
		clock.frame(16);
		clock.frame(40);
		const held = entry.time;
		scheduler.setActive(entry, false);
		clock.frame(10_000);
		expect(entry.time).toBe(held);
		scheduler.setActive(entry, true);
		clock.frame(16);
		// First frame after resuming re-anchors with delta 0.
		expect(entry.time).toBe(held);
		clock.frame(40);
		expect(entry.time).toBeCloseTo(held + 0.04, 5);
	});

	test("an inactive entry is never called, and removed entries stop the loop", () => {
		const clock = fakeClock();
		const scheduler = createScheduler(clock.env);
		let calls = 0;
		const entry = createFrameEntry(30, () => calls++);
		scheduler.add(entry);
		clock.frame(16);
		expect(calls).toBe(0);
		scheduler.setActive(entry, true);
		scheduler.remove(entry);
		expect(scheduler.isRunning()).toBe(false);
		clock.frame(16);
		expect(calls).toBe(0);
	});

	test("two entries share one loop", () => {
		const clock = fakeClock();
		const scheduler = createScheduler(clock.env);
		const a = createFrameEntry(30, () => {});
		const b = createFrameEntry(30, () => {});
		scheduler.add(a);
		scheduler.add(b);
		scheduler.setActive(a, true);
		scheduler.setActive(b, true);
		expect(clock.pending()).toBe(1);
		scheduler.setActive(a, false);
		expect(scheduler.isRunning()).toBe(true);
		scheduler.setActive(b, false);
		expect(scheduler.isRunning()).toBe(false);
	});
});
