import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { StatusIcon, type StatusKind } from "./status-icon";

const ALL: Array<[StatusKind, string]> = [
	["live", "Live"],
	["healthy", "Healthy"],
	["building", "Building"],
	["deploying", "Deploying"],
	["verifying", "Verifying"],
	["failed", "Failed"],
	["degraded", "Degraded"],
	["warning", "Warning"],
	["stopped", "Stopped"],
	["offline", "Offline"],
	["pending", "Pending"],
	["unknown", "Unknown"],
];

const root = () =>
	document.querySelector("[data-slot=status-icon]") as HTMLElement;
const svg = () => root().querySelector("svg") as SVGElement;

describe("StatusIcon", () => {
	test.each(ALL)(
		"kind %s is exposed as data-kind and spoken as %s",
		(kind, spoken) => {
			render(<StatusIcon kind={kind} />);
			expect(root()).toHaveAttribute("data-kind", kind);
			expect(screen.getByText(spoken)).toHaveClass("sr-only");
			expect(svg()).toHaveAttribute("aria-hidden", "true");
		},
	);

	test("a custom label replaces the spoken name", () => {
		render(<StatusIcon kind="live" label="Serving traffic" />);
		expect(screen.getByText("Serving traffic")).toHaveClass("sr-only");
		expect(screen.queryByText("Live")).toBeNull();
	});

	test("an empty label makes the icon purely decorative", () => {
		render(<StatusIcon kind="failed" label="" />);
		expect(root().querySelector(".sr-only")).toBeNull();
		expect(root().textContent).toBe("");
	});

	test("in-flight kinds spin, settled and failed kinds do not", () => {
		const spins = (kind: StatusKind) => {
			const { unmount } = render(<StatusIcon kind={kind} />);
			const v = svg().getAttribute("class")?.includes("animate-nc-spin");
			unmount();
			return v;
		};
		expect(spins("building")).toBe(true);
		expect(spins("deploying")).toBe(true);
		expect(spins("verifying")).toBe(true);
		expect(spins("live")).toBe(false);
		expect(spins("failed")).toBe(false);
		expect(spins("pending")).toBe(false);
	});

	test("kinds sharing a glyph differ in colour, distinct glyphs differ in shape", () => {
		const html = (kind: StatusKind) => {
			const { unmount } = render(<StatusIcon kind={kind} />);
			const out = { shape: svg().innerHTML, cls: svg().getAttribute("class") };
			unmount();
			return out;
		};
		const live = html("live");
		const healthy = html("healthy");
		expect(live.shape).toBe(healthy.shape);
		const failed = html("failed");
		expect(failed.shape).not.toBe(live.shape);
		const building = html("building");
		expect(building.shape).not.toBe(live.shape);
		expect(building.cls).not.toBe(live.cls);
		// same glyph, different colour: stopped vs offline
		expect(html("stopped").shape).toBe(html("offline").shape);
		expect(html("stopped").cls).not.toBe(html("offline").cls);
	});

	test("className is appended to the icon", () => {
		render(<StatusIcon kind="live" className="size-5" />);
		expect(svg()).toHaveClass("size-5");
	});

	describe("completion check", () => {
		let timers: Array<{ fn: () => void; ms: number; live: boolean }>;
		const realST = window.setTimeout;
		const realCT = window.clearTimeout;
		beforeEach(() => {
			timers = [];
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
			window.setTimeout = realST;
			window.clearTimeout = realCT;
		});

		const wrapper = () => root().firstElementChild as HTMLElement;

		test("an in-flight -> settled change plays the check once, then drops it", () => {
			const { rerender } = render(<StatusIcon kind="deploying" />);
			expect(wrapper()).not.toHaveClass("t-success-check");
			rerender(<StatusIcon kind="live" />);
			expect(wrapper()).toHaveClass("t-success-check");
			expect(wrapper()).toHaveAttribute("data-state", "in");
			const timer = timers.find((t) => t.live);
			// verySlow 500 + micro 80 + 100
			expect(timer?.ms).toBe(680);
			act(() => timer?.fn());
			expect(wrapper()).not.toHaveClass("t-success-check");
			expect(wrapper()).not.toHaveAttribute("data-state");
		});

		test.each([
			["building", "healthy"],
			["verifying", "live"],
		] as const)("%s -> %s also celebrates", (from, to) => {
			const { rerender } = render(<StatusIcon kind={from} />);
			rerender(<StatusIcon kind={to} />);
			expect(wrapper()).toHaveClass("t-success-check");
		});

		test("mounting already settled never animates", () => {
			render(<StatusIcon kind="live" />);
			expect(wrapper()).not.toHaveClass("t-success-check");
			expect(timers).toHaveLength(0);
		});

		test.each([
			["deploying", "failed"],
			["live", "building"],
			["pending", "live"],
			["live", "healthy"],
		] as const)("%s -> %s does not celebrate", (from, to) => {
			const { rerender } = render(<StatusIcon kind={from} />);
			rerender(<StatusIcon kind={to} />);
			expect(wrapper()).not.toHaveClass("t-success-check");
		});

		test("changing away from the settled kind mid-check cancels the check", () => {
			const { rerender } = render(<StatusIcon kind="building" />);
			rerender(<StatusIcon kind="live" />);
			expect(wrapper()).toHaveClass("t-success-check");
			rerender(<StatusIcon kind="failed" />);
			expect(wrapper()).not.toHaveClass("t-success-check");
		});

		test("unmounting mid-check clears the pending timer", () => {
			const { rerender, unmount } = render(<StatusIcon kind="building" />);
			rerender(<StatusIcon kind="live" />);
			expect(timers.some((t) => t.live)).toBe(true);
			unmount();
			expect(timers.every((t) => !t.live)).toBe(true);
		});
	});
});
