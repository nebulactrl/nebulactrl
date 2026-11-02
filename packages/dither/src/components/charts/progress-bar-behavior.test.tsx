import { describe, expect, spyOn, test } from "bun:test";
import { act, render, screen, within } from "@testing-library/react";
import { DitherProgressBar, type DitherProgressBarProps } from "./progress-bar";

type Props = {
	[K in keyof DitherProgressBarProps]?: DitherProgressBarProps[K] | undefined;
};
const bar = (props: Props = {}) => {
	const all = {
		label: "Upload",
		value: 40,
		...props,
	} as DitherProgressBarProps;
	return <DitherProgressBar {...all} />;
};
const edges = (c: HTMLElement) => [
	...c.querySelectorAll<HTMLElement>("[data-dither-edge]"),
];
const fillOf = (c: HTMLElement) => edges(c)[0] as HTMLElement;
const progress = () => screen.getByRole("progressbar", { name: "Upload" });

describe("DitherProgressBar semantics", () => {
	test("a progressbar with min, max, now and a percentage valuetext", () => {
		render(bar());
		expect(progress()).toHaveAttribute("aria-valuemin", "0");
		expect(progress()).toHaveAttribute("aria-valuemax", "100");
		expect(progress()).toHaveAttribute("aria-valuenow", "40");
		expect(progress()).toHaveAttribute("aria-valuetext", "40%");
		expect(screen.getByText("Upload")).toBeInTheDocument();
		expect(screen.getByText("40%")).toBeInTheDocument();
	});

	test("max rescales the percentage; valuenow is capped at max", () => {
		render(bar({ value: 5, max: 20 }));
		expect(progress()).toHaveAttribute("aria-valuemax", "20");
		expect(progress()).toHaveAttribute("aria-valuetext", "25%");
		const over = render(bar({ value: 500, label: "Over" }));
		expect(within(over.container).getByRole("progressbar")).toHaveAttribute(
			"aria-valuenow",
			"100",
		);
		expect(within(over.container).getByRole("progressbar")).toHaveAttribute(
			"aria-valuetext",
			"100%",
		);
	});

	test("negative and zero maxima read as 0%", () => {
		render(bar({ value: -10 }));
		expect(progress()).toHaveAttribute("aria-valuetext", "0%");
		const zeroMax = render(bar({ label: "Z", value: 5, max: 0 }));
		expect(within(zeroMax.container).getByRole("progressbar")).toHaveAttribute(
			"aria-valuetext",
			"0%",
		);
	});

	test("valueText replaces the percentage in the readout and for screen readers", () => {
		render(bar({ valueText: "3 of 7 files" }));
		expect(progress()).toHaveAttribute("aria-valuetext", "3 of 7 files");
		expect(screen.getByText("3 of 7 files")).toBeInTheDocument();
	});

	test("hideLabel drops the visible row but keeps the accessible name", () => {
		render(bar({ hideLabel: true }));
		expect(screen.queryByText("40%")).toBeNull();
		expect(progress()).toBeInTheDocument();
	});

	test("no value is indeterminate: no valuenow, a comet and 'Working'", () => {
		const { container } = render(bar({ value: undefined }));
		expect(progress()).not.toHaveAttribute("aria-valuenow");
		expect(progress()).toHaveAttribute("aria-valuetext", "Working");
		expect(container.querySelector("[data-dither-comet]")).not.toBeNull();
		expect(screen.getByText("Working")).toBeInTheDocument();
	});

	test("null is indeterminate too, and a determinate bar has no comet", () => {
		const nul = render(bar({ value: null }));
		expect(nul.container.querySelector("[data-dither-comet]")).not.toBeNull();
		nul.unmount();
		const { container } = render(bar());
		expect(container.querySelector("[data-dither-comet]")).toBeNull();
	});

	test("a zero value draws an empty bar with no fill", () => {
		const { container } = render(bar({ value: 0 }));
		expect(fillOf(container).querySelector("[data-dither-fill]")).toBeNull();
	});

	test("inline renders spans, never divs, so it fits inside a button", () => {
		const { container } = render(
			<button type="button">{bar({ inline: true })}</button>,
		);
		expect(container.querySelector("div")).toBeNull();
		expect(screen.getByRole("progressbar").tagName).toBe("SPAN");
	});

	test("stale dims the bar", () => {
		const { container } = render(bar({ stale: true }));
		expect((container.firstChild as HTMLElement).className).toContain(
			"opacity-50",
		);
	});

	test("size sets the track height", () => {
		const heights = (["sm", "md", "lg"] as const).map((size) => {
			const { unmount } = render(bar({ size }));
			const h = progress().style.height;
			unmount();
			return h;
		});
		expect(heights).toEqual(["8px", "10px", "14px"]);
	});
});

describe("DitherProgressBar thresholds", () => {
	test("warnAt and badAt add a state word and recolour", () => {
		const warn = render(
			bar({ value: 70, warnAt: 60, badAt: 90, color: "#336699" }),
		);
		expect(progress()).toHaveAttribute("aria-valuetext", "70%, high");
		expect(screen.getByText("high")).toBeInTheDocument();
		expect(
			fillOf(warn.container).querySelector<HTMLElement>("[data-dither-fill]")
				?.style.backgroundColor,
		).toBe("var(--color-warn)");
		warn.unmount();
		const bad = render(bar({ value: 95, warnAt: 60, badAt: 90 }));
		expect(progress()).toHaveAttribute("aria-valuetext", "95%, critical");
		expect(
			fillOf(bad.container).querySelector<HTMLElement>("[data-dither-fill]")
				?.style.backgroundColor,
		).toBe("var(--color-bad)");
	});

	test("below the thresholds nothing is added", () => {
		render(bar({ value: 10, warnAt: 60, badAt: 90 }));
		expect(progress()).toHaveAttribute("aria-valuetext", "10%");
		expect(screen.queryByText("high")).toBeNull();
	});

	test("an indeterminate bar never warns", () => {
		render(bar({ value: undefined, warnAt: 0, badAt: 0 }));
		expect(progress()).toHaveAttribute("aria-valuetext", "Working");
	});

	test("markers are decorative ticks placed by percent and clamped to the track", () => {
		const { container } = render(
			bar({
				markers: [
					{ percent: 50, title: "Alert at 50%" },
					{ percent: 150, title: "Way over", warn: true },
					{ percent: -20, title: "Below" },
				],
			}),
		);
		const ticks = [...container.querySelectorAll<HTMLElement>("[title]")];
		expect(ticks.map((t) => t.title)).toEqual([
			"Alert at 50%",
			"Way over",
			"Below",
		]);
		expect(ticks.map((t) => t.style.left)).toEqual([
			"calc(50% - 1px)",
			"calc(100% - 1px)",
			"calc(0% - 1px)",
		]);
		expect(ticks.every((t) => t.getAttribute("aria-hidden") === "true")).toBe(
			true,
		);
		expect(ticks[1]?.className).toContain("bg-warn");
	});
});

describe("DitherProgressBar fills", () => {
	test("the fill width follows the percentage (scaled by --dither-grow)", () => {
		const { container } = render(bar({ value: 40, animate: false }));
		expect(fillOf(container).style.width).toBe(
			"calc(40% * var(--dither-grow, 1))",
		);
	});

	test("fill variants: gradient stacks five layers, dotted one, solid a flat colour", () => {
		const layers = (props: Props) => {
			const { container, unmount } = render(
				bar({ color: "#112233", ...props }),
			);
			const n = fillOf(container).querySelectorAll("[data-dither-fill]").length;
			unmount();
			return n;
		};
		// layers + the leading cap
		expect(layers({})).toBe(6);
		expect(layers({ fill: "dotted" })).toBe(2);
		expect(layers({ fill: "hatched" })).toBe(2);
		expect(layers({ fill: "solid" })).toBe(2);
		expect(layers({ cell: 1 })).toBe(2);
	});

	test("the solid and tiny-cell fills are flat colour, patterns are masks", () => {
		const first = (props: Props) => {
			const { container, unmount } = render(
				bar({ color: "#112233", ...props }),
			);
			const el = fillOf(container).querySelector<HTMLElement>(
				"[data-dither-fill]",
			) as HTMLElement;
			const out = {
				bg: el.style.backgroundColor,
				masked:
					el.style.maskImage || el.style.getPropertyValue("-webkit-mask-image"),
			};
			unmount();
			return out;
		};
		expect(first({ fill: "solid" }).bg).toBe("#112233");
		expect(first({ fill: "solid" }).masked).toBeFalsy();
		expect(first({}).bg).toBe("#112233");
		expect(first({}).masked).toBeTruthy();
	});

	test("the cap on the leading edge is at least 2px wide and follows the cell", () => {
		const cap = (cell?: number) => {
			const { container, unmount } = render(bar(cell ? { cell } : {}));
			const fills =
				fillOf(container).querySelectorAll<HTMLElement>("[data-dither-fill]");
			const w = fills[fills.length - 1]?.style.width;
			unmount();
			return w;
		};
		expect(cap()).toBe("2px");
		expect(cap(5)).toBe("5px");
	});

	test("a bloom adds a blurred glow under the fill; off adds none", () => {
		const off = render(bar());
		expect(
			off.container.querySelectorAll("[aria-hidden][style*='blur']"),
		).toHaveLength(0);
		off.unmount();
		const { container } = render(bar({ bloom: "high" }));
		const glow = container.querySelector<HTMLElement>("[style*='blur']");
		expect(glow).not.toBeNull();
		expect(glow?.style.filter).toContain("blur(");
	});
});

describe("DitherProgressBar parts", () => {
	const parts = [
		{ label: "Images", value: 30, color: "#111111", text: "30 MB" },
		{ label: "Video", value: 50, color: "#222222" },
		{ label: "Other", value: 40, color: "#333333" },
	];

	test("parts stack left to right and are cut at the capacity", () => {
		const { container } = render(
			bar({ value: undefined, parts, animate: false }),
		);
		const [a, b, c] = edges(container);
		expect(a?.style.left).toBe("calc(0% * var(--dither-grow, 1))");
		expect(a?.style.width).toBe("calc(30% * var(--dither-grow, 1))");
		expect(b?.style.left).toBe("calc(30% * var(--dither-grow, 1))");
		expect(b?.style.width).toBe("calc(50% * var(--dither-grow, 1))");
		// 30 + 50 + 40 > 100: the last is cut to 20.
		expect(c?.style.width).toBe("calc(20% * var(--dither-grow, 1))");
		expect(progress()).toHaveAttribute("aria-valuenow", "100");
	});

	test("the legend lists each part with its text or raw value and unit", () => {
		render(bar({ parts, unit: " MB" }));
		const legend = screen.getByRole("list", { name: "Legend" });
		expect(
			within(legend)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual(["Images30 MB", "Video50 MB", "Other40 MB"]);
	});

	test("hideLegend leaves the legend out", () => {
		render(bar({ parts, hideLegend: true }));
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
	});

	test("an empty part stays mounted but hidden", () => {
		const { container } = render(
			bar({
				parts: [
					{ label: "A", value: 10 },
					{ label: "B", value: 0 },
				],
			}),
		);
		expect(edges(container)[1]?.style.visibility).toBe("hidden");
		expect(edges(container)[0]?.style.visibility).toBe("");
	});

	test("a part's own fill wins; gradient defaults parts to dotted", () => {
		const { container } = render(
			bar({
				parts: [
					{ label: "A", value: 10, fill: "solid" },
					{ label: "B", value: 10 },
				],
			}),
		);
		const counts = edges(container).map(
			(e) => e.querySelectorAll("[data-dither-fill]").length,
		);
		// solid: one flat layer + cap; default gradient -> dotted: one mask layer + cap.
		expect(counts).toEqual([2, 2]);
		const masked = edges(container).map(
			(e) =>
				!!e.querySelector<HTMLElement>("[data-dither-fill]")?.style.maskImage,
		);
		expect(masked).toEqual([false, true]);
	});

	test("negative parts count as zero", () => {
		render(
			bar({
				value: undefined,
				parts: [
					{ label: "A", value: -5 },
					{ label: "B", value: 20 },
				],
			}),
		);
		expect(progress()).toHaveAttribute("aria-valuenow", "20");
	});
});

describe("DitherProgressBar segments", () => {
	test("a value fills whole blocks and a fraction of the next", () => {
		const { container } = render(
			bar({ value: 25, segments: 4, animate: false }),
		);
		const blocks = [...container.querySelectorAll("[data-dither-track]")];
		expect(blocks).toHaveLength(4);
		const widths = blocks.map(
			(b) => b.querySelector<HTMLElement>("[data-dither-edge]")?.style.width,
		);
		expect(widths).toEqual([
			"calc(100% * var(--dither-grow, 1))",
			"calc(0% * var(--dither-grow, 1))",
			"calc(0% * var(--dither-grow, 1))",
			"calc(0% * var(--dither-grow, 1))",
		]);
		const partial = render(bar({ value: 37.5, segments: 4, animate: false }));
		const w = [
			...partial.container.querySelectorAll("[data-dither-track]"),
		][1]?.querySelector<HTMLElement>("[data-dither-edge]")?.style.width;
		expect(w).toBe("calc(50% * var(--dither-grow, 1))");
	});

	test("empty blocks are hidden; blocks step in with growing delays", () => {
		const { container } = render(bar({ value: 50, segments: 4 }));
		const spans = [...container.querySelectorAll("[data-dither-track]")].map(
			(b) => b.querySelector<HTMLElement>("[data-dither-edge]"),
		);
		expect(spans.map((s) => s?.style.visibility)).toEqual([
			"",
			"",
			"hidden",
			"hidden",
		]);
		expect(spans[0]?.style.transitionDelay).not.toBe(
			spans[1]?.style.transitionDelay,
		);
	});

	test("parts with segments paint each block by the part that covers it", () => {
		const { container } = render(
			bar({
				value: undefined,
				segments: 4,
				max: 4,
				parts: [
					{ label: "A", value: 1, color: "#111111" },
					{ label: "B", value: 2, color: "#222222" },
				],
			}),
		);
		const perBlock = [...container.querySelectorAll("[data-dither-track]")].map(
			(b) => b.querySelectorAll("[data-dither-edge]").length,
		);
		expect(perBlock).toEqual([1, 1, 1, 0]);
	});
});

describe("DitherProgressBar motion", () => {
	test("animate grows from empty: --dither-grow is 0 until two frames have passed", () => {
		const frames: FrameRequestCallback[] = [];
		const raf = spyOn(globalThis, "requestAnimationFrame").mockImplementation(
			(cb) => {
				frames.push(cb);
				return frames.length;
			},
		);
		const mm = spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: false,
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);
		render(bar());
		expect(progress().style.getPropertyValue("--dither-grow")).toBe("0");
		act(() => frames.shift()?.(0));
		expect(progress().style.getPropertyValue("--dither-grow")).toBe("0");
		act(() => frames.shift()?.(0));
		expect(progress().style.getPropertyValue("--dither-grow")).toBe("1");
		raf.mockRestore();
		mm.mockRestore();
	});

	test("reduced motion paints the final state at once", () => {
		render(bar());
		expect(progress().style.getPropertyValue("--dither-grow")).toBe("1");
	});

	test("animate={false} leaves --dither-grow to the host", () => {
		render(bar({ animate: false }));
		expect(progress().style.getPropertyValue("--dither-grow")).toBe("");
	});

	test("forced colours flatten the fill", () => {
		const mm = spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: q === "(forced-colors: active)",
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);
		const { container } = render(bar());
		expect(
			fillOf(container).querySelectorAll("[data-dither-fill]"),
		).toHaveLength(2);
		mm.mockRestore();
	});

	test("unmounting cancels pending frames", () => {
		const cancel = spyOn(globalThis, "cancelAnimationFrame");
		const mm = spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: false,
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);
		const view = render(bar());
		view.unmount();
		expect(cancel).toHaveBeenCalled();
		cancel.mockRestore();
		mm.mockRestore();
	});

	test("a comet in forced colours is one flat tint", () => {
		const mm = spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: q === "(forced-colors: active)",
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);
		const { container } = render(bar({ value: undefined }));
		// One flat tint plus the leading cap, not the four dither layers.
		const fills = container.querySelectorAll<HTMLElement>(
			"[data-dither-comet] [data-dither-fill]",
		);
		expect(fills).toHaveLength(2);
		expect(fills[0]?.style.opacity).toBe("0.6");
		mm.mockRestore();
	});

	test("the working band is dropped when forced colours flatten the bar", () => {
		const mm = spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: q === "(forced-colors: active)",
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);
		const { container } = render(bar({ activity: "band" }));
		expect(container.querySelector("[data-dither-band]")).toBeNull();
		mm.mockRestore();
	});
});
