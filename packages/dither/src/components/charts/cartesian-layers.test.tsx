import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { type CartesianContext, DitherCartesianChart } from "./cartesian-chart";
import {
	DitherAreaLayer,
	type DitherAreaLayerProps,
	DitherBarLayer,
	type DitherBarLayerProps,
	DitherLineLayer,
	type DitherLineLayerProps,
	highlightIndex,
} from "./cartesian-layers";
import { installChartEnv } from "./test-env";

installChartEnv();

interface Row {
	day: string;
	a: number;
	b: number;
}
const rows: Row[] = [
	{ day: "Mon", a: 10, b: 5 },
	{ day: "Tue", a: 40, b: 10 },
	{ day: "Wed", a: 20, b: 15 },
	{ day: "Thu", a: 30, b: 20 },
];
const series = [
	{ label: "A", value: "a" as const },
	{ label: "B", value: "b" as const },
] as const;

/** A chart whose only marks are the layer under test; returns the chart's context too. */
function mount(
	layer: (ctx: CartesianContext) => React.ReactNode,
	extra: {
		layout?: "point" | "band";
		cell?: number;
		bloom?: "off" | "low" | "high";
		colors?: string[];
	} = {},
) {
	let ctx: CartesianContext | null = null;
	const view = render(
		<DitherCartesianChart
			label="CI"
			data={rows}
			x="day"
			series={series}
			animate={false}
			{...extra}
		>
			{(c) => {
				ctx = c;
				return layer(c);
			}}
		</DitherCartesianChart>,
	);
	const marks = view.container.querySelector(
		"svg > g[aria-hidden]",
	) as SVGGElement;
	return { ...view, marks, ctx: ctx as unknown as CartesianContext };
}

const lastNum = (d: string | null | undefined) =>
	Number(d?.match(/-?\d+(\.\d+)?/g)?.at(-1));

describe("highlightIndex", () => {
	const values = [3, 9, 4];
	test("none/undefined pick nothing", () => {
		expect(highlightIndex(undefined, values)).toBeNull();
		expect(highlightIndex("none", values)).toBeNull();
	});
	test("a number picks that index when it exists", () => {
		expect(highlightIndex(1, values)).toBe(1);
		expect(highlightIndex(3, values)).toBeNull();
		expect(highlightIndex(-1, values)).toBeNull();
	});
	test("last and max", () => {
		expect(highlightIndex("last", values)).toBe(2);
		expect(highlightIndex("max", values)).toBe(1);
		expect(highlightIndex("max", [5, 5])).toBe(0);
	});
	test("last and max of nothing", () => {
		expect(highlightIndex("last", [])).toBeNull();
		expect(highlightIndex("max", [])).toBeNull();
	});
});

describe("DitherAreaLayer", () => {
	const area =
		(props: Partial<DitherAreaLayerProps> = {}) =>
		(c: CartesianContext) => <DitherAreaLayer ctx={c} index={0} {...props} />;

	test("draws the series line in its colour from the first to the last datum", () => {
		const { container, ctx } = mount(area(), {
			colors: ["#112233", "#445566"],
		});
		const line = container.querySelector("path[stroke-width='1.5']");
		expect(line).toHaveAttribute("stroke", "#112233");
		const d = line?.getAttribute("d") ?? "";
		expect(d.startsWith(`M${ctx.xAt(0)} ${ctx.yAt(10)}`)).toBe(true);
		expect(lastNum(d)).toBeCloseTo(ctx.yAt(30), 1);
		expect(d).toContain(`${ctx.xAt(3)} `);
	});

	test("line={false} draws only the fill", () => {
		const { container } = mount(area({ line: false }));
		expect(container.querySelector("path[stroke-width='1.5']")).toBeNull();
		expect(container.querySelector("clipPath")).not.toBeNull();
	});

	test("values override the series' own numbers; lower becomes the fill's bottom edge", () => {
		const { container, ctx } = mount(
			area({ values: [50, 50, 50, 50], lower: [20, 20, 20, 20] }),
		);
		const line = container.querySelector("path[stroke-width='1.5']");
		expect(line?.getAttribute("d")).toContain(` ${ctx.yAt(50)}`);
		const region =
			container.querySelector("clipPath path")?.getAttribute("d") ?? "";
		expect(region).toContain(` ${ctx.yAt(20)}`);
		expect(region).not.toContain(` ${ctx.baseline}L`);
	});

	test("without lower the fill reaches the baseline", () => {
		const { container, ctx } = mount(area());
		const region =
			container.querySelector("clipPath path")?.getAttribute("d") ?? "";
		expect(region).toContain(` ${ctx.baseline}`);
	});

	test("linear and monotone curves differ", () => {
		const d = (curve: "linear" | "smooth") => {
			const { container, unmount } = mount(area({ curve }));
			const out = container
				.querySelector("path[stroke-width='1.5']")
				?.getAttribute("d");
			unmount();
			return out;
		};
		expect(d("linear")).not.toBe(d("smooth"));
	});

	test("fewer than two values draws nothing", () => {
		const { container } = mount(area({ values: [5] }));
		expect(container.querySelector("path")).toBeNull();
	});

	test("fill variants and bloom", () => {
		const gradient = mount(area());
		expect(
			gradient.container.querySelectorAll("g[clip-path] > path"),
		).toHaveLength(7);
		gradient.unmount();
		const dotted = mount(area({ fill: "dotted" }));
		expect(
			dotted.container.querySelectorAll("g[clip-path] > path"),
		).toHaveLength(1);
		dotted.unmount();
		const tiny = mount(area(), { cell: 1 });
		expect(
			tiny.container.querySelector("g[clip-path] > path")?.getAttribute("fill"),
		).toMatch(/^color-mix/);
		tiny.unmount();
		const off = mount(area());
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const chartBloom = mount(area(), { bloom: "low" });
		expect(chartBloom.container.querySelector("feGaussianBlur")).not.toBeNull();
		chartBloom.unmount();
		const layerBloom = mount(area({ bloom: "high" }));
		expect(layerBloom.container.querySelector("feGaussianBlur")).not.toBeNull();
	});

	test("is decorative", () => {
		const { marks } = mount(area());
		expect(marks).toHaveAttribute("aria-hidden", "true");
	});
});

describe("DitherLineLayer", () => {
	const line =
		(props: Partial<DitherLineLayerProps> = {}) =>
		(c: CartesianContext) => <DitherLineLayer ctx={c} index={0} {...props} />;
	const strokes = (c: HTMLElement) => [
		...c.querySelectorAll("path[fill='none']"),
	];

	test("a solid 2px line through every value", () => {
		const { container, ctx } = mount(line({ markers: "none" }));
		const [path] = strokes(container);
		expect(path).toHaveAttribute("stroke-width", "2");
		expect(path).not.toHaveAttribute("stroke-dasharray");
		expect(
			path?.getAttribute("d")?.startsWith(`M${ctx.xAt(0)} ${ctx.yAt(10)}`),
		).toBe(true);
		expect(lastNum(path?.getAttribute("d"))).toBeCloseTo(ctx.yAt(30), 1);
	});

	test("dashed draws a thinner dashed line", () => {
		const { container } = mount(line({ dashed: true, markers: "none" }));
		const [path] = strokes(container);
		expect(path).toHaveAttribute("stroke-dasharray", "5 4");
		expect(path).toHaveAttribute("stroke-width", "1.5");
		expect(path).toHaveAttribute("stroke-linecap", "butt");
	});

	test("null values break the line; an isolated reading becomes a dot", () => {
		const { container, ctx } = mount(
			line({ values: [1, 2, null, 4], markers: "none" }),
		);
		expect(strokes(container)).toHaveLength(1);
		const dots = container.querySelectorAll("circle[r='2']");
		expect(dots).toHaveLength(1);
		expect(Number(dots[0]?.getAttribute("cx"))).toBe(ctx.xAt(3));
		expect(Number(dots[0]?.getAttribute("cy"))).toBe(ctx.yAt(4));
	});

	test("markers: last (default), all, none", () => {
		const count = (markers?: "none" | "all" | "last") => {
			const { container, unmount } = mount(line(markers ? { markers } : {}));
			const n = container.querySelectorAll("circle[r='3']").length;
			unmount();
			return n;
		};
		expect(count()).toBe(1);
		expect(count("last")).toBe(1);
		expect(count("all")).toBe(4);
		expect(count("none")).toBe(0);
	});

	test("the last marker sits on the last value", () => {
		const { container, ctx } = mount(line());
		const marker = container.querySelector("circle[r='3']");
		expect(Number(marker?.getAttribute("cx"))).toBe(ctx.xAt(3));
		expect(Number(marker?.getAttribute("cy"))).toBe(ctx.yAt(30));
	});

	test("a range band is filled between low and high and uses the band colour", () => {
		const { container } = mount(
			line({
				markers: "none",
				range: {
					low: [5, 10, 8, 12],
					high: [15, 45, 30, 35],
					color: "#abcdef",
				},
			}),
		);
		const bands = [...container.querySelectorAll("path")].filter(
			(p) => p.getAttribute("fill") !== "none",
		);
		expect(bands.length).toBeGreaterThan(0);
		for (const b of bands)
			expect(b.getAttribute("fill")).toMatch(/^url\(#|^color-mix/);
	});

	test("range fill variants: gradient/dotted use patterns, solid and tiny cells a tint", () => {
		const fillOf = (props: Partial<DitherLineLayerProps>, extra = {}) => {
			const { container, unmount } = mount(
				line({
					markers: "none",
					range: { low: [1, 2, 3, 4], high: [5, 6, 7, 8] },
					...props,
				}),
				extra,
			);
			const fill = [...container.querySelectorAll("path")]
				.map((p) => p.getAttribute("fill") ?? "")
				.find((f) => f !== "none");
			unmount();
			return fill;
		};
		const gradient = fillOf({});
		const dotted = fillOf({ fill: "dotted" });
		expect(gradient).toMatch(/^url\(#/);
		expect(dotted).toMatch(/^url\(#/);
		expect(dotted).not.toBe(gradient);
		expect(fillOf({ fill: "solid" })).toMatch(/^color-mix/);
		expect(fillOf({}, { cell: 1 })).toMatch(/^color-mix/);
	});

	test("a range with gaps only fills the run where both edges exist", () => {
		const { container, ctx } = mount(
			line({
				markers: "none",
				range: { low: [1, null, 3, 4], high: [5, 6, 7, 8] },
			}),
		);
		const bands = [...container.querySelectorAll("path")].filter(
			(p) => p.getAttribute("fill") !== "none",
		);
		expect(bands).toHaveLength(1);
		const d = bands[0]?.getAttribute("d") ?? "";
		const r1 = (v: number) => Math.round(v * 10) / 10;
		expect(d.startsWith(`M${r1(ctx.xAt(2))} `)).toBe(true);
		expect(d).toContain(`L${r1(ctx.xAt(3))} `);
		expect(d).not.toContain(`${r1(ctx.xAt(0))} `);
	});

	test("bloom adds a glow behind a range band only", () => {
		const withBand = mount(
			line({ range: { low: [1, 2, 3, 4], high: [5, 6, 7, 8] }, bloom: "low" }),
		);
		expect(withBand.container.querySelector("feGaussianBlur")).not.toBeNull();
		withBand.unmount();
		const without = mount(line({ bloom: "low" }));
		expect(without.container.querySelector("feGaussianBlur")).toBeNull();
	});
});

describe("DitherBarLayer", () => {
	const bar =
		(props: Partial<DitherBarLayerProps> = {}) =>
		(c: CartesianContext) => <DitherBarLayer ctx={c} index={0} {...props} />;
	const caps = (c: HTMLElement) =>
		[...c.querySelectorAll("rect[ry], g > rect")].filter(
			(r) =>
				r.getAttribute("fill") !== null &&
				r.getAttribute("fill") !== "transparent",
		);

	test("one capped bar per positive value; cap y follows the scale", () => {
		const { container, ctx } = mount(bar(), { layout: "band" });
		const cs = caps(container);
		expect(cs).toHaveLength(4);
		[10, 40, 20, 30].forEach((v, i) => {
			expect(Number(cs[i]?.getAttribute("y"))).toBeCloseTo(ctx.yAt(v), 5);
			expect(Number(cs[i]?.getAttribute("height"))).toBe(2);
		});
		// Bars are centred on their slot.
		const first = cs[0];
		const centre =
			Number(first?.getAttribute("x")) +
			Number(first?.getAttribute("width")) / 2;
		expect(centre).toBeCloseTo(ctx.xAt(0), 5);
	});

	test("zero, negative and non-finite values draw nothing", () => {
		const { container } = mount(bar({ values: [0, -3, Number.NaN, 5] }), {
			layout: "band",
		});
		expect(caps(container)).toHaveLength(1);
		expect(container.innerHTML).not.toContain("NaN");
	});

	test("maxWidth caps the bar width", () => {
		const { container } = mount(bar({ maxWidth: 10 }), { layout: "band" });
		for (const c of caps(container)) {
			expect(Number(c.getAttribute("width"))).toBeLessThanOrEqual(10);
		}
	});

	test("slots share a band between side-by-side series", () => {
		const left = mount(bar({ slot: { index: 0, count: 2 } }), {
			layout: "band",
		});
		const right = mount(
			(c) => <DitherBarLayer ctx={c} index={1} slot={{ index: 1, count: 2 }} />,
			{ layout: "band" },
		);
		const lx = Number(caps(left.container)[0]?.getAttribute("x"));
		const rx = Number(caps(right.container)[0]?.getAttribute("x"));
		expect(rx).toBeGreaterThan(lx);
		expect(Number(caps(left.container)[0]?.getAttribute("width"))).toBeLessThan(
			left.ctx.band,
		);
	});

	test("lower lifts a bar onto a stack", () => {
		const base = mount(bar({ values: [10, 10, 10, 10] }), { layout: "band" });
		const stacked = mount(
			bar({ values: [10, 10, 10, 10], lower: [10, 10, 10, 10] }),
			{
				layout: "band",
			},
		);
		expect(Number(caps(stacked.container)[0]?.getAttribute("y"))).toBeLessThan(
			Number(caps(base.container)[0]?.getAttribute("y")),
		);
	});

	test("highlight draws that bar solid and labels it", () => {
		const { container, ctx } = mount(
			bar({ highlight: "max", highlightLabel: (i) => `peak ${rows[i]?.a}` }),
			{ layout: "band" },
		);
		const label = container.querySelector("g[aria-hidden] > text");
		expect(label).toHaveTextContent("peak 40");
		expect(Number(label?.getAttribute("y"))).toBe(ctx.yAt(40) - 6);
		expect(Number(label?.getAttribute("x"))).toBeCloseTo(ctx.xAt(1), 5);
		const solid = [...container.querySelectorAll("g[aria-hidden] path")].filter(
			(p) => p.getAttribute("fill") === ctx.colors[0],
		);
		expect(solid).toHaveLength(1);
	});

	test("no highlight, no label", () => {
		const { container } = mount(bar({ highlightLabel: () => "x" }), {
			layout: "band",
		});
		expect(container.querySelector("text.fill-ink-1")).toBeNull();
	});

	test("fill variants: gradient bands, a single dotted pattern, solid tint, forced solid", () => {
		const fills = (props: Partial<DitherBarLayerProps>, extra = {}) => {
			const { container, unmount } = mount(bar(props), {
				layout: "band",
				...extra,
			});
			const out = [...container.querySelectorAll("g[aria-hidden] path")]
				.map((p) => p.getAttribute("fill") ?? "")
				.filter((f) => f !== "" && f !== "none");
			unmount();
			return out;
		};
		const gradient = fills({});
		expect(gradient.length).toBeGreaterThan(3);
		for (const f of gradient) expect(f).toMatch(/^url\(#/);
		expect(fills({ fill: "hatched" })).toHaveLength(1);
		expect(fills({ fill: "solid" })[0]).toMatch(/^color-mix/);
		expect(fills({}, { cell: 1 })[0]).toMatch(/^color-mix/);
	});

	test("bloom glows behind the bars", () => {
		const { container } = mount(bar({ bloom: "low" }), { layout: "band" });
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
	});
});
