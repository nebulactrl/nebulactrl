import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, cleanup, render, renderHook } from "@testing-library/react";
import { useRef } from "react";
import {
	useBoxSize,
	useDevicePixelRatio,
	useFontVersion,
	useInView,
} from "./hooks";
import { stubObservers } from "./test-env";

let env: ReturnType<typeof stubObservers>;
beforeEach(() => {
	env = stubObservers();
});
afterEach(() => env.restore());

function BoxProbe() {
	const ref = useRef<HTMLDivElement>(null);
	const size = useBoxSize(ref);
	return (
		<div ref={ref} data-testid="box">
			{size ? `${size.width}x${size.height}` : "unmeasured"}
		</div>
	);
}

function SizeLogProbe({ seen }: { seen: unknown[] }) {
	const ref = useRef<HTMLDivElement>(null);
	seen.push(useBoxSize(ref));
	return <div ref={ref} data-testid="box" />;
}

describe("useBoxSize", () => {
	test("is null until the element reports a size, then follows every resize", () => {
		const { getByTestId } = render(<BoxProbe />);
		const el = getByTestId("box");
		expect(el).toHaveTextContent("unmeasured");
		env.resize(el, 300, 150);
		expect(el).toHaveTextContent("300x150");
		env.resize(el, 320, 150);
		expect(el).toHaveTextContent("320x150");
	});

	test("an unchanged size keeps the same object, so nothing re-renders for it", () => {
		const seen: unknown[] = [];
		const { getByTestId } = render(<SizeLogProbe seen={seen} />);
		env.resize(getByTestId("box"), 100, 50);
		const first = seen.at(-1);
		env.resize(getByTestId("box"), 100, 50);
		expect(seen.at(-1)).toBe(first);
	});

	test("stops observing on unmount", () => {
		const { unmount } = render(<BoxProbe />);
		expect(env.ro.some((r) => r.live)).toBe(true);
		unmount();
		expect(env.ro.every((r) => !r.live)).toBe(true);
	});

	test("does nothing for a ref with no element", () => {
		const { result } = renderHook(() => useBoxSize({ current: null }));
		expect(result.current).toBeNull();
		expect(env.ro).toHaveLength(0);
	});
});

function ViewProbe(props: Parameters<typeof useInView>[1]) {
	const ref = useRef<HTMLDivElement>(null);
	const inView = useInView(ref, props);
	return (
		<div ref={ref} data-testid="el">
			{String(inView)}
		</div>
	);
}

describe("useInView", () => {
	test("false until the element intersects, and false again when it leaves", () => {
		const { getByTestId } = render(<ViewProbe />);
		const el = getByTestId("el");
		expect(el).toHaveTextContent("false");
		env.intersect(el, true);
		expect(el).toHaveTextContent("true");
		env.intersect(el, false);
		expect(el).toHaveTextContent("false");
	});

	test("once keeps it true after the first sighting and stops observing", () => {
		const { getByTestId } = render(<ViewProbe once />);
		const el = getByTestId("el");
		env.intersect(el, true);
		expect(el).toHaveTextContent("true");
		expect(env.io.every((r) => !r.live)).toBe(true);
	});

	test("once ignores a not-yet-intersecting report", () => {
		const { getByTestId } = render(<ViewProbe once />);
		env.intersect(getByTestId("el"), false);
		expect(getByTestId("el")).toHaveTextContent("false");
	});

	test("passes the margin and threshold to the observer", () => {
		render(<ViewProbe rootMargin="200px 0px" threshold={0.5} />);
		expect(env.io[0]?.options).toEqual({
			rootMargin: "200px 0px",
			threshold: 0.5,
		});
	});

	test("defaults to a 0px margin and any visibility", () => {
		render(<ViewProbe />);
		expect(env.io[0]?.options).toEqual({ rootMargin: "0px", threshold: 0 });
	});

	test("without IntersectionObserver the element counts as in view", () => {
		// @ts-expect-error simulating an old browser
		globalThis.IntersectionObserver = undefined;
		const { getByTestId } = render(<ViewProbe />);
		expect(getByTestId("el")).toHaveTextContent("true");
	});

	test("a ref with no element stays false", () => {
		const { result } = renderHook(() => useInView({ current: null }));
		expect(result.current).toBe(false);
	});
});

describe("useDevicePixelRatio", () => {
	test("reads the window's ratio, falling back to 1", () => {
		const original = window.devicePixelRatio;
		try {
			window.devicePixelRatio = 2;
			const { result } = renderHook(() => useDevicePixelRatio());
			expect(result.current).toBe(2);
		} finally {
			window.devicePixelRatio = original;
		}
	});

	test("follows a change of screen: the media query is re-armed for the new ratio", () => {
		const original = window.devicePixelRatio;
		const originalMatch = window.matchMedia;
		const queries: string[] = [];
		const listeners = new Map<string, Set<() => void>>();
		window.matchMedia = ((query: string) => {
			queries.push(query);
			return {
				matches: false,
				media: query,
				addEventListener: (_: string, fn: () => void) => {
					if (!listeners.has(query)) listeners.set(query, new Set());
					listeners.get(query)?.add(fn);
				},
				removeEventListener: (_: string, fn: () => void) =>
					listeners.get(query)?.delete(fn),
			} as unknown as MediaQueryList;
		}) as typeof window.matchMedia;
		try {
			window.devicePixelRatio = 1;
			const { result } = renderHook(() => useDevicePixelRatio());
			expect(result.current).toBe(1);
			expect(queries).toContain("(resolution: 1dppx)");
			window.devicePixelRatio = 3;
			act(() => {
				for (const fn of [...(listeners.get("(resolution: 1dppx)") ?? [])])
					fn();
			});
			expect(result.current).toBe(3);
			expect(queries).toContain("(resolution: 3dppx)");
			expect(listeners.get("(resolution: 1dppx)")?.size).toBe(0);
			expect(listeners.get("(resolution: 3dppx)")?.size).toBe(1);
		} finally {
			window.devicePixelRatio = original;
			window.matchMedia = originalMatch;
		}
	});

	test("removes its listener on unmount", () => {
		const originalMatch = window.matchMedia;
		let active = 0;
		window.matchMedia = (() =>
			({
				matches: false,
				addEventListener: () => active++,
				removeEventListener: () => active--,
			}) as unknown as MediaQueryList) as typeof window.matchMedia;
		try {
			const { unmount } = renderHook(() => useDevicePixelRatio());
			expect(active).toBe(1);
			unmount();
			expect(active).toBe(0);
		} finally {
			window.matchMedia = originalMatch;
		}
	});
});

describe("useFontVersion", () => {
	// The stub must outlive the test's unmount, which runs in the shared cleanup.
	const installed: Array<() => void> = [];
	afterEach(() => {
		cleanup();
		for (const remove of installed.splice(0)) remove();
	});
	function installFonts() {
		let resolve = () => {};
		const ready = new Promise<void>((r) => {
			resolve = r;
		});
		const listeners = new Set<() => void>();
		Object.defineProperty(document, "fonts", {
			configurable: true,
			value: {
				ready,
				addEventListener: (_: string, fn: () => void) => listeners.add(fn),
				removeEventListener: (_: string, fn: () => void) =>
					listeners.delete(fn),
			},
		});
		installed.push(() => {
			delete (document as unknown as { fonts?: unknown }).fonts;
		});
		return {
			listeners,
			finish: () => act(async () => resolve()),
		};
	}

	test("is 0 without a font loading API", () => {
		const { result } = renderHook(() => useFontVersion());
		expect(result.current).toBe(0);
	});

	test("bumps when fonts are ready and again for each batch that finishes loading", async () => {
		const fonts = installFonts();
		const { result } = renderHook(() => useFontVersion());
		expect(result.current).toBe(0);
		await fonts.finish();
		expect(result.current).toBe(1);
		act(() => {
			for (const fn of fonts.listeners) fn();
		});
		expect(result.current).toBe(2);
	});

	test("unsubscribes on unmount and never bumps after it", async () => {
		const fonts = installFonts();
		const { unmount } = renderHook(() => useFontVersion());
		expect(fonts.listeners.size).toBe(1);
		unmount();
		expect(fonts.listeners.size).toBe(0);
		await fonts.finish();
	});
});
