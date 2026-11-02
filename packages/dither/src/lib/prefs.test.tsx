import { afterEach, describe, expect, test } from "bun:test";
import { act, render, renderHook } from "@testing-library/react";
import { useRef } from "react";
import {
	useElementWidth,
	useForcedColors,
	useOnScreen,
	useReducedMotionPref,
} from "./prefs";

/** A controllable matchMedia: flip a query's state and its listeners fire. */
function fakeMedia(initial: Record<string, boolean>) {
	const original = window.matchMedia;
	const state = { ...initial };
	const listeners = new Map<string, Set<() => void>>();
	window.matchMedia = ((query: string) => ({
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
	})) as unknown as typeof window.matchMedia;
	return {
		set(query: string, value: boolean) {
			state[query] = value;
			for (const fn of listeners.get(query) ?? []) fn();
		},
		listenerCount: (query: string) => listeners.get(query)?.size ?? 0,
		restore() {
			window.matchMedia = original;
		},
	};
}

const REDUCED = "(prefers-reduced-motion: reduce)";
const FORCED = "(forced-colors: active)";

let media: ReturnType<typeof fakeMedia> | null = null;
const realResize = globalThis.ResizeObserver;
const realIntersection = globalThis.IntersectionObserver;
afterEach(() => {
	media?.restore();
	media = null;
	globalThis.ResizeObserver = realResize;
	globalThis.IntersectionObserver = realIntersection;
});

describe("media preference hooks", () => {
	test("useReducedMotionPref reads the query and follows changes", () => {
		media = fakeMedia({ [REDUCED]: false });
		const { result, unmount } = renderHook(() => useReducedMotionPref());
		expect(result.current).toBe(false);

		act(() => media?.set(REDUCED, true));
		expect(result.current).toBe(true);

		unmount();
		expect(media.listenerCount(REDUCED)).toBe(0);
	});

	test("useForcedColors tracks forced-colors independently of motion", () => {
		media = fakeMedia({ [REDUCED]: true, [FORCED]: false });
		const { result } = renderHook(() => useForcedColors());
		expect(result.current).toBe(false);

		act(() => media?.set(FORCED, true));
		expect(result.current).toBe(true);
	});
});

describe("useElementWidth", () => {
	function stubResizeObserver() {
		const instances: {
			callback: ResizeObserverCallback;
			observed: Element[];
			disconnected: boolean;
		}[] = [];
		globalThis.ResizeObserver = class {
			entry = {
				callback: null as unknown as ResizeObserverCallback,
				observed: [] as Element[],
				disconnected: false,
			};
			constructor(callback: ResizeObserverCallback) {
				this.entry.callback = callback;
				instances.push(this.entry);
			}
			observe(el: Element) {
				this.entry.observed.push(el);
			}
			unobserve() {}
			disconnect() {
				this.entry.disconnected = true;
			}
		} as unknown as typeof ResizeObserver;
		return instances;
	}

	// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
	function Probe({ attach }: { attach: boolean }) {
		const ref = useRef<HTMLDivElement>(null);
		const width = useElementWidth(ref);
		return (
			<div ref={attach ? ref : undefined} data-testid="probe">
				{width === null ? "unmeasured" : width}
			</div>
		);
	}

	test("is null until measured, then reports the floored content width", () => {
		const observers = stubResizeObserver();
		const { getByTestId } = render(<Probe attach />);
		expect(getByTestId("probe")).toHaveTextContent("unmeasured");
		expect(observers).toHaveLength(1);
		expect(observers[0]?.observed).toEqual([getByTestId("probe")]);

		act(() =>
			observers[0]?.callback(
				[{ contentRect: { width: 321.9 } } as ResizeObserverEntry],
				{} as ResizeObserver,
			),
		);
		expect(getByTestId("probe")).toHaveTextContent("321");
	});

	test("ignores an empty entry list and disconnects on unmount", () => {
		const observers = stubResizeObserver();
		const { getByTestId, unmount } = render(<Probe attach />);
		act(() => observers[0]?.callback([], {} as ResizeObserver));
		expect(getByTestId("probe")).toHaveTextContent("unmeasured");

		unmount();
		expect(observers[0]?.disconnected).toBe(true);
	});

	test("does not observe when the ref is unattached", () => {
		const observers = stubResizeObserver();
		render(<Probe attach={false} />);
		expect(observers).toHaveLength(0);
	});
});

describe("useOnScreen", () => {
	function stubIntersectionObserver() {
		const instances: {
			callback: IntersectionObserverCallback;
			disconnected: boolean;
		}[] = [];
		globalThis.IntersectionObserver = class {
			entry = {
				callback: null as unknown as IntersectionObserverCallback,
				disconnected: false,
			};
			constructor(callback: IntersectionObserverCallback) {
				this.entry.callback = callback;
				instances.push(this.entry);
			}
			observe() {}
			unobserve() {}
			disconnect() {
				this.entry.disconnected = true;
			}
		} as unknown as typeof IntersectionObserver;
		return instances;
	}

	// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
	function Probe({ attach }: { attach: boolean }) {
		const ref = useRef<HTMLDivElement>(null);
		const on = useOnScreen(ref);
		return (
			<div ref={attach ? ref : undefined} data-testid="probe">
				{on ? "on" : "off"}
			</div>
		);
	}

	const entry = (isIntersecting: boolean) =>
		[{ isIntersecting } as IntersectionObserverEntry] as const;

	test("flips with the intersection state and disconnects on unmount", () => {
		const observers = stubIntersectionObserver();
		const { getByTestId, unmount } = render(<Probe attach />);
		expect(getByTestId("probe")).toHaveTextContent("off");

		act(() =>
			observers[0]?.callback([...entry(true)], {} as IntersectionObserver),
		);
		expect(getByTestId("probe")).toHaveTextContent("on");
		act(() =>
			observers[0]?.callback([...entry(false)], {} as IntersectionObserver),
		);
		expect(getByTestId("probe")).toHaveTextContent("off");

		unmount();
		expect(observers[0]?.disconnected).toBe(true);
	});

	test("stays off with an empty entry list and without an element", () => {
		const observers = stubIntersectionObserver();
		const { getByTestId } = render(<Probe attach />);
		act(() => observers[0]?.callback([], {} as IntersectionObserver));
		expect(getByTestId("probe")).toHaveTextContent("off");

		render(<Probe attach={false} />);
		expect(observers).toHaveLength(1);
	});
});
