import { describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DitherRadarChart, type DitherRadarChartProps } from "./radar-chart";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

const axes = ["CPU", "Mem", "Disk", "Net"];
const a = { label: "Prod", values: [50, 100, 25, 75] };
const b = { label: "Stage", values: { CPU: 10, Mem: 20, Disk: 30, Net: 40 } };

const radar = (props: Partial<DitherRadarChartProps> = {}) =>
	(
		<DitherRadarChart label="Capacity" axes={axes} series={[a]} {...props} />
	) as React.ReactElement;

const svgOf = (c: HTMLElement) => c.querySelector("svg") as SVGSVGElement;
const live = (c: HTMLElement) => c.querySelector("[aria-live='polite']");
const keys = () =>
	screen.getByRole("button", { name: /Use Left and Right to inspect axes/ });
const polygons = (c: HTMLElement) =>
	[...c.querySelectorAll("path[fill='none'][stroke]")] as SVGPathElement[];
const vertices = (c: HTMLElement) => [
	...c.querySelectorAll<SVGCircleElement>("circle"),
];
const mm = (matches: string | null) =>
	spyOn(window, "matchMedia").mockImplementation(
		(q: string) =>
			({
				matches: q === matches,
				media: q,
				addEventListener: () => {},
				removeEventListener: () => {},
			}) as unknown as MediaQueryList,
	);

describe("DitherRadarChart geometry", () => {
	test("axis 0 points up and the rest follow clockwise; vertices sit at value / max of the radius", () => {
		const { container } = render(radar());
		// px 280, pad 42 -> centre 140, radius 98; max rounds 100 up to 100.
		const v = vertices(container).map((c) => [
			Number(c.getAttribute("cx")),
			Number(c.getAttribute("cy")),
		]);
		expect(v[0]?.[0]).toBeCloseTo(140, 5);
		expect(v[0]?.[1]).toBeCloseTo(140 - 49, 5);
		expect(v[1]?.[0]).toBeCloseTo(140 + 98, 5);
		expect(v[1]?.[1]).toBeCloseTo(140, 5);
		expect(v[2]?.[0]).toBeCloseTo(140, 5);
		expect(v[2]?.[1]).toBeCloseTo(140 + 24.5, 5);
		expect(v[3]?.[0]).toBeCloseTo(140 - 73.5, 5);
	});

	test("the scale labels run from max/rings to max on rings", () => {
		const { container } = render(radar({ rings: 4 }));
		const scale = [...svgOf(container).querySelectorAll("text")]
			.map((t) => t.textContent)
			.filter((t) => /^\d+$/.test(t ?? ""));
		expect(scale).toEqual(["25", "50", "75", "100"]);
	});

	test("max overrides the data's top and clamps larger values to the outer ring", () => {
		const { container } = render(radar({ max: 50 }));
		const v = vertices(container);
		// Mem 100 > 50 sits on the outer ring; CPU 50 does too.
		expect(Number(v[1]?.getAttribute("cx"))).toBeCloseTo(238, 5);
		expect(Number(v[0]?.getAttribute("cy"))).toBeCloseTo(42, 5);
		expect(Number(v[2]?.getAttribute("cy"))).toBeCloseTo(140 + 49, 5);
	});

	test("rings are clamped to 3..5; circle grids use arcs", () => {
		const ringCount = (props: Partial<DitherRadarChartProps>) => {
			const { container, unmount } = render(radar(props));
			const n = container.querySelectorAll("path.stroke-line-faint").length;
			unmount();
			return n;
		};
		expect(ringCount({ rings: 1 })).toBe(3);
		expect(ringCount({ rings: 9 })).toBe(5);
		expect(ringCount({})).toBe(4);
		const circle = render(radar({ grid: "circle" }));
		expect(
			circle.container
				.querySelector("path.stroke-line-faint")
				?.getAttribute("d"),
		).toContain("A");
		circle.unmount();
		const poly = render(radar({ grid: "polygon" }));
		expect(
			poly.container.querySelector("path.stroke-line-faint")?.getAttribute("d"),
		).not.toContain("A");
	});

	test("axis labels are drawn once each and extra axes beyond ten are ignored", () => {
		const many = Array.from({ length: 14 }, (_, i) => `A${i}`);
		const { container } = render(
			radar({
				axes: many,
				series: [{ label: "S", values: many.map((_, i) => i) }],
			}),
		);
		const labels = [...svgOf(container).querySelectorAll("text")]
			.map((t) => t.textContent)
			.filter((t) => t?.startsWith("A"));
		expect(labels).toHaveLength(10);
		expect(vertices(container)).toHaveLength(10);
		expect(within(screen.getByRole("table")).getAllByRole("row")).toHaveLength(
			11,
		);
	});

	test("values may be an array (padded or cut to the axes) or a record by axis name", () => {
		render(
			radar({
				series: [
					{ label: "Short", values: [1, 2] },
					{ label: "Long", values: [1, 2, 3, 4, 5, 6] },
					b,
				],
			}),
		);
		const table = screen.getByRole("table");
		const rowOf = (name: string) =>
			within(within(table).getByRole("row", { name: new RegExp(`^${name}`) }))
				.getAllByRole("cell")
				.map((c) => c.textContent);
		expect(rowOf("CPU")).toEqual(["1", "1", "10"]);
		expect(rowOf("Disk")).toEqual(["0", "3", "30"]);
		expect(rowOf("Net")).toEqual(["0", "4", "40"]);
	});

	test("non-finite and negative values do not break the polygon", () => {
		const { container } = render(
			radar({
				series: [
					{
						label: "S",
						values: [Number.NaN, -5, Number.POSITIVE_INFINITY, 50],
					},
				],
			}),
		);
		expect(container.innerHTML).not.toContain("NaN");
		const v = vertices(container);
		expect(Number(v[0]?.getAttribute("cy"))).toBeCloseTo(140, 5);
	});

	test("no data at all still draws a grid with a max of 1", () => {
		const { container } = render(
			radar({ series: [{ label: "S", values: [0, 0, 0, 0] }] }),
		);
		expect(container.innerHTML).not.toContain("NaN");
		expect(
			[...svgOf(container).querySelectorAll("text")].map((t) => t.textContent),
		).toContain("1");
	});

	test("size is capped by the container and never below 160", () => {
		const { container } = render(radar({ size: 200 }));
		expect(svgOf(container)).toHaveAttribute("width", "200");
		env.resize(120);
		expect(svgOf(container)).toHaveAttribute("width", "160");
	});

	test("format shapes the scale, the readout and the table", () => {
		const { container } = render(radar({ format: (v) => `${v}%` }));
		expect(
			[...svgOf(container).querySelectorAll("text")].map((t) => t.textContent),
		).toContain("100%");
		expect(
			within(screen.getByRole("row", { name: /^Mem/ })).getByRole("cell"),
		).toHaveTextContent("100%");
	});

	test("fill variants: flat default, gradient bands per ring, dotted, solid tint", () => {
		const fillsOf = (props: Partial<DitherRadarChartProps>) => {
			const { container, unmount } = render(radar(props));
			const g = container.querySelector("svg g[style] ");
			const out = [...(g?.querySelectorAll("path:not([stroke])") ?? [])].map(
				(p) => ({
					fill: p.getAttribute("fill"),
					clip: p.getAttribute("clip-path"),
					opacity: p.getAttribute("fill-opacity"),
				}),
			);
			unmount();
			return out;
		};
		const flat = fillsOf({});
		expect(flat).toHaveLength(1);
		expect(flat[0]?.fill).toMatch(/^url\(#/);
		const bands = fillsOf({ fill: "gradient", rings: 4 });
		expect(bands).toHaveLength(4);
		expect(new Set(bands.map((b) => b.fill)).size).toBe(4);
		for (const band of bands) expect(band.clip).toMatch(/^url\(#.+-\d\)$/);
		expect(fillsOf({ fill: "dotted" })[0]?.fill).toMatch(/^url\(#/);
		const solid = fillsOf({ fill: "solid" });
		expect(solid[0]?.fill).toBe("var(--dither-series-1)");
		expect(solid[0]?.opacity).toBe("0.3");
		const tiny = fillsOf({ cell: 1 });
		expect(tiny[0]?.opacity).toBe("0.2");
	});

	test("a series' own fill wins over the chart's", () => {
		const { container } = render(
			radar({
				fill: "solid",
				series: [{ ...a, fill: "dotted" }, b],
			}),
		);
		const fills = [
			...container.querySelectorAll("svg g[style] > path:first-child"),
		].map((p) => p.getAttribute("fill"));
		expect(fills[0]).toMatch(/^url\(#/);
		expect(fills[1]).toBe("var(--dither-series-2)");
	});

	test("grid='dither' fills the rings, denser toward the centre", () => {
		const { container } = render(radar({ grid: "dither", rings: 3 }));
		const filled = [...svgOf(container).querySelectorAll("path")]
			.filter((p) => p.getAttribute("fill")?.startsWith("url("))
			.slice(0, 3);
		expect(filled).toHaveLength(3);
		expect(new Set(filled.map((p) => p.getAttribute("fill"))).size).toBe(3);
		const plain = render(radar({ grid: "line", rings: 3 }));
		const urls = [...plain.container.querySelectorAll("svg > path")].filter(
			(p) => p.getAttribute("fill")?.startsWith("url("),
		);
		expect(urls).toHaveLength(0);
	});

	test("colors by position and a series' own color win", () => {
		const { container } = render(
			radar({
				series: [a, { ...b, color: "#102030" }],
				colors: ["#abcdef", "#ffffff"],
			}),
		);
		expect(polygons(container).map((p) => p.getAttribute("stroke"))).toEqual([
			"#abcdef",
			"#102030",
		]);
	});

	test("bloom adds a blurred copy behind the polygons", () => {
		const off = render(radar());
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const { container } = render(radar({ bloom: "high" }));
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
	});
});

describe("DitherRadarChart accessibility", () => {
	test("the interactive chart is a group around one keyboard button; the drawing is hidden", () => {
		const { container } = render(radar({ series: [a, b] }));
		expect(screen.getByRole("group", { name: "Capacity" })).toBeInTheDocument();
		expect(keys().getAttribute("aria-describedby")).toBeTruthy();
		expect(svgOf(container)).toHaveAttribute("aria-hidden", "true");
	});

	test("the data table lists axes by series", () => {
		render(radar({ series: [a, b] }));
		const table = screen.getByRole("table");
		expect(
			within(table)
				.getAllByRole("columnheader")
				.map((h) => h.textContent),
		).toEqual(["Axis", "Prod", "Stage"]);
		expect(
			within(within(table).getByRole("row", { name: /Disk/ }))
				.getAllByRole("cell")
				.map((c) => c.textContent),
		).toEqual(["25", "30"]);
	});

	test("interactive={false}: an image naming axes and series, no button, no live region", () => {
		const { container } = render(radar({ series: [a, b], interactive: false }));
		expect(
			screen.getByRole("img", {
				name: "Capacity. CPU, Mem, Disk, Net. Prod, Stage.",
			}),
		).toBeInTheDocument();
		expect(screen.queryAllByRole("button")).toHaveLength(0);
		expect(live(container)).toBeNull();
		fireEvent.pointerMove(svgOf(container), { clientX: 240, clientY: 140 });
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("legend defaults on for two or more series, and can be forced", () => {
		const one = render(radar());
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
		one.unmount();
		const two = render(radar({ series: [a, b] }));
		expect(screen.getByRole("list", { name: "Legend" })).toBeInTheDocument();
		two.unmount();
		render(radar({ series: [a, b], legend: false }));
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
	});
});

describe("DitherRadarChart interaction", () => {
	test("Left/Right scrub the axes, wrap around, and the readout lists every series on that axis", async () => {
		const user = userEvent.setup();
		const { container } = render(radar({ series: [a, b] }));
		await user.tab();
		await user.keyboard("{ArrowRight}");
		let status = screen.getByRole("status");
		expect(status).toHaveTextContent("CPU");
		expect(status).toHaveTextContent("Prod50");
		expect(status).toHaveTextContent("Stage10");
		expect(live(container)).toHaveTextContent("CPU: Prod 50, Stage 10");
		await user.keyboard("{ArrowLeft}");
		expect(screen.getByRole("status")).toHaveTextContent("Net");
		await user.keyboard("{ArrowRight}");
		expect(screen.getByRole("status")).toHaveTextContent("CPU");
		await user.keyboard("{End}");
		status = screen.getByRole("status");
		expect(status).toHaveTextContent("Net");
		expect(status).toHaveTextContent("Prod75");
		await user.keyboard("{Home}");
		expect(screen.getByRole("status")).toHaveTextContent("CPU");
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("status")).toBeNull();
		expect(live(container)).toHaveTextContent("");
	});

	test("ArrowLeft with nothing picked starts at the last axis", async () => {
		const user = userEvent.setup();
		render(radar());
		await user.tab();
		await user.keyboard("{ArrowLeft}");
		expect(screen.getByRole("status")).toHaveTextContent("Net");
	});

	test("Up/Down pick a series, wrap, dim the others, and the readout lists its axes", async () => {
		const user = userEvent.setup();
		const { container } = render(radar({ series: [a, b] }));
		await user.tab();
		await user.keyboard("{ArrowDown}");
		expect(screen.getByRole("status")).toHaveTextContent("Prod");
		expect(screen.getByRole("status")).toHaveTextContent("Mem100");
		const opacities = () =>
			[
				...container.querySelectorAll<SVGGElement>(
					"svg g[style]:not([style*='transform-origin'])",
				),
			].map((g) => g.style.opacity);
		expect(opacities().sort()).toEqual(["0.3", "1"]);
		await user.keyboard("{ArrowDown}");
		expect(screen.getByRole("status")).toHaveTextContent("Stage");
		await user.keyboard("{ArrowDown}");
		expect(screen.getByRole("status")).toHaveTextContent("Prod");
		await user.keyboard("{ArrowUp}");
		expect(screen.getByRole("status")).toHaveTextContent("Stage");
		await user.keyboard("{Escape}");
		expect(opacities()).toEqual(["1", "1"]);
	});

	test("ArrowUp with nothing picked starts at the last series", async () => {
		const user = userEvent.setup();
		render(radar({ series: [a, b] }));
		await user.tab();
		await user.keyboard("{ArrowUp}");
		expect(screen.getByRole("status")).toHaveTextContent("Stage");
	});

	test("the highlighted series is drawn last so it sits on top, with a thicker outline", async () => {
		const user = userEvent.setup();
		const { container } = render(radar({ series: [a, b] }));
		await user.tab();
		await user.keyboard("{ArrowDown}");
		const order = polygons(container).map((p) => [
			p.getAttribute("stroke"),
			p.getAttribute("stroke-width"),
		]);
		expect(order).toEqual([
			["var(--dither-series-2)", "1.5"],
			["var(--dither-series-1)", "2"],
		]);
	});

	test("unrelated keys do nothing", async () => {
		const user = userEvent.setup();
		render(radar());
		await user.tab();
		await user.keyboard("q");
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("blur clears the scrub", async () => {
		const user = userEvent.setup();
		render(radar());
		await user.tab();
		await user.keyboard("{ArrowRight}");
		fireEvent.blur(keys());
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("pointer position picks the nearest axis around the centre; leaving clears", () => {
		const { container } = render(radar());
		const svg = svgOf(container);
		const at = (x: number, y: number) => {
			fireEvent.pointerMove(svg, { clientX: x, clientY: y });
			return screen.getByRole("status").textContent;
		};
		expect(at(140, 40)).toContain("CPU");
		expect(at(240, 140)).toContain("Mem");
		expect(at(140, 240)).toContain("Disk");
		expect(at(40, 140)).toContain("Net");
		fireEvent.pointerLeave(svg);
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("the axis under the pointer is emphasised with a stronger spoke and bigger dots", () => {
		const { container } = render(radar());
		fireEvent.pointerMove(svgOf(container), { clientX: 240, clientY: 140 });
		const spokes = [...svgOf(container).querySelectorAll("line")];
		expect(spokes.map((l) => l.getAttribute("class"))).toEqual([
			"stroke-line-faint",
			"stroke-line-strong",
			"stroke-line-faint",
			"stroke-line-faint",
		]);
		expect(vertices(container).map((c) => c.getAttribute("r"))).toEqual([
			"2.5",
			"3.5",
			"2.5",
			"2.5",
		]);
	});

	test("hovering a polygon picks that series", () => {
		const { container } = render(radar({ series: [a, b] }));
		const groups = container.querySelectorAll(
			"svg g[style]:not([style*='transform-origin'])",
		);
		fireEvent.pointerEnter(groups[1] as Element);
		expect(screen.getByRole("status")).toHaveTextContent("Stage");
		fireEvent.pointerLeave(groups[1] as Element);
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("legend: hovering a chip spotlights its series; clicking locks it", async () => {
		const user = userEvent.setup();
		render(radar({ series: [a, b] }));
		const stage = screen.getByRole("button", { name: "Spotlight Stage" });
		await user.hover(stage);
		expect(screen.getByRole("status")).toHaveTextContent("Stage");
		await user.unhover(stage);
		expect(screen.queryByRole("status")).toBeNull();
		await user.click(stage);
		await user.unhover(stage);
		expect(stage).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByRole("status")).toHaveTextContent("Stage");
	});

	test("legendAction=toggle hides a series but keeps the last one", async () => {
		const user = userEvent.setup();
		const { container } = render(
			radar({ series: [a, b], legendAction: "toggle" }),
		);
		expect(polygons(container)).toHaveLength(2);
		await user.click(screen.getByRole("button", { name: /Stage/ }));
		expect(polygons(container)).toHaveLength(1);
		await user.click(screen.getByRole("button", { name: /Prod/ }));
		expect(polygons(container)).toHaveLength(1);
		await user.click(screen.getByRole("button", { name: /Stage/ }));
		expect(polygons(container)).toHaveLength(2);
	});

	test("with every polygon hidden keys do nothing for series picking", async () => {
		const user = userEvent.setup();
		render(radar({ axes: [], series: [{ label: "S", values: [] }] }));
		await user.tab();
		await user.keyboard("{ArrowRight}{Home}{End}");
		expect(screen.queryByRole("status")).toBeNull();
	});
});

describe("DitherRadarChart motion", () => {
	test("polygons are hidden until seen when motion is allowed, then grow", () => {
		const m = mm(null);
		const { container } = render(radar());
		const wrap = () =>
			container.querySelector(
				"svg g[style*='transform-origin']",
			) as SVGGElement;
		expect(wrap().style.opacity).toBe("0");
		act(() => env.intersect(true));
		expect(wrap().style.opacity).toBe("");
		m.mockRestore();
	});

	test("animate={false} and reduced motion never hide it", () => {
		const m = mm(null);
		const off = render(radar({ animate: false }));
		expect(
			(
				off.container.querySelector(
					"svg g[style*='transform-origin']",
				) as SVGGElement
			).style.opacity,
		).toBe("");
		off.unmount();
		m.mockRestore();
		const reduced = render(radar());
		expect(
			(
				reduced.container.querySelector(
					"svg g[style*='transform-origin']",
				) as SVGGElement
			).style.opacity,
		).toBe("");
	});

	test("forced colours draw the fill as a tint, not a pattern", () => {
		const m = mm("(forced-colors: active)");
		const { container } = render(radar());
		const fill = container.querySelector("svg g[style] > path");
		expect(fill?.getAttribute("fill")).toBe("var(--dither-series-1)");
		expect(fill?.getAttribute("fill-opacity")).toBe("0.2");
		m.mockRestore();
	});
});
