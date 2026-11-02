import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	type CartesianContext,
	DitherCartesianChart,
	type DitherCartesianChartProps,
} from "./cartesian-chart";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

interface Row {
	day: string;
	a: number;
	b: number;
}
const rows: Row[] = [
	{ day: "Mon", a: 10, b: 5 },
	{ day: "Tue", a: 40, b: 10 },
	{ day: "Wed", a: 20, b: 15 },
];
const one = [{ label: "Builds", value: "a" as const }] as const;
const two = [
	{ label: "Builds", value: "a" as const },
	{ label: "Deploys", value: "b" as const, dashed: true },
] as const;

type Props = DitherCartesianChartProps<Row>;
const chart = (props: Partial<Props> = {}) => (
	<DitherCartesianChart
		label="CI"
		data={rows}
		x="day"
		series={one}
		{...props}
	/>
);

const svgOf = (c: HTMLElement) => {
	const svg = c.querySelector("svg");
	if (!svg) throw new Error("no svg");
	return svg;
};
const axis = (c: HTMLElement) =>
	[...svgOf(c).querySelectorAll("text")].map((t) => t.textContent);
const stage = () => screen.getByRole("group", { name: "CI" });

let spy: { mockRestore: () => void } | undefined;
let rafSpy: { mockRestore: () => void } | undefined;
afterEach(() => {
	spy?.mockRestore();
	rafSpy?.mockRestore();
});
/** Frames run at once, so a pointer move lands within the same act. */
beforeEach(() => {
	rafSpy = spyOn(globalThis, "requestAnimationFrame").mockImplementation(
		(cb: FrameRequestCallback) => {
			cb(0);
			return 0;
		},
	);
});
const media = (query: string) => {
	spy = spyOn(window, "matchMedia").mockImplementation(
		(q: string) =>
			({
				matches: q === query,
				media: q,
				addEventListener: () => {},
				removeEventListener: () => {},
			}) as unknown as MediaQueryList,
	);
};

describe("DitherCartesianChart context", () => {
	const capture = (props: Partial<Props> = {}) => {
		let ctx: CartesianContext | null = null;
		const view = render(
			chart({
				children: (c) => {
					ctx = c;
					return <g data-testid="marks" />;
				},
				...props,
			}),
		);
		return {
			view,
			get ctx() {
				return ctx as CartesianContext | null;
			},
		};
	};

	test("hands layers the measured size, scale, values and colours", () => {
		const { ctx } = capture({ series: two, colors: ["#aabbcc", "#ddeeff"] });
		expect(ctx).not.toBeNull();
		expect(ctx?.width).toBe(480);
		expect(ctx?.height).toBe(220);
		expect(ctx?.count).toBe(3);
		expect(ctx?.values).toEqual([
			[10, 40, 20],
			[5, 10, 15],
		]);
		expect(ctx?.colors).toEqual(["#aabbcc", "#ddeeff"]);
		expect(ctx?.scale.min).toBe(0);
		expect(ctx?.scale.max).toBeGreaterThanOrEqual(40);
		expect(ctx?.focus).toBeNull();
	});

	test("point layout puts the first datum on the plot's left and the last on its right", () => {
		const { ctx } = capture();
		const c = ctx as CartesianContext;
		expect(c.xAt(0)).toBe(c.plot.left);
		expect(c.xAt(2)).toBe(c.plot.right);
		expect(c.xAt(1)).toBeCloseTo((c.plot.left + c.plot.right) / 2, 5);
		expect(c.band).toBeCloseTo((c.plot.right - c.plot.left) / 2, 5);
	});

	test("band layout centres each datum in its own slot", () => {
		const { ctx } = capture({ layout: "band" });
		const c = ctx as CartesianContext;
		expect(c.band).toBeCloseTo((c.plot.right - c.plot.left) / 3, 5);
		expect(c.xAt(0)).toBeCloseTo(c.plot.left + c.band / 2, 5);
		expect(c.plot.right).toBe(480);
	});

	test("yAt maps the scale floor to the baseline and its ceiling to the top", () => {
		const { ctx } = capture();
		const c = ctx as CartesianContext;
		expect(c.yAt(c.scale.min)).toBe(c.baseline);
		expect(c.yAt(c.scale.max)).toBe(c.plot.top);
		expect(c.yAt(20)).toBeLessThan(c.yAt(10));
	});

	test("non-finite values read as 0", () => {
		const { ctx } = capture({
			data: [
				{ day: "Mon", a: Number.NaN, b: 1 },
				{ day: "Tue", a: Number.POSITIVE_INFINITY, b: 1 },
				{ day: "Wed", a: 3, b: 1 },
			],
		});
		expect(ctx?.values[0]).toEqual([0, 0, 3]);
	});

	test("domain forces the value range; zero=false fits the data", () => {
		const domain = capture({ domain: [0, 100] });
		expect(domain.ctx?.scale.max).toBe(100);
		domain.view.unmount();
		const zero = capture({
			zero: false,
			data: [
				{ day: "Mon", a: 100, b: 0 },
				{ day: "Tue", a: 110, b: 0 },
			],
		});
		expect(zero.ctx?.scale.min).toBeGreaterThan(0);
	});

	test("a ready-made scale wins over the data", () => {
		const { ctx } = capture({
			scale: { min: 0, max: 500, ticks: [0, 250, 500] },
		});
		expect(ctx?.scale.max).toBe(500);
	});

	test("no context (and no marks) until the element is wide enough", () => {
		env.width = 10;
		const { view } = capture();
		expect(view.container.querySelector("svg")).toBeNull();
		expect(screen.queryByTestId("marks")).toBeNull();
		env.resize(300);
		expect(screen.getByTestId("marks")).toBeInTheDocument();
		expect(svgOf(view.container)).toHaveAttribute("width", "300");
	});

	test("without children the chart is only frame, axes and legend", () => {
		const { container } = render(chart());
		expect(axis(container)).toContain("Mon");
		expect(container.querySelectorAll("path")).toHaveLength(0);
	});

	test("cell prop wins and is carried as the layer's dither cell; below 1.5px layers draw solid", () => {
		const big = capture({ cell: 6 });
		expect(big.ctx?.cell).toBe(6);
		expect(big.ctx?.solid).toBe(false);
		big.view.unmount();
		const tiny = capture({ cell: 1 });
		expect(tiny.ctx?.cell).toBe(1);
		expect(tiny.ctx?.solid).toBe(true);
	});

	test("forced colours make layers draw solid", () => {
		media("(forced-colors: active)");
		expect(capture().ctx?.solid).toBe(true);
	});

	test("the dither cell defaults to 3px", () => {
		expect(capture().ctx?.cell).toBe(3);
	});

	test("spotlighting a legend chip sets the focus index and dims the others", async () => {
		const user = userEvent.setup();
		const out = capture({ series: two });
		await user.hover(screen.getByRole("button", { name: "Spotlight Deploys" }));
		expect(out.ctx?.focus).toBe(1);
		expect(out.ctx?.opacityOf(1)).toBe(1);
		expect(out.ctx?.opacityOf(0)).toBeLessThan(1);
		await user.unhover(
			screen.getByRole("button", { name: "Spotlight Deploys" }),
		);
		expect(out.ctx?.focus).toBeNull();
		expect(out.ctx?.opacityOf(0)).toBe(1);
	});

	test("interactive={false} ignores the spotlight", async () => {
		const out = capture({ series: two, interactive: false });
		expect(
			screen.queryByRole("button", { name: "Spotlight Deploys" }),
		).toBeNull();
		expect(out.ctx?.focus).toBeNull();
	});
});

describe("DitherCartesianChart axes", () => {
	test("tick labels use tickFormat, falling back to format; the readout uses format", async () => {
		const user = userEvent.setup();
		const { container } = render(
			chart({ format: (v) => `${v} ms`, tickFormat: (v) => `${v}!` }),
		);
		expect(axis(container)).toContain("0!");
		expect(axis(container)).not.toContain("0 ms");
		await user.click(stage());
		await user.keyboard("{Home}");
		expect(screen.getByRole("status", { hidden: true })).toHaveTextContent(
			"10 ms",
		);
	});

	test("ticks use format when tickFormat is absent, with Intl options too", () => {
		const { container } = render(chart({ format: { style: "percent" } }));
		expect(axis(container)).toContain("0%");
	});

	test("compact format by default", () => {
		const { container } = render(
			chart({
				data: [
					{ day: "Mon", a: 4000, b: 0 },
					{ day: "Tue", a: 1000, b: 0 },
				],
			}),
		);
		expect(axis(container)).toContain("4K");
	});

	test("grid draws one line per tick, the floor solid and the rest dashed", () => {
		const { container } = render(chart());
		const lines = [
			...svgOf(container).querySelectorAll("line.stroke-line-faint"),
		];
		const ticks = axis(container).filter((t) => /^\d+$/.test(t ?? ""));
		expect(lines).toHaveLength(ticks.length);
		expect(lines[0]).not.toHaveAttribute("stroke-dasharray");
		expect(lines[1]).toHaveAttribute("stroke-dasharray", "3 3");
	});

	test("grid={false} keeps tick labels but drops the lines", () => {
		const { container } = render(chart({ grid: false }));
		expect(
			svgOf(container).querySelectorAll("line.stroke-line-faint"),
		).toHaveLength(0);
		expect(axis(container)).toContain("0");
	});

	test("axes={false} drops labels and grid and runs the plot edge to edge", () => {
		let ctx: CartesianContext | null = null;
		const { container } = render(
			chart({
				axes: false,
				children: (c) => {
					ctx = c;
					return null;
				},
			}),
		);
		expect(axis(container)).toEqual([]);
		expect((ctx as unknown as CartesianContext).plot.left).toBe(0);
		expect((ctx as unknown as CartesianContext).plot.right).toBe(480);
	});

	test("x labels: first anchors start, last end, in a point layout; band labels centre", () => {
		const point = render(chart());
		const anchors = () =>
			[...svgOf(point.container).querySelectorAll("text")]
				.filter((t) => ["Mon", "Tue", "Wed"].includes(t.textContent ?? ""))
				.map((t) => [t.textContent, t.getAttribute("text-anchor")]);
		expect(anchors()).toEqual([
			["Mon", "start"],
			["Tue", "middle"],
			["Wed", "end"],
		]);
		point.unmount();
		const band = render(chart({ layout: "band" }));
		const bandAnchors = [...svgOf(band.container).querySelectorAll("text")]
			.filter((t) => ["Mon", "Tue", "Wed"].includes(t.textContent ?? ""))
			.map((t) => t.getAttribute("text-anchor"));
		expect(bandAnchors).toEqual(["middle", "middle", "middle"]);
	});

	test("x accessor may be a function", () => {
		const { container } = render(chart({ x: (r) => r.day.toUpperCase() }));
		expect(axis(container)).toContain("MON");
	});
});

describe("DitherCartesianChart accessibility", () => {
	test("interactive: a chart group with key help and a hidden table", () => {
		render(chart({ series: two }));
		expect(stage()).toHaveAttribute("aria-roledescription", "chart");
		expect(stage()).toHaveAttribute("tabindex", "0");
		expect(
			document.getElementById(stage().getAttribute("aria-describedby") ?? ""),
		).toHaveTextContent("Use the arrow keys");
		const table = screen.getByRole("table");
		expect(
			within(table)
				.getAllByRole("columnheader")
				.map((h) => h.textContent),
		).toEqual(["x", "Builds", "Deploys"]);
		const tue = within(table).getByRole("row", { name: /Tue/ });
		expect(
			within(tue)
				.getAllByRole("cell")
				.map((c) => c.textContent),
		).toEqual(["40", "10"]);
	});

	test("interactive={false}: an image with summary, no focus stop, no readout, no key help", () => {
		const { container } = render(chart({ series: two, interactive: false }));
		const img = screen.getByRole("img", { name: "CI. Builds, Deploys." });
		expect(img).not.toHaveAttribute("tabindex");
		expect(screen.queryByRole("group")).toBeNull();
		expect(screen.queryByText(/Use the arrow keys/)).toBeNull();
		expect(container.querySelector("[aria-live]")).toBeNull();
		expect(container.querySelector("line[stroke-dasharray='2 3']")).toBeNull();
	});

	test("the svg is hidden from assistive tech", () => {
		const { container } = render(chart());
		expect(svgOf(container)).toHaveAttribute("aria-hidden", "true");
	});

	test("empty data: no focus stop and an empty table body", () => {
		render(chart({ data: [] }));
		expect(stage()).toHaveAttribute("tabindex", "-1");
		expect(screen.getAllByRole("row")).toHaveLength(1);
	});

	test("duplicate x labels still list every row", () => {
		render(
			chart({
				data: [
					{ day: "Mon", a: 1, b: 1 },
					{ day: "Mon", a: 2, b: 1 },
				],
			}),
		);
		expect(screen.getAllByRole("row")).toHaveLength(3);
	});

	test("a custom readout replaces the rows in the table, readout and live region", async () => {
		const user = userEvent.setup();
		const { container } = render(
			chart({
				readout: (i) => [
					{
						label: "Total",
						color: "red",
						value: `sum ${(rows[i]?.a ?? 0) + (rows[i]?.b ?? 0)}`,
					},
				],
			}),
		);
		expect(
			within(screen.getByRole("table"))
				.getAllByRole("columnheader")
				.map((h) => h.textContent),
		).toEqual(["x", "Total"]);
		await user.click(stage());
		await user.keyboard("{Home}");
		expect(screen.getByRole("status", { hidden: true })).toHaveTextContent(
			"sum 15",
		);
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"Mon: Total sum 15",
		);
	});
});

describe("DitherCartesianChart scrubbing", () => {
	test("focus shows the readout for the last datum", async () => {
		const user = userEvent.setup();
		render(chart());
		await user.tab();
		expect(stage()).toHaveFocus();
		expect(screen.getByRole("status", { hidden: true })).toHaveTextContent(
			"Wed",
		);
		expect(screen.getByRole("status", { hidden: true })).toHaveTextContent(
			"20",
		);
	});

	test("arrow keys, Home, End and Escape move through the data and clear", async () => {
		const user = userEvent.setup();
		const { container } = render(chart({ series: two }));
		const live = () => container.querySelector("[aria-live='polite']");
		await user.tab();
		await user.keyboard("{ArrowLeft}");
		expect(live()).toHaveTextContent("Tue: Builds 40, Deploys 10");
		await user.keyboard("{Home}");
		expect(live()).toHaveTextContent("Mon: Builds 10, Deploys 5");
		await user.keyboard("{ArrowLeft}");
		expect(live()).toHaveTextContent("Mon:");
		await user.keyboard("{End}");
		expect(live()).toHaveTextContent("Wed: Builds 20, Deploys 15");
		await user.keyboard("{Escape}");
		expect(live()).toHaveTextContent("");
		expect(
			screen.getByRole("status", { hidden: true }).parentElement,
		).toHaveStyle({
			opacity: "0",
		});
	});

	test("pointer moves select the nearest datum in a point layout and leave hides the readout", () => {
		const { container } = render(chart());
		const live = () => container.querySelector("[aria-live='polite']");
		fireEvent.pointerMove(stage(), { clientX: 9999 });
		expect(live()).toHaveTextContent("Wed:");
		fireEvent.pointerMove(stage(), { clientX: 0 });
		expect(live()).toHaveTextContent("Mon:");
		fireEvent.pointerLeave(stage());
		expect(live()).toHaveTextContent("");
	});

	test("in a band layout a pointer picks the slot it is inside", () => {
		const { container } = render(chart({ layout: "band" }));
		const live = () => container.querySelector("[aria-live='polite']");
		// plot spans ~[left, 480]; the far right slot is Wed.
		fireEvent.pointerMove(stage(), { clientX: 479 });
		expect(live()).toHaveTextContent("Wed:");
		fireEvent.pointerMove(stage(), { clientX: 36 });
		expect(live()).toHaveTextContent("Mon:");
	});

	test("the crosshair line follows the scrubbed datum and hides after Escape", async () => {
		const user = userEvent.setup();
		const { container } = render(chart());
		const line = () =>
			svgOf(container).querySelector(
				"line[stroke-dasharray='2 3']",
			) as SVGLineElement;
		expect(line().style.opacity).toBe("0");
		await user.tab();
		await user.keyboard("{Home}");
		expect(line().style.opacity).toBe("1");
		const first = line().style.transform;
		await user.keyboard("{End}");
		expect(line().style.transform).not.toBe(first);
		await user.keyboard("{Escape}");
		expect(line().style.opacity).toBe("0");
	});

	test("crosshair='column' draws a track rect, 'none' draws nothing; band layouts default to column", () => {
		const col = render(chart({ crosshair: "column" }));
		expect(svgOf(col.container).querySelector("rect")).not.toBeNull();
		expect(
			svgOf(col.container).querySelector("line[stroke-dasharray='2 3']"),
		).toBeNull();
		col.unmount();
		const none = render(chart({ crosshair: "none" }));
		expect(svgOf(none.container).querySelector("rect")).toBeNull();
		expect(
			svgOf(none.container).querySelector("line[stroke-dasharray='2 3']"),
		).toBeNull();
		none.unmount();
		const band = render(chart({ layout: "band" }));
		expect(svgOf(band.container).querySelector("rect")).not.toBeNull();
	});

	test("crosshair markers carry the series colours", async () => {
		const user = userEvent.setup();
		const { container } = render(
			chart({ series: two, colors: ["#112233", "#445566"] }),
		);
		await user.tab();
		const circles = [...svgOf(container).querySelectorAll("circle")];
		expect(circles.map((c) => c.getAttribute("fill"))).toEqual([
			"#112233",
			"#445566",
		]);
		expect(
			circles.every((c) => (c as SVGCircleElement).style.opacity === "1"),
		).toBe(true);
	});

	test("pointsAt places the markers; activeMarks draws extra marks only for the scrubbed datum", async () => {
		const user = userEvent.setup();
		const { container } = render(
			chart({
				pointsAt: (i) => [{ seriesIndex: 0, y: 50 + i }],
				activeMarks: (i) => <rect data-testid="active" data-i={i} />,
			}),
		);
		expect(screen.queryByTestId("active")).toBeNull();
		await user.tab();
		await user.keyboard("{Home}");
		expect(screen.getByTestId("active")).toHaveAttribute("data-i", "0");
		const circle = svgOf(container).querySelector("circle") as SVGCircleElement;
		expect(circle.style.transform).toContain(", 50px)");
		await user.keyboard("{ArrowRight}");
		expect(screen.getByTestId("active")).toHaveAttribute("data-i", "1");
		expect(circle.style.transform).toContain(", 51px)");
	});

	test("tooltip={false} removes the readout card but keeps the live region", async () => {
		const user = userEvent.setup();
		const { container } = render(chart({ tooltip: false }));
		await user.tab();
		await user.keyboard("{Home}");
		expect(screen.queryByRole("status", { hidden: true })).toBeNull();
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"Mon:",
		);
	});

	test("the readout card positions itself next to the crosshair", async () => {
		const user = userEvent.setup();
		render(chart());
		await user.tab();
		await user.keyboard("{Home}");
		const card = screen.getByRole("status", { hidden: true })
			.parentElement as HTMLElement;
		expect(card.style.transform).toMatch(
			/^translate3d\(-?\d+px, -?\d+px, 0\)$/,
		);
		expect(card.style.opacity).toBe("1");
	});

	test("blur hides the scrub", async () => {
		const user = userEvent.setup();
		const { container } = render(chart());
		await user.tab();
		await user.tab();
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"",
		);
	});

	test("Escape also lets go of a locked legend spotlight", async () => {
		const user = userEvent.setup();
		render(chart({ series: two }));
		const chip = screen.getByRole("button", { name: "Spotlight Deploys" });
		await user.click(chip);
		expect(chip).toHaveAttribute("aria-pressed", "true");
		stage().focus();
		await user.keyboard("{Escape}");
		expect(chip).toHaveAttribute("aria-pressed", "false");
	});

	test("an unrelated key leaves the scrub alone", async () => {
		const user = userEvent.setup();
		const { container } = render(chart());
		await user.tab();
		await user.keyboard("{Home}");
		await user.keyboard("q");
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"Mon:",
		);
	});

	test("repeated moves over the same datum are stable", () => {
		const { container } = render(chart());
		fireEvent.pointerMove(stage(), { clientX: 0 });
		fireEvent.pointerMove(stage(), { clientX: 1 });
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"Mon:",
		);
	});

	test("pointer moves are ignored without data or when decorative", () => {
		const empty = render(chart({ data: [] }));
		fireEvent.pointerMove(stage(), { clientX: 10 });
		expect(
			empty.container.querySelector("[aria-live='polite']"),
		).toHaveTextContent("");
		empty.unmount();
		const still = render(chart({ interactive: false }));
		fireEvent.pointerMove(screen.getByRole("img"), { clientX: 10 });
		expect(still.container.querySelector("[aria-live]")).toBeNull();
	});
});

describe("DitherCartesianChart legend", () => {
	test("legend defaults on for two series and honours the override", () => {
		const a = render(chart());
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
		a.unmount();
		const b = render(chart({ series: two }));
		expect(screen.getByRole("list", { name: "Legend" })).toBeInTheDocument();
		b.unmount();
		const c = render(chart({ legend: true }));
		expect(screen.getByRole("list", { name: "Legend" })).toBeInTheDocument();
		c.unmount();
		render(chart({ series: two, legend: false }));
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
	});

	test("a decorative chart's legend is a plain list, not spotlight buttons", () => {
		render(chart({ series: two, interactive: false }));
		const legend = screen.getByRole("list", { name: "Legend" });
		expect(within(legend).queryAllByRole("button")).toHaveLength(0);
		expect(within(legend).getByText("Deploys")).toBeInTheDocument();
	});
});

describe("DitherCartesianChart motion", () => {
	test("the entrance plan is armed until seen, then plays; off under animate={false}", () => {
		media("full motion");
		let ctx: CartesianContext | null = null;
		const props = {
			children: (c: CartesianContext) => {
				ctx = c;
				return null;
			},
		};
		const { rerender } = render(chart(props));
		expect(ctx as unknown as CartesianContext).toMatchObject({
			entrance: { armed: true, go: false },
		});
		act(() => env.intersect(true));
		expect(ctx as unknown as CartesianContext).toMatchObject({
			entrance: { armed: true, go: true },
		});
		rerender(chart({ ...props, animate: false }));
		expect(ctx as unknown as CartesianContext).toMatchObject({
			entrance: { armed: false, go: false },
		});
	});

	test("reduced motion never arms the entrance", () => {
		media("(prefers-reduced-motion: reduce)");
		let ctx: CartesianContext | null = null;
		render(
			chart({
				children: (c) => {
					ctx = c;
					return null;
				},
			}),
		);
		expect((ctx as unknown as CartesianContext).entrance.armed).toBe(false);
	});

	test("the crosshair only glides between positions when motion is allowed", async () => {
		media("full motion");
		const { container } = render(chart());
		const line = () =>
			svgOf(container).querySelector(
				"line[stroke-dasharray='2 3']",
			) as SVGLineElement;
		fireEvent.pointerMove(stage(), { clientX: 0 });
		expect(line().style.transition).not.toContain("transform");
		fireEvent.pointerMove(stage(), { clientX: 9999 });
		expect(line().style.transition).toContain("transform 200ms");
	});

	test("reduced motion makes the crosshair jump", () => {
		media("(prefers-reduced-motion: reduce)");
		const { container } = render(chart());
		const line = svgOf(container).querySelector(
			"line[stroke-dasharray='2 3']",
		) as SVGLineElement;
		fireEvent.pointerMove(stage(), { clientX: 0 });
		fireEvent.pointerMove(stage(), { clientX: 9999 });
		expect(line.style.transition).not.toContain("transform");
	});
});
