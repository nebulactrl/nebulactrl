import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DitherAreaChart } from "./area-chart";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

interface Row {
	day: string;
	a: number;
	b: number;
}
const rows: Row[] = [
	{ day: "Mon", a: 10, b: 30 },
	{ day: "Tue", a: 20, b: 10 },
	{ day: "Wed", a: 40, b: 10 },
];
const one = [{ label: "Reads", value: "a" as const }] as const;
const two = [
	{ label: "Reads", value: "a" as const },
	{ label: "Writes", value: "b" as const },
] as const;

const svgOf = (c: HTMLElement) => {
	const svg = c.querySelector("svg");
	if (!svg) throw new Error("no svg");
	return svg;
};
const axis = (c: HTMLElement) =>
	[...svgOf(c).querySelectorAll("text")].map((t) => t.textContent);
const topLines = (c: HTMLElement) => [
	...c.querySelectorAll("path[stroke-width='1.5']"),
];
const lastY = (d: string) => Number(d.match(/-?\d+(\.\d+)?/g)?.at(-1));
const firstY = (d: string) => Number(d.match(/-?\d+(\.\d+)?/g)?.[1]);

/** Opacity of the group that wraps the first line: "0" while the entrance holds it back. */
const wrapperOpacity = (c: HTMLElement) =>
	(
		c.querySelector("path[stroke-width='1.5']")
			?.parentElement as unknown as SVGGElement | null
	)?.style.opacity;

let spy: { mockRestore: () => void } | undefined;
afterEach(() => spy?.mockRestore());
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

describe("DitherAreaChart layout", () => {
	test("the line spans the plot and rises with the data", () => {
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} />,
		);
		const [line] = topLines(container);
		const d = line?.getAttribute("d") ?? "";
		expect(d.startsWith("M40 ")).toBe(true);
		expect(lastY(d)).toBeLessThan(firstY(d));
		expect(d).toContain("472");
	});

	test("value axis starts at 0 and ends at or above the max; compact format by default", () => {
		const big = rows.map((r) => ({ ...r, a: r.a * 1000 }));
		const { container } = render(
			<DitherAreaChart label="IO" data={big} x="day" series={one} />,
		);
		const labels = axis(container);
		expect(labels).toContain("0");
		expect(labels).toContain("40K");
		expect(labels).toContain("Mon");
		expect(labels).toContain("Wed");
	});

	test("format as a function or Intl options is used for ticks", () => {
		const { container, rerender } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={one}
				format={(v) => `${v} MB`}
			/>,
		);
		expect(axis(container)).toContain("0 MB");
		rerender(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={one}
				format={{
					style: "currency",
					currency: "USD",
					maximumFractionDigits: 0,
				}}
			/>,
		);
		expect(axis(container)).toContain("$0");
	});

	test("x may be an accessor function", () => {
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x={(r) => `#${r.day}`}
				series={one}
			/>,
		);
		expect(axis(container)).toContain("#Mon");
	});

	test("axes={false} removes ticks and labels and lets the fill run to the edge", () => {
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={one}
				axes={false}
			/>,
		);
		expect(axis(container)).toEqual([]);
		expect(topLines(container)[0]?.getAttribute("d")?.startsWith("M0 ")).toBe(
			true,
		);
	});

	test("percent stacking labels the axis in % and tops out at 100%", () => {
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={two}
				stack="percent"
			/>,
		);
		const labels = axis(container);
		expect(labels).toContain("0%");
		expect(labels).toContain("100%");
		// Percent stacks end at the top of the plot: the topmost line sits at y = 8.
		const ys = topLines(container).map((l) => lastY(l.getAttribute("d") ?? ""));
		expect(Math.min(...ys)).toBe(8);
	});

	test("stacked puts the second series above the first", () => {
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={two}
				stack="stacked"
			/>,
		);
		const [first, second] = topLines(container);
		expect(firstY(second?.getAttribute("d") ?? "")).toBeLessThan(
			firstY(first?.getAttribute("d") ?? ""),
		);
	});

	test("overlapping (none) draws each series from the baseline", () => {
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={two} />,
		);
		const [first, second] = topLines(container);
		// Mon: a=10, b=30 -> b is higher. Wed: a=40, b=10 -> a is higher.
		expect(firstY(second?.getAttribute("d") ?? "")).toBeLessThan(
			firstY(first?.getAttribute("d") ?? ""),
		);
		expect(lastY(first?.getAttribute("d") ?? "")).toBeLessThan(
			lastY(second?.getAttribute("d") ?? ""),
		);
	});

	test("curve linear and monotone give different paths for the same data", () => {
		const d = (curve: "linear" | "smooth") => {
			const { container, unmount } = render(
				<DitherAreaChart
					label="IO"
					data={rows}
					x="day"
					series={one}
					curve={curve}
				/>,
			);
			const out = topLines(container)[0]?.getAttribute("d");
			unmount();
			return out;
		};
		expect(d("linear")).not.toBe(d("smooth"));
	});

	test("non-numeric and missing values count as zero and never produce NaN", () => {
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={[
					{ day: "Mon", a: Number.NaN },
					{ day: "Tue", a: 5 },
				]}
				x="day"
				series={one}
			/>,
		);
		expect(container.innerHTML).not.toContain("NaN");
		const cells = within(screen.getByRole("table")).getAllByRole("cell");
		expect(cells.map((c) => c.textContent)).toEqual(["0", "5"]);
	});

	test("empty and single-point data render without a line or NaN", () => {
		const empty = render(
			<DitherAreaChart label="IO" data={[]} x="day" series={one} />,
		);
		expect(empty.container.innerHTML).not.toContain("NaN");
		expect(screen.getAllByRole("row")).toHaveLength(1);
		empty.unmount();
		const single = render(
			<DitherAreaChart
				label="IO"
				data={[{ day: "Mon", a: 1, b: 1 }]}
				x="day"
				series={one}
			/>,
		);
		expect(single.container.innerHTML).not.toContain("NaN");
	});

	test("no svg below 24px; one appears once the element grows", () => {
		env.width = 12;
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} />,
		);
		expect(container.querySelector("svg")).toBeNull();
		env.resize(240);
		expect(svgOf(container)).toHaveAttribute("width", "240");
	});

	test("height is applied to the svg", () => {
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={one}
				height={90}
			/>,
		);
		expect(svgOf(container)).toHaveAttribute("height", "90");
	});
});

describe("DitherAreaChart accessibility and scrubbing", () => {
	test("interactive: focusable group with key help, summary image and a data table", () => {
		render(<DitherAreaChart label="IO" data={rows} x="day" series={two} />);
		const group = screen.getByRole("group", { name: "IO" });
		expect(group).toHaveAttribute("tabindex", "0");
		expect(
			document.getElementById(group.getAttribute("aria-describedby") ?? ""),
		).toHaveTextContent("Use the arrow keys");
		expect(
			screen.getByRole("img", { name: "IO. Reads, Writes." }),
		).toBeTruthy();
		const table = screen.getByRole("table");
		expect(
			within(table)
				.getAllByRole("columnheader")
				.map((h) => h.textContent),
		).toEqual(["x", "Reads", "Writes"]);
		const wed = within(table).getByRole("row", { name: /Wed/ });
		expect(
			within(wed)
				.getAllByRole("cell")
				.map((c) => c.textContent),
		).toEqual(["40", "10"]);
	});

	test("interactive={false}: a labelled image, no focus stop, no scrubbing", () => {
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={two}
				interactive={false}
			/>,
		);
		const [img] = screen.getAllByRole("img", { name: "IO. Reads, Writes." });
		expect(img).toHaveAttribute("tabindex", "-1");
		expect(screen.queryByRole("group")).toBeNull();
		expect(
			svgOf(container).querySelector("rect[fill='transparent']"),
		).toBeNull();
	});

	test("tooltip={false} keeps the group out of the tab order", () => {
		render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={one}
				tooltip={false}
			/>,
		);
		expect(screen.getByRole("group", { name: "IO" })).toHaveAttribute(
			"tabindex",
			"-1",
		);
	});

	test("keyboard scrubbing updates the readout, live region and crosshair", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={two} />,
		);
		await user.click(screen.getByRole("group", { name: "IO" }));
		expect(screen.queryByRole("status")).toBeNull();
		await user.keyboard("{ArrowLeft}");
		expect(screen.getByRole("status")).toHaveTextContent("Wed");
		await user.keyboard("{ArrowLeft}");
		let status = screen.getByRole("status");
		expect(status).toHaveTextContent("Tue");
		expect(status).toHaveTextContent("20");
		expect(status).toHaveTextContent("Writes");
		await user.keyboard("{Home}");
		status = screen.getByRole("status");
		expect(status).toHaveTextContent("Mon");
		expect(container.querySelector("line.stroke-line-strong")).toHaveAttribute(
			"x1",
			"40",
		);
		await user.keyboard("{End}");
		expect(container.querySelector("line.stroke-line-strong")).toHaveAttribute(
			"x1",
			"472",
		);
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"Wed: Reads 40, Writes 10",
		);
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("status")).toBeNull();
		expect(container.querySelector("line.stroke-line-strong")).toBeNull();
	});

	test("unrelated keys do nothing", async () => {
		const user = userEvent.setup();
		render(<DitherAreaChart label="IO" data={rows} x="day" series={one} />);
		await user.click(screen.getByRole("group", { name: "IO" }));
		await user.keyboard("a");
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("pointer maps x to the nearest datum, clamped to the ends, and leaving clears it", () => {
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} />,
		);
		const overlay = svgOf(container).querySelector("rect[fill='transparent']");
		if (!overlay) throw new Error("no overlay");
		fireEvent.pointerMove(overlay, { clientX: 256 });
		expect(screen.getByRole("status")).toHaveTextContent("Tue");
		fireEvent.pointerMove(overlay, { clientX: 9999 });
		expect(screen.getByRole("status")).toHaveTextContent("Wed");
		fireEvent.pointerDown(overlay, { clientX: -50 });
		expect(screen.getByRole("status")).toHaveTextContent("Mon");
		fireEvent.pointerLeave(overlay);
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("the readout formats values with the chart format", async () => {
		const user = userEvent.setup();
		render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={one}
				format={(v) => `${v}ms`}
			/>,
		);
		await user.click(screen.getByRole("group", { name: "IO" }));
		await user.keyboard("{Home}");
		expect(screen.getByRole("status")).toHaveTextContent("10ms");
	});

	test("blur clears the scrub", async () => {
		const user = userEvent.setup();
		render(<DitherAreaChart label="IO" data={rows} x="day" series={one} />);
		await user.click(screen.getByRole("group", { name: "IO" }));
		await user.keyboard("{Home}");
		await user.tab();
		expect(screen.queryByRole("status")).toBeNull();
	});
});

describe("DitherAreaChart legend, colour and motion", () => {
	test("legend defaults to two or more series and can be forced either way", () => {
		const a = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} />,
		);
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
		a.unmount();
		const b = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} legend />,
		);
		expect(screen.getByRole("list", { name: "Legend" })).toBeTruthy();
		b.unmount();
		render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={two}
				legend={false}
			/>,
		);
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
	});

	test("spotlighting a legend chip dims the other series", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={two} />,
		);
		const opacities = () =>
			[...svgOf(container).querySelectorAll(":scope > g")]
				.filter((g) => g.querySelector("path"))
				.map((g) => (g as unknown as SVGGElement).style.opacity);
		expect(new Set(opacities())).toEqual(new Set(["1"]));
		const writes = screen.getByRole("button", { name: "Spotlight Writes" });
		await user.click(writes);
		expect(opacities().some((o) => o !== "1")).toBe(true);
		expect(opacities()).toContain("1");
		// The lock outlives the pointer; Escape on the chip lets go.
		await user.unhover(writes);
		expect(writes).toHaveAttribute("aria-pressed", "true");
		expect(opacities().some((o) => o !== "1")).toBe(true);
		await user.keyboard("{Escape}");
		await user.tab();
		expect(new Set(opacities())).toEqual(new Set(["1"]));
	});

	test("legendAction=toggle hides a series but keeps the last one", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={two}
				legendAction="toggle"
			/>,
		);
		expect(topLines(container)).toHaveLength(2);
		await user.click(screen.getByRole("button", { name: /Writes/ }));
		expect(topLines(container)).toHaveLength(1);
		expect(screen.getByRole("img", { name: "IO. Reads." })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /Reads/ }));
		expect(topLines(container)).toHaveLength(1);
		await user.click(screen.getByRole("button", { name: /Writes/ }));
		expect(topLines(container)).toHaveLength(2);
	});

	test("colors by position, overridden by a series color", () => {
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={[
					{ label: "Reads", value: "a" },
					{ label: "Writes", value: "b", color: "#010203" },
				]}
				colors={["#aabbcc", "#ffffff"]}
			/>,
		);
		const [first, second] = topLines(container);
		expect(first).toHaveAttribute("stroke", "#aabbcc");
		expect(second).toHaveAttribute("stroke", "#010203");
	});

	test("fill variants: gradient uses level bands, dotted a single pattern, solid a tint", () => {
		const count = (props: object) => {
			const { container, unmount } = render(
				<DitherAreaChart
					label="IO"
					data={rows}
					x="day"
					series={one}
					{...props}
				/>,
			);
			const n = container.querySelectorAll("g[clip-path] > path").length;
			const fill = container
				.querySelector("g[clip-path] > path")
				?.getAttribute("fill");
			unmount();
			return { n, fill };
		};
		expect(count({}).n).toBe(7);
		expect(count({ fill: "dotted" }).n).toBe(1);
		expect(count({ fill: "solid" }).fill).toMatch(/^color-mix/);
		expect(count({ cell: 1 }).fill).toMatch(/^color-mix/);
	});

	test("bloom draws a blurred copy; off does not", () => {
		const off = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} />,
		);
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const { container } = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={one}
				bloom="low"
			/>,
		);
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
	});

	test("the entrance hides the fill until the chart is seen", () => {
		media("full motion");
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} />,
		);
		const wrap = () =>
			container.querySelector("path[stroke-width='1.5']")
				?.parentElement as unknown as SVGGElement;
		expect(wrap().style.opacity).toBe("0");
		act(() => env.intersect(true));
		expect(wrap().style.opacity).toBe("");
	});

	test("animate={false} and reduced motion never hide the chart", () => {
		media("full motion");
		const off = render(
			<DitherAreaChart
				label="IO"
				data={rows}
				x="day"
				series={one}
				animate={false}
			/>,
		);
		expect(wrapperOpacity(off.container)).toBe("");
		off.unmount();
		media("(prefers-reduced-motion: reduce)");
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} />,
		);
		expect(wrapperOpacity(container)).toBe("");
	});

	test("forced colours turn the dither into a tint", () => {
		media("(forced-colors: active)");
		const { container } = render(
			<DitherAreaChart label="IO" data={rows} x="day" series={one} />,
		);
		const fill = container
			.querySelector("g[clip-path] > path")
			?.getAttribute("fill");
		expect(fill).toMatch(/^color-mix/);
	});
});
