import { describe, expect, mock, spyOn, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { DitherGauge, type DitherGaugeProps } from "./gauge";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

const gauge = (props: Partial<DitherGaugeProps> = {}) => (
	<DitherGauge label="CPU" value={40} {...props} />
);
const meter = () => screen.getByRole("meter", { name: "CPU" });
const blocks = (c: HTMLElement) => [...c.querySelectorAll("svg > path")];
const filled = (c: HTMLElement) =>
	blocks(c).filter((p) => p.hasAttribute("data-block"));

describe("DitherGauge accessibility", () => {
	test("is a meter with min, max, now and a spoken value", () => {
		render(gauge({ unit: "%", min: 0, max: 100 }));
		expect(meter()).toHaveAttribute("aria-valuemin", "0");
		expect(meter()).toHaveAttribute("aria-valuemax", "100");
		expect(meter()).toHaveAttribute("aria-valuenow", "40");
		expect(meter()).toHaveAttribute("aria-valuetext", "40% of 100%");
	});

	test("the value now is clamped to the range; the spoken text keeps the real value", () => {
		const { unmount } = render(gauge({ value: 250, max: 100 }));
		expect(meter()).toHaveAttribute("aria-valuenow", "100");
		expect(meter()).toHaveAttribute("aria-valuetext", "250 of 100");
		unmount();
		render(gauge({ value: -5, min: 0 }));
		expect(meter()).toHaveAttribute("aria-valuenow", "0");
	});

	test("a NaN value falls back to the minimum", () => {
		render(gauge({ value: Number.NaN, min: 10, max: 50 }));
		expect(meter()).toHaveAttribute("aria-valuenow", "10");
	});

	test("shows value, unit and label as text; the drawing is hidden", () => {
		const { container } = render(gauge({ value: 1234, unit: " ms" }));
		expect(meter()).toHaveTextContent("1.2K");
		expect(meter()).toHaveTextContent("ms");
		expect(meter()).toHaveTextContent("CPU");
		expect(container.querySelector("svg")).toHaveAttribute(
			"aria-hidden",
			"true",
		);
	});

	test("format shapes value and spoken text", () => {
		render(gauge({ value: 0.256, max: 1, format: { style: "percent" } }));
		expect(meter()).toHaveAttribute("aria-valuetext", "26% of 100%");
	});
});

describe("DitherGauge thresholds", () => {
	test("below warnAt it is ok: no status word", () => {
		render(gauge({ warnAt: 60, badAt: 80 }));
		expect(meter().getAttribute("aria-valuetext")).toBe("40 of 100");
		expect(meter()).not.toHaveTextContent("Warning");
	});

	test("crossing warnAt says Warning ≥ and recolours the fill", () => {
		const { container } = render(gauge({ value: 70, warnAt: 60, badAt: 80 }));
		expect(meter()).toHaveTextContent("Warning ≥ 60");
		expect(meter().getAttribute("aria-valuetext")).toBe(
			"70 of 100, Warning ≥ 60",
		);
		expect(
			filled(container).some(
				(p) => p.getAttribute("fill") === "var(--color-warn)",
			),
		).toBe(true);
	});

	test("crossing badAt says Critical and uses the bad colour", () => {
		const { container } = render(
			gauge({ value: 95, unit: "%", warnAt: 60, badAt: 80 }),
		);
		expect(meter()).toHaveTextContent("Critical ≥ 80%");
		expect(
			filled(container).some(
				(p) => p.getAttribute("fill") === "var(--color-bad)",
			),
		).toBe(true);
	});

	test("badAt below warnAt means lower is worse and says ≤", () => {
		render(gauge({ value: 15, warnAt: 30, badAt: 10 }));
		expect(meter()).toHaveTextContent("Warning ≤ 30");
		render(gauge({ value: 5, warnAt: 30, badAt: 10 }));
		expect(screen.getAllByRole("meter", { name: "CPU" })[1]).toHaveTextContent(
			"Critical ≤ 10",
		);
	});

	test("a notch is drawn at each threshold, in its status colour", () => {
		const { container } = render(gauge({ warnAt: 60, badAt: 80 }));
		const notches = [...container.querySelectorAll("svg > line")];
		expect(notches.map((l) => l.getAttribute("stroke"))).toEqual([
			"var(--color-warn)",
			"var(--color-bad)",
		]);
	});

	test("no thresholds, no notches", () => {
		const { container } = render(gauge());
		expect(container.querySelectorAll("svg > line")).toHaveLength(0);
	});
});

describe("DitherGauge geometry", () => {
	test("segments default by shape; segments prop is clamped to 6..90", () => {
		const count = (props: Partial<DitherGaugeProps>) => {
			const { container, unmount } = render(gauge(props));
			const n = blocks(container).length;
			unmount();
			return n;
		};
		expect(count({})).toBe(24);
		expect(count({ variant: "ring" })).toBe(48);
		expect(count({ segments: 2 })).toBe(6);
		expect(count({ segments: 500 })).toBe(90);
		expect(count({ segments: 10 })).toBe(10);
	});

	test("the blocks up to the value are filled; the one holding it is solid, the rest track", () => {
		const { container } = render(
			gauge({ value: 50, segments: 10, color: "#336699" }),
		);
		const all = blocks(container);
		expect(filled(container)).toHaveLength(5);
		expect(all[4]?.getAttribute("fill")).toBe("#336699");
		for (const p of all.slice(5))
			expect(p.getAttribute("fill")).toBe("var(--dither-track)");
		// Behind the value the blocks are dithered patterns.
		for (const p of all.slice(0, 4))
			expect(p.getAttribute("fill")).toMatch(/^url\(#/);
	});

	test("zero fills nothing; max fills everything", () => {
		const zero = render(gauge({ value: 0, segments: 10 }));
		expect(filled(zero.container)).toHaveLength(0);
		zero.unmount();
		const full = render(gauge({ value: 100, segments: 10 }));
		expect(filled(full.container)).toHaveLength(10);
	});

	test("min and max rescale the value", () => {
		const { container } = render(
			gauge({ value: 150, min: 100, max: 200, segments: 10 }),
		);
		expect(filled(container)).toHaveLength(5);
	});

	test("an empty or inverted range fills nothing", () => {
		const { container } = render(
			gauge({ value: 5, min: 10, max: 10, segments: 10 }),
		);
		expect(filled(container)).toHaveLength(0);
	});

	test("trackColor tints the empty blocks", () => {
		const { container } = render(
			gauge({ value: 10, segments: 10, trackColor: "#ff0000" }),
		);
		expect(blocks(container).at(-1)?.getAttribute("fill")).toBe(
			"color-mix(in oklab, #ff0000 20%, transparent)",
		);
	});

	test("fill variants: solid paints filled blocks in the colour, dotted one pattern, gradient several", () => {
		const fills = (props: Partial<DitherGaugeProps>) => {
			const { container, unmount } = render(
				gauge({ value: 80, segments: 10, color: "#112233", ...props }),
			);
			const out = new Set(
				filled(container)
					.slice(0, -1)
					.map((p) => p.getAttribute("fill")),
			);
			unmount();
			return out;
		};
		expect([...fills({ fill: "solid" })]).toEqual(["#112233"]);
		expect(fills({ fill: "dotted" }).size).toBe(1);
		expect([...fills({ fill: "dotted" })][0]).toMatch(/^url\(#/);
		expect(fills({ fill: "gradient" }).size).toBeGreaterThan(1);
	});

	test("tiny cells use opacity steps of the colour instead of patterns", () => {
		const { container } = render(
			gauge({ value: 80, segments: 10, color: "#112233", cell: 1 }),
		);
		const behind = filled(container).slice(0, -1);
		for (const p of behind) {
			expect(p.getAttribute("fill")).toBe("#112233");
			expect(Number(p.getAttribute("fill-opacity"))).toBeLessThan(1);
		}
	});

	test("showRange draws min and max labels under the ends, but never for a ring", () => {
		const { container } = render(gauge({ showRange: true, min: 0, max: 200 }));
		const labels = [...container.querySelectorAll("svg > text")].map(
			(t) => t.textContent,
		);
		expect(labels).toEqual(["0", "200"]);
		const ring = render(gauge({ showRange: true, variant: "ring" }));
		expect(ring.container.querySelectorAll("svg > text")).toHaveLength(0);
	});

	test("ticks: true marks round values; an array marks the given ones inside the range", () => {
		const auto = render(gauge({ ticks: true }));
		const autoLabels = [...auto.container.querySelectorAll("svg g > text")].map(
			(t) => t.textContent,
		);
		expect(autoLabels).toContain("0");
		expect(autoLabels).toContain("100");
		auto.unmount();
		const custom = render(gauge({ ticks: [0, 25, 250] }));
		expect(
			[...custom.container.querySelectorAll("svg g > text")].map(
				(t) => t.textContent,
			),
		).toEqual(["0", "25"]);
		custom.unmount();
		const none = render(gauge({ ticks: false }));
		expect(none.container.querySelectorAll("svg g > text")).toHaveLength(0);
	});

	test("size is the width and shrinks to the container; the shape sets the height", () => {
		const { container } = render(gauge({ size: 160, variant: "half" }));
		const root = meter();
		expect(root.style.width).toBe("160px");
		expect(container.querySelector("svg")).toHaveAttribute("width", "160");
		env.resize(120);
		expect(container.querySelector("svg")).toHaveAttribute("width", "120");
		const half = Number(container.querySelector("svg")?.getAttribute("height"));
		expect(half).toBeLessThan(120);
	});

	test("bloom glows behind the filled blocks only", () => {
		const off = render(gauge());
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const { container } = render(
			gauge({ value: 50, segments: 10, bloom: "low" }),
		);
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
		expect(container.querySelectorAll("g[filter] path")).toHaveLength(5);
	});
});

describe("DitherGauge entrance", () => {
	const motion = () =>
		spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: false,
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);

	test("filled blocks are hidden until seen, then fade in one by one", () => {
		const mm = motion();
		const animate = mock(() => ({ cancel: () => {} }) as unknown as Animation);
		const proto = spyOn(Element.prototype, "animate").mockImplementation(
			animate as unknown as Element["animate"],
		);
		const { container } = render(gauge({ value: 50, segments: 10 }));
		expect(
			filled(container).every((p) => (p as SVGElement).style.opacity === "0"),
		).toBe(true);
		expect(animate).not.toHaveBeenCalled();
		act(() => env.intersect(true));
		expect(animate).toHaveBeenCalledTimes(5);
		const delays = (
			animate.mock.calls as unknown as [unknown, { delay: number }][]
		).map(([, o]) => o.delay);
		expect(delays).toEqual([0, 18, 36, 54, 72]);
		expect(
			filled(container).every((p) => (p as SVGElement).style.opacity === ""),
		).toBe(true);
		proto.mockRestore();
		mm.mockRestore();
	});

	test("when the value rises only the new blocks animate", () => {
		const mm = motion();
		const animate = mock(() => ({ cancel: () => {} }) as unknown as Animation);
		const proto = spyOn(Element.prototype, "animate").mockImplementation(
			animate as unknown as Element["animate"],
		);
		const { rerender } = render(gauge({ value: 50, segments: 10 }));
		act(() => env.intersect(true));
		animate.mockClear();
		rerender(gauge({ value: 80, segments: 10 }));
		expect(animate).toHaveBeenCalledTimes(3);
		animate.mockClear();
		rerender(gauge({ value: 30, segments: 10 }));
		expect(animate).not.toHaveBeenCalled();
		proto.mockRestore();
		mm.mockRestore();
	});

	test("animate={false} never hides the blocks", () => {
		const mm = motion();
		const { container } = render(
			gauge({ animate: false, value: 50, segments: 10 }),
		);
		expect(
			filled(container).every((p) => (p as SVGElement).style.opacity === ""),
		).toBe(true);
		mm.mockRestore();
	});
});
