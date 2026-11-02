import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { Skeleton, SkeletonReveal } from "./skeleton";

describe("Skeleton", () => {
	test("is hidden from assistive tech by default", () => {
		const { container } = render(<Skeleton />);
		const el = container.firstElementChild as HTMLElement;
		expect(el).toHaveAttribute("data-slot", "skeleton");
		expect(el).toHaveAttribute("aria-hidden", "true");
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("with an explicit role it is exposed instead of hidden", () => {
		render(<Skeleton role="status" aria-label="Loading servers" />);
		const el = screen.getByRole("status", { name: "Loading servers" });
		expect(el).not.toHaveAttribute("aria-hidden");
	});

	test("applies the default radius unless className sets one", () => {
		const { rerender, container } = render(<Skeleton className="h-4 w-10" />);
		const el = () => container.firstElementChild as HTMLElement;
		expect(el()).toHaveClass("nc-skeleton", "h-4", "w-10", "rounded-ctl");
		rerender(<Skeleton className="h-4 rounded-full" />);
		expect(el()).toHaveClass("rounded-full");
		expect(el()).not.toHaveClass("rounded-ctl");
	});
});

// SkeletonReveal schedules requestAnimationFrame and a timeout; drive them by
// hand so the phases are observable without waiting on real time.
describe("SkeletonReveal", () => {
	let frames: Array<FrameRequestCallback | null>;
	let timers: Array<{ fn: () => void; ms: number; live: boolean }>;
	const real = {
		raf: globalThis.requestAnimationFrame,
		caf: globalThis.cancelAnimationFrame,
		st: window.setTimeout,
		ct: window.clearTimeout,
	};

	beforeEach(() => {
		frames = [];
		timers = [];
		globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
			frames.push(cb);
			return frames.length;
		}) as typeof requestAnimationFrame;
		globalThis.cancelAnimationFrame = ((id: number) => {
			frames[id - 1] = null;
		}) as typeof cancelAnimationFrame;
		window.setTimeout = ((fn: () => void, ms: number) => {
			timers.push({ fn, ms, live: true });
			return timers.length;
		}) as unknown as typeof window.setTimeout;
		window.clearTimeout = ((id: number) => {
			const t = timers[id - 1];
			if (t) t.live = false;
		}) as typeof window.clearTimeout;
	});
	afterEach(() => {
		globalThis.requestAnimationFrame = real.raf;
		globalThis.cancelAnimationFrame = real.caf;
		window.setTimeout = real.st;
		window.clearTimeout = real.ct;
	});

	const flushFrames = () => {
		// run queued frames (each may enqueue another)
		for (let i = 0; i < frames.length; i++) {
			const cb = frames[i];
			frames[i] = null;
			cb?.(0);
		}
	};

	test("shows only the skeleton while loading", () => {
		render(
			<SkeletonReveal loading skeleton={<div>skeleton</div>}>
				<div>content</div>
			</SkeletonReveal>,
		);
		expect(screen.getByText("skeleton")).toBeInTheDocument();
		expect(screen.queryByText("content")).toBeNull();
	});

	test("content already loaded on mount renders at once with no reveal classes", () => {
		const { container } = render(
			<SkeletonReveal
				loading={false}
				skeleton={<div>skeleton</div>}
				className="grid"
			>
				<div>content</div>
			</SkeletonReveal>,
		);
		expect(screen.getByText("content")).toBeInTheDocument();
		expect(screen.queryByText("skeleton")).toBeNull();
		const wrapper = container.firstElementChild as HTMLElement;
		expect(wrapper).toHaveClass("grid");
		expect(wrapper).not.toHaveClass("t-skel");
		expect(frames).toHaveLength(0);
	});

	test("the caller's className lands on the wrapper while loading", () => {
		const { container } = render(
			<SkeletonReveal loading skeleton={<i>s</i>} className="flex gap-2">
				<b>c</b>
			</SkeletonReveal>,
		);
		expect(container.firstElementChild).toHaveClass("flex", "gap-2");
	});

	test("loading -> loaded cross-fades: both present, then settles on content only", () => {
		const { container, rerender } = render(
			<SkeletonReveal loading skeleton={<div>skeleton</div>} className="grid">
				<div>content</div>
			</SkeletonReveal>,
		);
		rerender(
			<SkeletonReveal
				loading={false}
				skeleton={<div>skeleton</div>}
				className="grid"
			>
				<div>content</div>
			</SkeletonReveal>,
		);
		const wrapper = container.firstElementChild as HTMLElement;
		// revealing: wrapper is flagged, skeleton overlay kept aria-hidden, content mounted
		expect(wrapper).toHaveClass("t-skel", "grid");
		expect(wrapper).not.toHaveClass("is-revealed");
		expect(screen.getByText("content")).toBeInTheDocument();
		const overlay = screen.getByText("skeleton").parentElement as HTMLElement;
		expect(overlay).toHaveAttribute("aria-hidden", "true");
		expect(overlay).toHaveClass("t-skel-skeleton", "grid");

		// two frames later the tween starts
		act(() => flushFrames());
		expect(wrapper).toHaveClass("is-revealed");

		// the settle timer fires after the reveal duration (slow 400ms + 80)
		const settle = timers.find((t) => t.live);
		expect(settle?.ms).toBe(480);
		act(() => settle?.fn());
		expect(wrapper).not.toHaveClass("t-skel");
		expect(wrapper).not.toHaveClass("is-revealed");
		expect(screen.queryByText("skeleton")).toBeNull();
		expect(screen.getByText("content")).toBeInTheDocument();
	});

	test("going back to loading restores the skeleton and drops the content", () => {
		const ui = (loading: boolean) => (
			<SkeletonReveal loading={loading} skeleton={<div>skeleton</div>}>
				<div>content</div>
			</SkeletonReveal>
		);
		const { rerender } = render(ui(false));
		expect(screen.getByText("content")).toBeInTheDocument();
		rerender(ui(true));
		expect(screen.getByText("skeleton")).toBeInTheDocument();
		expect(screen.queryByText("content")).toBeNull();
	});

	test("children as a function is only called once loading has ended", () => {
		let calls = 0;
		const ui = (loading: boolean) => (
			<SkeletonReveal loading={loading} skeleton={<div>skeleton</div>}>
				{() => {
					calls++;
					return <div>lazy content</div>;
				}}
			</SkeletonReveal>
		);
		const { rerender } = render(ui(true));
		expect(calls).toBe(0);
		expect(screen.queryByText("lazy content")).toBeNull();
		rerender(ui(false));
		expect(calls).toBeGreaterThan(0);
		expect(screen.getByText("lazy content")).toBeInTheDocument();
	});

	test("unmounting mid-reveal cancels the pending frames and timer", () => {
		const ui = (loading: boolean) => (
			<SkeletonReveal loading={loading} skeleton={<div>s</div>}>
				<div>c</div>
			</SkeletonReveal>
		);
		const { rerender, unmount } = render(ui(true));
		rerender(ui(false));
		expect(timers.some((t) => t.live)).toBe(true);
		unmount();
		expect(timers.every((t) => !t.live)).toBe(true);
		expect(frames.every((f) => f === null)).toBe(true);
	});
});
