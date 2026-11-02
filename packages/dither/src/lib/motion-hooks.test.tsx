import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, renderHook } from "@testing-library/react";
import { useRef } from "react";
import type { StillReason } from "./motion";

// motion.ts keeps module state (shared scheduler, cached media query, one
// IntersectionObserver per margin) and bun shares modules across test files,
// so each test loads a private copy of the module after installing a media
// query it controls (the shared preload pins reduced motion on).
let copy = 0;
let m: typeof import("./motion");

const REDUCED = "(prefers-reduced-motion: reduce)";

function installFakeMedia() {
	const original = window.matchMedia;
	const state: Record<string, boolean> = {};
	const listeners = new Map<string, Set<() => void>>();
	const lists = new Map<string, MediaQueryList>();
	window.matchMedia = ((query: string) => {
		let list = lists.get(query);
		if (!list) {
			list = {
				get matches() {
					return state[query] ?? false;
				},
				media: query,
				addEventListener(_: string, fn: () => void) {
					if (!listeners.has(query)) listeners.set(query, new Set());
					listeners.get(query)?.add(fn);
				},
				removeEventListener(_: string, fn: () => void) {
					listeners.get(query)?.delete(fn);
				},
			} as unknown as MediaQueryList;
			lists.set(query, list);
		}
		return list;
	}) as typeof window.matchMedia;
	return {
		set(query: string, value: boolean) {
			state[query] = value;
			act(() => {
				for (const fn of listeners.get(query) ?? []) fn();
			});
		},
		restore() {
			window.matchMedia = original;
		},
	};
}

/** Manual requestAnimationFrame: callbacks run only when a test advances it. */
function installFakeRaf() {
	const g = globalThis as unknown as {
		requestAnimationFrame: typeof requestAnimationFrame;
		cancelAnimationFrame: typeof cancelAnimationFrame;
	};
	const realRequest = g.requestAnimationFrame;
	const realCancel = g.cancelAnimationFrame;
	let now = 0;
	let nextHandle = 1;
	const queue = new Map<number, FrameRequestCallback>();
	g.requestAnimationFrame = (cb) => {
		const handle = nextHandle++;
		queue.set(handle, cb);
		return handle;
	};
	g.cancelAnimationFrame = (handle) => {
		queue.delete(handle);
	};
	return {
		/** One animation frame `ms` after the previous one. */
		frame(ms: number) {
			now += ms;
			const pending = [...queue.values()];
			queue.clear();
			act(() => {
				for (const cb of pending) cb(now);
			});
		},
		pending: () => queue.size,
		restore() {
			g.requestAnimationFrame = realRequest;
			g.cancelAnimationFrame = realCancel;
		},
	};
}

let media: ReturnType<typeof installFakeMedia>;

function setReducedMotion(reduce: boolean) {
	media.set(REDUCED, reduce);
}

function setDocumentHidden(hidden: boolean) {
	Object.defineProperty(document, "visibilityState", {
		configurable: true,
		get: () => (hidden ? "hidden" : "visible"),
	});
	act(() => {
		document.dispatchEvent(new Event("visibilitychange"));
	});
}

let raf: ReturnType<typeof installFakeRaf>;
beforeEach(async () => {
	raf = installFakeRaf();
	media = installFakeMedia();
	copy += 1;
	m = await import(`./motion?copy=${copy}`);
});
afterEach(() => {
	raf.restore();
	media.restore();
	delete (document as unknown as Record<string, unknown>).visibilityState;
});

type Call = [time: number, delta: number, still: StillReason | undefined];

describe("useDitherFrame", () => {
	test("runs frames on the shared clock: first frame is delta 0, then real elapsed time", () => {
		const calls: Call[] = [];
		const { result } = renderHook(() =>
			m.useDitherFrame((t, d, s) => calls.push([t, d, s])),
		);
		expect(result.current).toBe("running");
		expect(calls).toEqual([]);

		raf.frame(16);
		expect(calls).toEqual([[0, 0, undefined]]);
		raf.frame(50);
		expect(calls).toHaveLength(2);
		expect(calls[1]?.[1]).toBeCloseTo(0.05, 5);
		expect(calls[1]?.[0]).toBeCloseTo(0.05, 5);
		expect(calls[1]?.[2]).toBeUndefined();
	});

	test("the fps option caps how often the callback fires", () => {
		let ticks = 0;
		renderHook(() => m.useDitherFrame(() => ticks++, { fps: 10 }));
		raf.frame(16);
		expect(ticks).toBe(1);
		// 50 ms is under the 100 ms interval of 10 fps.
		raf.frame(50);
		expect(ticks).toBe(1);
		raf.frame(60);
		expect(ticks).toBe(2);
	});

	test("an fps change applies to the live entry", () => {
		let ticks = 0;
		const { rerender } = renderHook(
			({ fps }) => m.useDitherFrame(() => ticks++, { fps }),
			{ initialProps: { fps: 10 } },
		);
		raf.frame(16);
		raf.frame(60);
		expect(ticks).toBe(1);
		rerender({ fps: 30 });
		// 60 ms is past the 33 ms interval of 30 fps.
		raf.frame(60);
		expect(ticks).toBe(2);
	});

	test("always calls the latest callback without re-subscribing", () => {
		const seen: string[] = [];
		const { rerender } = renderHook(
			({ tag }) => m.useDitherFrame(() => seen.push(tag)),
			{ initialProps: { tag: "a" } },
		);
		raf.frame(16);
		rerender({ tag: "b" });
		raf.frame(60);
		expect(seen).toEqual(["a", "b"]);
	});

	test("enabled: false is static: one still call at stillTime, no frame loop", () => {
		const calls: Call[] = [];
		const { result } = renderHook(() =>
			m.useDitherFrame((t, d, s) => calls.push([t, d, s]), {
				enabled: false,
				stillTime: 3,
			}),
		);
		expect(result.current).toBe("static");
		expect(calls).toEqual([[3, 0, "disabled"]]);
		expect(raf.pending()).toBe(0);
	});

	test("disabling mid-run holds the time already reached", () => {
		const calls: Call[] = [];
		const { rerender, result } = renderHook(
			({ enabled }) =>
				m.useDitherFrame((t, d, s) => calls.push([t, d, s]), {
					enabled,
					stillTime: 9,
				}),
			{ initialProps: { enabled: true } },
		);
		raf.frame(16);
		raf.frame(50);
		const reached = calls[calls.length - 1]?.[0] ?? 0;
		expect(reached).toBeGreaterThan(0);

		rerender({ enabled: false });
		expect(result.current).toBe("static");
		expect(calls[calls.length - 1]).toEqual([reached, 0, "disabled"]);
		expect(raf.pending()).toBe(0);

		rerender({ enabled: true });
		expect(result.current).toBe("running");
		expect(raf.pending()).toBe(1);
	});

	test("reduced motion draws one still frame at stillTime and never ticks", () => {
		setReducedMotion(true);
		const calls: Call[] = [];
		const { result } = renderHook(() =>
			m.useDitherFrame((t, d, s) => calls.push([t, d, s]), { stillTime: 4 }),
		);
		expect(result.current).toBe("static");
		expect(calls).toEqual([[4, 0, "reduced"]]);
		expect(raf.pending()).toBe(0);
	});

	test("follows the reduced-motion setting while mounted", () => {
		const calls: Call[] = [];
		const { result } = renderHook(() =>
			m.useDitherFrame((t, d, s) => calls.push([t, d, s]), { stillTime: 2 }),
		);
		expect(result.current).toBe("running");

		setReducedMotion(true);
		expect(result.current).toBe("static");
		expect(calls[calls.length - 1]).toEqual([2, 0, "reduced"]);
		expect(raf.pending()).toBe(0);

		setReducedMotion(false);
		expect(result.current).toBe("running");
		expect(raf.pending()).toBe(1);
	});

	test("a hidden tab pauses the loop and a visible one resumes it", () => {
		const { result } = renderHook(() => m.useDitherFrame(() => {}));
		expect(result.current).toBe("running");
		expect(raf.pending()).toBe(1);

		setDocumentHidden(true);
		expect(result.current).toBe("paused");
		expect(raf.pending()).toBe(0);

		setDocumentHidden(false);
		expect(result.current).toBe("running");
		expect(raf.pending()).toBe(1);
	});

	test("a target that is not on screen yet keeps the clock paused", () => {
		// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
		function Probe() {
			const ref = useRef<HTMLDivElement>(null);
			const state = m.useDitherFrame(() => {}, { target: ref });
			return (
				<div ref={ref} data-testid="probe">
					{state}
				</div>
			);
		}
		const { getByTestId } = render(<Probe />);
		// No intersection report has arrived, so the element counts as off-screen.
		expect(getByTestId("probe")).toHaveTextContent("paused");
		expect(raf.pending()).toBe(0);
	});

	test("a target ref with no element runs, since there is nothing to gate on", () => {
		const { result } = renderHook(() =>
			m.useDitherFrame(() => {}, { target: { current: null } }),
		);
		expect(result.current).toBe("running");
		expect(raf.pending()).toBe(1);
	});

	test("unmounting removes the entry and stops the loop", () => {
		let ticks = 0;
		const { unmount } = renderHook(() => m.useDitherFrame(() => ticks++));
		raf.frame(16);
		expect(ticks).toBe(1);
		unmount();
		expect(raf.pending()).toBe(0);
		raf.frame(100);
		expect(ticks).toBe(1);
	});
});

describe("useFrameLoop", () => {
	test("passes time and delta only, and returns the frame state", () => {
		const args: unknown[][] = [];
		const { result } = renderHook(() =>
			m.useFrameLoop((...a: unknown[]) => args.push(a) as unknown as undefined),
		);
		expect(result.current).toBe("running");
		raf.frame(16);
		expect(args).toEqual([[0, 0]]);
	});

	test("static still call carries stillTime", () => {
		const args: unknown[][] = [];
		const { result } = renderHook(() =>
			m.useFrameLoop(
				(...a: unknown[]) => args.push(a) as unknown as undefined,
				{
					enabled: false,
					stillTime: 5,
				},
			),
		);
		expect(result.current).toBe("static");
		expect(args).toEqual([[5, 0]]);
	});
});

describe("useDitherClock", () => {
	test("starts at stillTime and tracks the shared clock without re-rendering", () => {
		let renders = 0;
		const { result } = renderHook(() => {
			renders++;
			return m.useDitherClock({ stillTime: 7 });
		});
		expect(result.current.current).toBe(7);
		const before = renders;

		raf.frame(16);
		expect(result.current.current).toBe(0);
		raf.frame(50);
		expect(result.current.current).toBeCloseTo(0.05, 5);
		expect(renders).toBe(before);
	});

	test("holds stillTime under reduced motion", () => {
		setReducedMotion(true);
		const { result } = renderHook(() => m.useDitherClock({ stillTime: 6 }));
		expect(result.current.current).toBe(6);
		expect(raf.pending()).toBe(0);
	});
});

describe("preference hooks", () => {
	test("prefersReducedMotion and usePrefersReducedMotion read the media query live", () => {
		const { result } = renderHook(() => m.usePrefersReducedMotion());
		expect(result.current).toBe(false);
		expect(m.prefersReducedMotion()).toBe(false);

		setReducedMotion(true);
		expect(result.current).toBe(true);
		expect(m.prefersReducedMotion()).toBe(true);
	});

	test("useDocumentVisible follows visibilitychange", () => {
		const { result } = renderHook(() => m.useDocumentVisible());
		expect(result.current).toBe(true);
		setDocumentHidden(true);
		expect(result.current).toBe(false);
		setDocumentHidden(false);
		expect(result.current).toBe(true);
	});
});

describe("observeVisibility", () => {
	interface FakeObserver {
		options: IntersectionObserverInit | undefined;
		callback: IntersectionObserverCallback;
		observed: Element[];
		unobserved: Element[];
	}

	function installFakeObserver() {
		const g = globalThis as unknown as {
			IntersectionObserver: typeof IntersectionObserver | undefined;
		};
		const real = g.IntersectionObserver;
		const created: FakeObserver[] = [];
		g.IntersectionObserver = class {
			readonly record: FakeObserver;
			constructor(
				callback: IntersectionObserverCallback,
				options?: IntersectionObserverInit,
			) {
				this.record = { options, callback, observed: [], unobserved: [] };
				created.push(this.record);
			}
			observe(el: Element) {
				this.record.observed.push(el);
			}
			unobserve(el: Element) {
				this.record.unobserved.push(el);
			}
			disconnect() {}
		} as unknown as typeof IntersectionObserver;
		return {
			created,
			report(
				observer: FakeObserver,
				...records: { target: Element; isIntersecting: boolean }[]
			) {
				observer.callback(
					records as IntersectionObserverEntry[],
					{} as IntersectionObserver,
				);
			},
			remove() {
				g.IntersectionObserver = undefined;
			},
			restore() {
				g.IntersectionObserver = real;
			},
		};
	}

	let fake: ReturnType<typeof installFakeObserver>;
	beforeEach(() => {
		fake = installFakeObserver();
	});
	afterEach(() => fake.restore());

	test("shares one observer per margin and routes reports to each element", () => {
		const a = document.createElement("div");
		const b = document.createElement("div");
		const seenA: boolean[] = [];
		const seenB: boolean[] = [];
		const stopA = m.observeVisibility(a, "101px 0px", (v) => seenA.push(v));
		const stopB = m.observeVisibility(b, "101px 0px", (v) => seenB.push(v));

		expect(fake.created).toHaveLength(1);
		expect(fake.created[0]?.options).toEqual({ rootMargin: "101px 0px" });
		expect(fake.created[0]?.observed).toEqual([a, b]);

		const [observer] = fake.created;
		if (!observer) throw new Error("observer not created");
		fake.report(
			observer,
			{ target: a, isIntersecting: true },
			{ target: b, isIntersecting: false },
		);
		expect(seenA).toEqual([true]);
		expect(seenB).toEqual([false]);

		stopA();
		stopB();
	});

	test("a different margin gets its own observer", () => {
		const el = document.createElement("div");
		const stop1 = m.observeVisibility(el, "102px 0px", () => {});
		const stop2 = m.observeVisibility(el, "103px 0px", () => {});
		expect(fake.created.map((o) => o.options?.rootMargin)).toEqual([
			"102px 0px",
			"103px 0px",
		]);
		stop1();
		stop2();
	});

	test("unsubscribing unobserves the element and silences its callback", () => {
		const el = document.createElement("div");
		const seen: boolean[] = [];
		const stop = m.observeVisibility(el, "104px 0px", (v) => seen.push(v));
		const [observer] = fake.created;
		if (!observer) throw new Error("observer not created");

		stop();
		expect(observer.unobserved).toEqual([el]);
		fake.report(observer, { target: el, isIntersecting: true });
		expect(seen).toEqual([]);
	});

	test("without IntersectionObserver the element counts as visible", () => {
		fake.remove();
		const seen: boolean[] = [];
		const stop = m.observeVisibility(
			document.createElement("div"),
			"105px 0px",
			(v) => seen.push(v),
		);
		expect(seen).toEqual([true]);
		expect(stop()).toBeUndefined();
	});

	test("useInView reflects intersection reports and stops on unmount", () => {
		// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
		function Probe({ attach }: { attach: boolean }) {
			const ref = useRef<HTMLDivElement>(null);
			const inView = m.useInView(ref, "106px 0px");
			return (
				<div ref={attach ? ref : undefined} data-testid="probe">
					{inView ? "in" : "out"}
				</div>
			);
		}
		const { getByTestId, unmount } = render(<Probe attach />);
		expect(getByTestId("probe")).toHaveTextContent("out");
		const [observer] = fake.created;
		if (!observer) throw new Error("observer not created");
		expect(observer.options).toEqual({ rootMargin: "106px 0px" });

		const el = getByTestId("probe");
		act(() => fake.report(observer, { target: el, isIntersecting: true }));
		expect(getByTestId("probe")).toHaveTextContent("in");
		act(() => fake.report(observer, { target: el, isIntersecting: false }));
		expect(getByTestId("probe")).toHaveTextContent("out");

		unmount();
		expect(observer.unobserved).toEqual([el]);
	});

	test("useDitherFrame runs only while its target is near the viewport", () => {
		// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
		function Probe() {
			const ref = useRef<HTMLDivElement>(null);
			const state = m.useDitherFrame(() => {}, { target: ref });
			return (
				<div ref={ref} data-testid="probe">
					{state}
				</div>
			);
		}
		const { getByTestId } = render(<Probe />);
		const [observer] = fake.created;
		if (!observer) throw new Error("observer not created");
		// The loop gates on a margin around the viewport, not the viewport itself.
		expect(observer.options).toEqual({ rootMargin: "64px 0px" });
		const el = getByTestId("probe");
		expect(el).toHaveTextContent("paused");
		expect(raf.pending()).toBe(0);

		act(() => fake.report(observer, { target: el, isIntersecting: true }));
		expect(el).toHaveTextContent("running");
		expect(raf.pending()).toBe(1);

		act(() => fake.report(observer, { target: el, isIntersecting: false }));
		expect(el).toHaveTextContent("paused");
		expect(raf.pending()).toBe(0);
	});

	test("useInView without an element never observes", () => {
		const { result } = renderHook(() =>
			m.useInView({ current: null }, "107px 0px"),
		);
		expect(result.current).toBe(false);
		expect(fake.created).toHaveLength(0);
	});
});
