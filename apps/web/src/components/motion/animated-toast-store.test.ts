import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { createAnimatedToastStore } from "./animated-toast-stack";

// The store schedules through window.setTimeout. A manual registry stands in
// for it so the test decides when a timer is due, never the wall clock.
let scheduled: Map<number, () => void>;
let nextId: number;
const realWindow = (globalThis as { window?: unknown }).window;

beforeEach(() => {
	scheduled = new Map();
	nextId = 1;
	(globalThis as { window?: unknown }).window = {
		setTimeout: (callback: () => void) => {
			const id = nextId++;
			scheduled.set(id, callback);
			return id;
		},
		clearTimeout: (id: number) => {
			scheduled.delete(id);
		},
	};
});

afterEach(() => {
	(globalThis as { window?: unknown }).window = realWindow;
});

describe("createAnimatedToastStore", () => {
	test("toasts added after the stack emptied under a hovering pointer still time out", () => {
		const store = createAnimatedToastStore();
		const first = store.add({ title: "first" });
		// The pointer enters the stack, then the last toast is dismissed with it
		// hovered: the stack unmounts and no mouseleave ever resumes the store.
		store.pause();
		store.dismiss(first);

		store.add({ title: "second" });

		expect(scheduled.size).toBe(1);
		const [fire] = [...scheduled.values()];
		fire?.();
		expect(store.getSnapshot()).toEqual([]);
	});

	test("clearing while paused also resumes timers for what follows", () => {
		const store = createAnimatedToastStore();
		store.add({ title: "first" });
		store.pause();
		store.clear();

		store.add({ title: "second" });

		expect(scheduled.size).toBe(1);
	});

	test("a toast dismissed while others remain keeps the pause", () => {
		const store = createAnimatedToastStore();
		const first = store.add({ title: "first" });
		store.add({ title: "second" });
		store.pause();
		store.dismiss(first);

		store.add({ title: "third" });

		expect(scheduled.size).toBe(0);
	});
});
