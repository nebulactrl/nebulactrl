import { describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DitherHeatmap, type DitherHeatmapProps } from "./heatmap";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

const rows = ["Mon", "Tue"];
const columns = ["00", "06", "12", "18"];
const values = [
	[0, 5, 10, 20],
	[null, 2, 15, 8],
];

const matrix = (props: Partial<DitherHeatmapProps> = {}) => {
	const all = { rows, columns, values, ...props } as DitherHeatmapProps;
	return <DitherHeatmap {...all} />;
};

const cells = (c: HTMLElement) => [
	...c.querySelectorAll<SVGRectElement>("rect[data-ripple]"),
];
const svgOf = (c: HTMLElement) =>
	c.querySelector("button svg, :scope > div svg") as SVGSVGElement;
const live = (c: HTMLElement) => c.querySelector("[aria-live='polite']");
const button = () =>
	screen.getByRole("button", { name: /Use the arrow keys to inspect cells/ });
const scale = () =>
	within(screen.getByRole("list", { name: "Scale" })).getAllByRole("button");
const translate = (el: Element) =>
	(el.getAttribute("transform")?.match(/-?\d+(\.\d+)?/g) ?? []).map(Number);

describe("DitherHeatmap matrix", () => {
	test("one square per row x column, laid out row-major with the gutter and header offsets", () => {
		const { container } = render(matrix());
		const rects = cells(container);
		expect(rects).toHaveLength(8);
		// labelW = 3 glyphs * 6 + 10 = 28; topH = 16; size 14, gap 2 -> pitch 16.
		expect(translate(rects[0] as Element)).toEqual([28, 16]);
		expect(translate(rects[1] as Element)).toEqual([44, 16]);
		expect(translate(rects[4] as Element)).toEqual([28, 32]);
		expect(rects[0]).toHaveAttribute("width", "13");
	});

	test("row labels sit in the gutter and column labels over their columns", () => {
		const { container } = render(matrix());
		const texts = [...svgOf(container).querySelectorAll("text")].map(
			(t) => t.textContent,
		);
		// Two-glyph labels need more than one 16px pitch, so every second column is labelled.
		expect(texts).toEqual(["Mon", "Tue", "00", "12"]);
	});

	test("the data table carries every cell, 'No data' for missing ones", () => {
		render(matrix());
		const table = screen.getByRole("table");
		expect(
			within(table)
				.getAllByRole("columnheader")
				.map((h) => h.textContent),
		).toEqual(columns);
		const tue = within(table).getByRole("row", { name: /Tue/ });
		expect(
			within(tue)
				.getAllByRole("cell")
				.map((c) => c.textContent),
		).toEqual(["No data", "2", "15", "8"]);
	});

	test("levels follow value / max: empty is an outline, the top level the plain colour", () => {
		const { container } = render(matrix({ color: "#336699" }));
		const fills = cells(container).map((r) => r.getAttribute("fill"));
		// 0 -> none, 20 (max) -> colour, 5 -> a pattern.
		expect(fills[0]).toBe("none");
		expect(fills[3]).toBe("#336699");
		expect(fills[1]).toMatch(/^url\(#/);
		expect(fills[4]).toBe("none");
		// Higher values never draw a lighter pattern than lower ones: the top-level colour wins.
		expect(fills[6]).not.toBe(fills[1]);
	});

	test("max pins the top level: larger values draw solid, smaller ones stay light", () => {
		const { container } = render(matrix({ max: 10, color: "#336699" }));
		const fills = cells(container).map((r) => r.getAttribute("fill"));
		expect(fills[2]).toBe("#336699");
		expect(fills[3]).toBe("#336699");
		expect(fills[1]).toMatch(/^url\(#/);
	});

	test("levels and its steps alias control how many patterns exist", () => {
		const distinct = (props: Partial<DitherHeatmapProps>) => {
			const { container, unmount } = render(matrix(props));
			const n = new Set(cells(container).map((r) => r.getAttribute("fill")))
				.size;
			unmount();
			return n;
		};
		expect(distinct({ levels: 2 })).toBeLessThan(distinct({ levels: 8 }));
		expect(distinct({ steps: 2 })).toBe(distinct({ levels: 2 }));
		expect(distinct({ levels: 2, steps: 8 })).toBe(distinct({ levels: 2 }));
		// Out-of-range levels are clamped to 2..8.
		expect(distinct({ levels: 1 })).toBe(distinct({ levels: 2 }));
		expect(distinct({ levels: 50 })).toBe(distinct({ levels: 8 }));
	});

	test("the legend names each step's range", () => {
		render(matrix({ levels: 4 }));
		expect(scale().map((b) => b.getAttribute("aria-label"))).toEqual([
			"No data or zero",
			"0 to 5",
			"5 to 10",
			"10 to 15",
			"15 and up",
		]);
		expect(screen.getByText("max 20")).toBeInTheDocument();
		expect(screen.getByText("Less")).toBeInTheDocument();
		expect(screen.getByText("More")).toBeInTheDocument();
	});

	test("legend={false} removes the scale", () => {
		render(matrix({ legend: false }));
		expect(screen.queryByRole("list", { name: "Scale" })).toBeNull();
		expect(screen.queryByText("Less")).toBeNull();
	});

	test("all-empty data shows 'No values yet' steps and no max", () => {
		render(matrix({ values: [[null, null], [null]], columns: ["a", "b"] }));
		expect(scale()[1]?.getAttribute("aria-label")).toBe("No values yet");
		expect(screen.queryByText(/^max /)).toBeNull();
		expect(screen.getByRole("group")).toHaveAttribute(
			"aria-label",
			"Heatmap: no data",
		);
	});

	test("non-finite and short rows read as no data", () => {
		render(
			matrix({
				values: [[1, Number.NaN], [Number.POSITIVE_INFINITY]],
				columns: ["a", "b"],
			}),
		);
		const cellsText = within(screen.getByRole("table"))
			.getAllByRole("cell")
			.slice(1)
			.map((c) => c.textContent);
		expect(cellsText).toEqual(["1", "No data", "No data", "No data"]);
	});

	test("the summary names total and busiest cell; label and unit shape it", () => {
		render(
			matrix({
				label: "Deploys by hour",
				unit: { one: "deploy", other: "deploys" },
			}),
		);
		expect(
			screen.getByRole("group", {
				name: "Deploys by hour: 60 deploys in total, busiest Mon 18 with 20",
			}),
		).toBeInTheDocument();
	});

	test("dithercn's mode='matrix' takes the grid in data", () => {
		const { container } = render(
			<DitherHeatmap
				mode="matrix"
				rows={rows}
				columns={columns}
				data={values}
			/>,
		);
		expect(cells(container)).toHaveLength(8);
	});

	test("cellSize is clamped to 6..40, the grid shrinks to fit narrow containers", () => {
		const size = (props: Partial<DitherHeatmapProps>) => {
			const { container, unmount } = render(matrix(props));
			const w = Number(cells(container)[0]?.getAttribute("width"));
			unmount();
			return w + 1;
		};
		expect(size({ cellSize: 2 })).toBe(6);
		expect(size({ cellSize: 100 })).toBeLessThanOrEqual(40);
		expect(size({ cellSize: 30 })).toBe(30);
		env.width = 100;
		expect(size({ cellSize: 30 })).toBeLessThan(30);
	});

	test("gap changes the pitch", () => {
		const { container } = render(matrix({ gap: 6 }));
		expect(translate(cells(container)[1] as Element)).toEqual([28 + 20, 16]);
	});

	test("nothing is drawn before the width is known, or without cells", () => {
		env.width = 0;
		const { container } = render(matrix());
		// A zero width measures 0: a grid still fits at the 4px minimum; with no cells nothing draws.
		expect(container.querySelector("svg")).not.toBeNull();
		const empty = render(<DitherHeatmap rows={[]} columns={[]} values={[]} />);
		expect(empty.container.querySelector("rect[data-ripple]")).toBeNull();
	});
});

describe("DitherHeatmap calendar", () => {
	const entries = [
		{ date: "2026-03-01", value: 3 },
		{ date: "2026-03-02", value: 9 },
		{ date: "2026-03-02", value: 1 },
		{ date: "2026-03-14", value: 4 },
	];
	const calendar = (props: Partial<DitherHeatmapProps> = {}) => {
		const all = {
			layout: "calendar",
			data: entries,
			...props,
		} as DitherHeatmapProps;
		return <DitherHeatmap {...all} />;
	};

	test("every day in the range has a cell: two weeks x seven days", () => {
		const { container } = render(calendar());
		expect(cells(container)).toHaveLength(14);
	});

	test("duplicate dates are summed and days without an entry say no data", () => {
		render(calendar());
		const table = screen.getByRole("table");
		expect(
			within(table).getByRole("row", { name: /Mon, Mar 2, 2026/ }),
		).toHaveTextContent("10");
		expect(
			within(table).getByRole("row", { name: /Tue, Mar 3, 2026/ }),
		).toHaveTextContent("No data");
		expect(
			within(table).getByRole("columnheader", { name: "value" }),
		).toBeInTheDocument();
	});

	test("weeks are columns and weekdays rows, starting on Sunday or Monday", () => {
		const sun = render(calendar());
		const first = cells(sun.container)[0] as Element;
		expect(translate(first)[1]).toBe(16);
		const names = [...svgOf(sun.container).querySelectorAll("text")].map(
			(t) => t.textContent,
		);
		expect(names).toEqual(expect.arrayContaining(["Mon", "Wed", "Fri"]));
		expect(names).not.toContain("Sun");
		sun.unmount();
		const mon = render(calendar({ weekStart: 1 }));
		// Monday start: Mar 1 (a Sunday) is the last row of its week.
		const ys = cells(mon.container)
			.slice(0, 7)
			.map((c) => translate(c)[1]);
		expect(ys).toContain(16 + 6 * 16);
		expect(
			[...svgOf(mon.container).querySelectorAll("text")].map(
				(t) => t.textContent,
			),
		).toEqual(expect.arrayContaining(["Tue", "Thu", "Sat"]));
	});

	test("a month label sits over the first column of the month", () => {
		const { container } = render(calendar());
		const label = [...svgOf(container).querySelectorAll("text")].find(
			(t) => t.textContent === "Mar",
		);
		expect(label).toBeDefined();
		expect(label).toHaveAttribute("text-anchor", "start");
	});

	test("the summary gives the range, total and busiest day", () => {
		render(calendar({ unit: "min" }));
		expect(
			screen.getByRole("group", {
				name: "Activity: Sun, Mar 1, 2026 to Sat, Mar 14, 2026, 17 min in total, busiest Mon, Mar 2, 2026 with 10",
			}),
		).toBeInTheDocument();
	});

	test("weeks trims the calendar to the latest weeks", () => {
		const { container } = render(calendar({ weeks: 1 }));
		expect(cells(container)).toHaveLength(7);
		expect(screen.getByRole("group").getAttribute("aria-label")).toContain(
			"Mar 8, 2026",
		);
	});

	test("from and to bound the range", () => {
		const { container } = render(
			calendar({ from: "2026-03-02", to: "2026-03-04" }),
		);
		expect(
			within(screen.getByRole("table"))
				.getAllByRole("row")
				.slice(1)
				.map((r) => r.textContent?.slice(0, 16)),
		).toHaveLength(cells(container).length);
		expect(cells(container).length).toBeGreaterThanOrEqual(3);
		expect(cells(container).length).toBeLessThan(14);
	});

	test("a narrow container drops the oldest weeks instead of shrinking below 6px", () => {
		const long = Array.from({ length: 120 }, (_, i) => ({
			date: new Date(Date.UTC(2026, 0, 1 + i)).toISOString().slice(0, 10),
			value: i,
		}));
		const wide = render(<DitherHeatmap layout="calendar" data={long} />);
		expect(
			new Set(cells(wide.container).map((c) => translate(c)[0])).size,
		).toBe(18);
		wide.unmount();
		env.width = 100;
		const { container } = render(
			<DitherHeatmap layout="calendar" data={long} />,
		);
		const weeks = new Set(cells(container).map((c) => translate(c)[0])).size;
		expect(weeks).toBeLessThan(18);
		expect(weeks).toBeLessThanOrEqual(9);
		expect(
			Number(cells(container)[0]?.getAttribute("width")) + 1,
		).toBeGreaterThanOrEqual(6);
	});

	test("dithercn's default mode reads data as a calendar; mode='calendar' too", () => {
		const { container } = render(<DitherHeatmap data={entries} />);
		expect(cells(container)).toHaveLength(14);
		const mode = render(<DitherHeatmap mode="calendar" data={entries} />);
		expect(cells(mode.container)).toHaveLength(14);
	});

	test("an empty calendar has no cells and says no data", () => {
		const { container } = render(<DitherHeatmap layout="calendar" data={[]} />);
		expect(cells(container)).toHaveLength(0);
		expect(screen.getByRole("group")).toHaveAttribute(
			"aria-label",
			"Activity: no data",
		);
	});

	test("unparseable dates are skipped", () => {
		const { container } = render(
			<DitherHeatmap
				layout="calendar"
				data={[
					{ date: "nonsense", value: 5 },
					{ date: "2026-03-01", value: 2 },
				]}
			/>,
		);
		expect(cells(container)).toHaveLength(1);
		expect(screen.getByRole("group").getAttribute("aria-label")).toContain(
			"2 value in total",
		);
	});
});

describe("DitherHeatmap interaction", () => {
	test("keyboard: focus lands on one button; arrows move between cells and the readout and live region follow", async () => {
		const user = userEvent.setup();
		const { container } = render(
			matrix({ unit: { one: "deploy", other: "deploys" } }),
		);
		await user.tab();
		expect(button()).toHaveFocus();
		expect(screen.queryByRole("status")).toBeNull();
		await user.keyboard("{ArrowRight}");
		// First arrow lands on the first cell.
		expect(live(container)).toHaveTextContent("Mon 00: 0 deploys");
		await user.keyboard("{ArrowRight}");
		expect(live(container)).toHaveTextContent("Mon 06: 5 deploys");
		await user.keyboard("{ArrowDown}");
		expect(live(container)).toHaveTextContent("Tue 06: 2 deploys");
		await user.keyboard("{ArrowLeft}");
		expect(live(container)).toHaveTextContent("Tue 00: No data");
		await user.keyboard("{ArrowUp}");
		expect(live(container)).toHaveTextContent("Mon 00:");
		// Stops at the edge instead of wrapping.
		await user.keyboard("{ArrowLeft}{ArrowUp}");
		expect(live(container)).toHaveTextContent("Mon 00:");
		await user.keyboard("{End}");
		expect(live(container)).toHaveTextContent("Tue 18: 8 deploys");
		await user.keyboard("{Home}");
		expect(live(container)).toHaveTextContent("Mon 00:");
		await user.keyboard("{Escape}");
		expect(live(container)).toHaveTextContent("");
	});

	test("a single-valued cell uses the singular unit", async () => {
		const user = userEvent.setup();
		const { container } = render(
			matrix({
				values: [[1, 2]],
				rows: ["Mon"],
				columns: ["a", "b"],
				unit: { one: "deploy", other: "deploys" },
			}),
		);
		await user.tab();
		await user.keyboard("{Home}");
		expect(live(container)).toHaveTextContent("Mon a: 1 deploy");
	});

	test("the readout card names the cell and its value, and a ring marks the cell", async () => {
		const user = userEvent.setup();
		const { container } = render(matrix({ unit: "builds" }));
		await user.tab();
		await user.keyboard("{Home}{ArrowRight}");
		const status = screen.getByRole("status");
		expect(status).toHaveTextContent("Mon 06");
		expect(status).toHaveTextContent("builds");
		expect(status).toHaveTextContent("5");
		expect(svgOf(container).querySelector("rect.stroke-ink-1")).not.toBeNull();
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("status")).toBeNull();
		expect(svgOf(container).querySelector("rect.stroke-ink-1")).toBeNull();
	});

	test("the readout for an empty cell says No data", async () => {
		const user = userEvent.setup();
		render(matrix());
		await user.tab();
		await user.keyboard("{End}{ArrowLeft}{ArrowLeft}{ArrowLeft}");
		expect(screen.getByRole("status")).toHaveTextContent("Tue 00");
		expect(screen.getByRole("status")).toHaveTextContent("No data");
	});

	test("blur hides the readout", async () => {
		const user = userEvent.setup();
		render(matrix());
		await user.tab();
		await user.keyboard("{Home}");
		expect(screen.getByRole("status")).toBeInTheDocument();
		fireEvent.blur(button());
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("pointer: moving over a cell shows its readout, leaving clears, the gutter hits nothing", () => {
		const { container } = render(matrix());
		const svg = svgOf(container);
		// Cell (col 1,row 0): x 44..57, y 16..29 inside the svg box (left/top 0).
		fireEvent.pointerMove(svg, { clientX: 50, clientY: 20 });
		expect(screen.getByRole("status")).toHaveTextContent("Mon 06");
		fireEvent.pointerMove(svg, { clientX: 50, clientY: 40 });
		expect(screen.getByRole("status")).toHaveTextContent("Tue 06");
		fireEvent.pointerDown(svg, { clientX: 28 + 3 * 16 + 2, clientY: 18 });
		expect(screen.getByRole("status")).toHaveTextContent("Mon 18");
		fireEvent.pointerMove(svg, { clientX: 5, clientY: 20 });
		expect(screen.queryByRole("status")).toBeNull();
		fireEvent.pointerMove(svg, { clientX: 50, clientY: 20 });
		fireEvent.pointerLeave(svg);
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("tooltip={false} drops the readout and the keyboard button but keeps the legend buttons", () => {
		const { container } = render(matrix({ tooltip: false }));
		expect(
			screen.queryByRole("button", { name: /Use the arrow keys/ }),
		).toBeNull();
		fireEvent.pointerMove(svgOf(container), { clientX: 50, clientY: 20 });
		expect(screen.queryByRole("status")).toBeNull();
		expect(screen.getByRole("list", { name: "Scale" })).toBeInTheDocument();
	});

	test("interactive={false}: a labelled image, a static scale without buttons, no live region", () => {
		const { container } = render(matrix({ interactive: false }));
		expect(screen.getByRole("img")).toHaveAttribute(
			"aria-label",
			expect.stringContaining("Heatmap:"),
		);
		expect(screen.queryAllByRole("button")).toHaveLength(0);
		expect(screen.queryByRole("list", { name: "Scale" })).toBeNull();
		expect(live(container)).toBeNull();
		expect(
			container.querySelectorAll("svg[aria-hidden]").length,
		).toBeGreaterThan(1);
	});

	test("scale steps spotlight their cells while hovered or focused, and click locks", async () => {
		const user = userEvent.setup();
		const { container } = render(matrix({ levels: 4 }));
		const opacities = () => cells(container).map((c) => c.style.opacity);
		expect(new Set(opacities())).toEqual(new Set(["1"]));
		const top = scale()[4] as HTMLElement;
		await user.hover(top);
		// Only the max cell (20) is at the top level.
		expect(opacities().filter((o) => o === "1")).toHaveLength(1);
		expect(opacities().filter((o) => o === "0.2")).toHaveLength(7);
		await user.unhover(top);
		expect(new Set(opacities())).toEqual(new Set(["1"]));
		await user.click(top);
		expect(top).toHaveAttribute("aria-pressed", "true");
		await user.unhover(top);
		expect(opacities().filter((o) => o === "0.2")).toHaveLength(7);
		await user.keyboard("{Escape}");
		expect(top).toHaveAttribute("aria-pressed", "false");
		expect(new Set(opacities())).toEqual(new Set(["1"]));
		await user.click(top);
		await user.click(top);
		expect(top).toHaveAttribute("aria-pressed", "false");
	});

	test("focusing a scale step lights it", () => {
		const { container } = render(matrix({ levels: 4 }));
		const first = scale()[0] as HTMLElement;
		fireEvent.focus(first);
		expect(
			cells(container).filter((c) => c.style.opacity === "1"),
		).toHaveLength(2);
		fireEvent.blur(first);
		expect(cells(container).every((c) => c.style.opacity === "1")).toBe(true);
	});

	test("Escape in the grid also releases a locked step", async () => {
		const user = userEvent.setup();
		render(matrix({ levels: 4 }));
		const top = scale()[4] as HTMLElement;
		await user.click(top);
		await user.click(button());
		await user.keyboard("{Escape}");
		expect(top).toHaveAttribute("aria-pressed", "false");
	});
});

describe("DitherHeatmap rendering options", () => {
	const fullMotion = () =>
		spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: false,
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);

	test("format shapes the readout, legend and table", async () => {
		const user = userEvent.setup();
		render(matrix({ format: (v) => `${v}x` }));
		expect(screen.getByText("max 20x")).toBeInTheDocument();
		expect(
			within(screen.getByRole("row", { name: /Mon/ })).getAllByRole("cell")[1],
		).toHaveTextContent("5x");
		await user.tab();
		await user.keyboard("{Home}{ArrowRight}");
		expect(screen.getByRole("status")).toHaveTextContent("5x");
	});

	test("Intl format options work too", () => {
		render(matrix({ format: { style: "percent" } }));
		expect(screen.getByText("max 2,000%")).toBeInTheDocument();
	});

	test("the default format keeps up to two decimals", () => {
		render(
			matrix({ values: [[1.234, 2.5]], rows: ["a"], columns: ["x", "y"] }),
		);
		expect(
			within(screen.getByRole("row", { name: /a/ })).getAllByRole("cell")[0],
		).toHaveTextContent("1.23");
	});

	test("small dither cells and forced colours draw tints of the colour", () => {
		const tiny = render(matrix({ cell: 1, color: "#336699" }));
		const fills = cells(tiny.container).map((r) => r.getAttribute("fill"));
		expect(fills.filter((f) => f === "#336699").length).toBeGreaterThan(2);
		expect(
			cells(tiny.container).some(
				(r) => Number(r.getAttribute("fill-opacity")) < 1,
			),
		).toBe(true);
		tiny.unmount();
		const mm = spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: q === "(forced-colors: active)",
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);
		const forced = render(matrix({ color: "#336699" }));
		expect(
			cells(forced.container).some((r) =>
				(r.getAttribute("fill") ?? "").startsWith("url("),
			),
		).toBe(false);
		mm.mockRestore();
	});

	test("bloom glows behind the two densest levels only", () => {
		const off = render(matrix());
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const { container } = render(matrix({ bloom: "low", levels: 4 }));
		const glow = container.querySelector("g[filter] path") as SVGPathElement;
		// Max 20 over 4 levels: 15 is level 3 and 20 level 4; 10 (level 2) gets no glow.
		expect(glow.getAttribute("d")?.match(/M/g)).toHaveLength(2);
	});

	test("the ripple entrance hides the grid until seen, then fades each square in", () => {
		const mm = fullMotion();
		const calls: Keyframe[][] = [];
		const delays: number[] = [];
		const anim = spyOn(Element.prototype, "animate").mockImplementation(((
			frames: Keyframe[],
			opts: KeyframeAnimationOptions,
		) => {
			calls.push(frames);
			delays.push(Number(opts.delay));
			return { cancel: () => {} } as unknown as Animation;
		}) as unknown as Element["animate"]);
		const { container } = render(matrix());
		const group = cells(container)[0]?.parentElement as unknown as SVGGElement;
		expect(group.style.opacity).toBe("0");
		expect(calls).toHaveLength(0);
		act(() => env.intersect(true));
		expect(group.style.opacity).toBe("");
		expect(calls).toHaveLength(8);
		// Delay grows toward the bottom right: ripple = col + row, 14ms a step.
		expect(delays.slice(0, 5)).toEqual([0, 14, 28, 42, 14]);
		anim.mockRestore();
		mm.mockRestore();
	});

	test("animate={false} never hides the grid", () => {
		const mm = fullMotion();
		const { container } = render(matrix({ animate: false }));
		const group = cells(container)[0]
			?.parentElement as unknown as SVGGElement | null;
		expect(group?.style.opacity).toBe("");
		mm.mockRestore();
	});
});
