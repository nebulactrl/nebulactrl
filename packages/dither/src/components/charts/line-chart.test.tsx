import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DitherLineChart } from "./line-chart";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

interface Row {
	day: string;
	a: number | null;
	b?: number | null;
	lo?: number | null;
	hi?: number | null;
}

const rows: Row[] = [
	{ day: "Mon", a: 10, b: 4, lo: 8, hi: 12 },
	{ day: "Tue", a: 30, b: 8, lo: 20, hi: 36 },
	{ day: "Wed", a: 20, b: 6, lo: 15, hi: 24 },
	{ day: "Thu", a: 40, b: 12, lo: 30, hi: 44 },
];

const one = [{ label: "Latency", value: "a" as const }] as const;
const two = [
	{ label: "Latency", value: "a" as const },
	{ label: "Errors", value: "b" as const, dashed: true },
] as const;

const svgOf = (container: HTMLElement) => {
	const svg = container.querySelector("svg");
	if (!svg) throw new Error("no svg");
	return svg;
};
const axisLabels = (container: HTMLElement) =>
	[...svgOf(container).querySelectorAll("text")].map((t) => t.textContent);

const mediaMatches = (matching: string) =>
	spyOn(window, "matchMedia").mockImplementation(
		(query: string) =>
			({
				matches: query === matching,
				media: query,
				addEventListener: () => {},
				removeEventListener: () => {},
			}) as unknown as MediaQueryList,
	);

describe("DitherLineChart layout and axes", () => {
	test("draws one polyline per series that rises with the readings", () => {
		const { container } = render(
			<DitherLineChart label="Latency" data={rows} x="day" series={one} />,
		);
		const paths = [...svgOf(container).querySelectorAll("path")].filter(
			(p) => p.getAttribute("stroke-width") === "1.5",
		);
		expect(paths).toHaveLength(1);
		const d = paths[0]?.getAttribute("d") ?? "";
		expect(d.startsWith("M")).toBe(true);
		// Mon(10) -> Thu(40): the end of the path is higher (smaller y) than the start.
		const nums = d.match(/-?\d+(\.\d+)?/g)?.map(Number) ?? [];
		expect(nums[0]).toBe(40);
		expect(nums.at(-2)).toBeCloseTo(40 + 428, 0);
		expect(nums.at(-1) ?? 0).toBeLessThan(nums[1] ?? 0);
	});

	test("labels the value axis with niceTicks of the data, formatted compact by default", () => {
		const big = rows.map((r) => ({ ...r, a: (r.a ?? 0) * 100 }));
		const { container } = render(
			<DitherLineChart label="Latency" data={big} x="day" series={one} />,
		);
		const labels = axisLabels(container);
		expect(labels).toContain("0");
		expect(labels).toContain("4K");
		expect(labels).toContain("Mon");
		expect(labels).toContain("Thu");
	});

	test("a custom format function and Intl options format the ticks", () => {
		const { container, rerender } = render(
			<DitherLineChart
				label="Latency"
				data={rows}
				x="day"
				series={one}
				format={(v) => `${v}ms`}
			/>,
		);
		expect(axisLabels(container)).toContain("0ms");
		rerender(
			<DitherLineChart
				label="Latency"
				data={rows}
				x="day"
				series={one}
				format={{ style: "percent" }}
			/>,
		);
		expect(axisLabels(container)).toContain("0%");
	});

	test("zero={false} fits the value axis to the data instead of starting at 0", () => {
		const { container } = render(
			<DitherLineChart
				label="Latency"
				data={rows}
				x="day"
				series={one}
				zero={false}
			/>,
		);
		const ticks = axisLabels(container).filter((t) => /^\d+$/.test(t ?? ""));
		expect(ticks).not.toContain("0");
		expect(Number(ticks[0])).toBeGreaterThanOrEqual(10);
	});

	test("the x accessor may be a function", () => {
		const { container } = render(
			<DitherLineChart
				label="Latency"
				data={rows}
				x={(r) => r.day.toUpperCase()}
				series={one}
			/>,
		);
		expect(axisLabels(container)).toContain("MON");
	});

	test("axes={false} drops tick labels and gridlines", () => {
		const { container } = render(
			<DitherLineChart
				label="Latency"
				data={rows}
				x="day"
				series={one}
				axes={false}
			/>,
		);
		expect(axisLabels(container)).toEqual([]);
		expect(svgOf(container).querySelectorAll("line")).toHaveLength(0);
	});

	test("renders no svg until the element is wide enough", () => {
		env.width = 10;
		const { container } = render(
			<DitherLineChart label="Latency" data={rows} x="day" series={one} />,
		);
		expect(container.querySelector("svg")).toBeNull();
		act(() => env.resize(300));
		expect(container.querySelector("svg")).not.toBeNull();
	});

	test("follows the measured width", () => {
		const { container } = render(
			<DitherLineChart label="Latency" data={rows} x="day" series={one} />,
		);
		expect(svgOf(container)).toHaveAttribute("width", "480");
		env.resize(320);
		expect(svgOf(container)).toHaveAttribute("width", "320");
	});

	test("height sets the plot height", () => {
		const { container } = render(
			<DitherLineChart
				label="Latency"
				data={rows}
				x="day"
				series={one}
				height={120}
			/>,
		);
		expect(svgOf(container)).toHaveAttribute("height", "120");
	});

	test("empty data draws axes but no line and an empty table body", () => {
		const { container } = render(
			<DitherLineChart label="Latency" data={[]} x="day" series={one} />,
		);
		expect(container.querySelectorAll("path[stroke-width='1.5']")).toHaveLength(
			0,
		);
		expect(container.querySelectorAll("tbody tr")).toHaveLength(0);
		expect(axisLabels(container)).toContain("0");
	});

	test("a single point is drawn as a dot centred in the plot", () => {
		const { container } = render(
			<DitherLineChart
				label="Latency"
				data={[{ day: "Mon", a: 5 }]}
				x="day"
				series={one}
			/>,
		);
		const dot = svgOf(container).querySelector("circle[r='2']");
		expect(dot).not.toBeNull();
		expect(Number(dot?.getAttribute("cx"))).toBe(40 + 428 / 2);
		expect(container.querySelectorAll("path[stroke-width='1.5']")).toHaveLength(
			0,
		);
	});

	test("all-equal readings and negative values still produce a finite axis", () => {
		const { container } = render(
			<DitherLineChart
				label="Latency"
				data={[
					{ day: "Mon", a: -5 },
					{ day: "Tue", a: -5 },
				]}
				x="day"
				series={one}
			/>,
		);
		const d = container
			.querySelector("path[stroke-width='1.5']")
			?.getAttribute("d");
		expect(d).toBeDefined();
		expect(d).not.toContain("NaN");
		expect(axisLabels(container).join(" ")).not.toContain("NaN");
	});
});

describe("DitherLineChart gaps, markers, references and bands", () => {
	const gap: Row[] = [
		{ day: "Mon", a: 1 },
		{ day: "Tue", a: 2 },
		{ day: "Wed", a: null },
		{ day: "Thu", a: 4 },
		{ day: "Fri", a: 5 },
	];
	test("null readings lift the pen: two line segments, and the table says no data", () => {
		const { container } = render(
			<DitherLineChart label="L" data={gap} x="day" series={one} />,
		);
		expect(container.querySelectorAll("path[stroke-width='1.5']")).toHaveLength(
			2,
		);
		const wed = screen.getByRole("row", { name: /Wed/ });
		expect(within(wed).getByText("no data")).toBeInTheDocument();
	});

	test("an isolated reading between gaps is a dot, not a line", () => {
		const { container } = render(
			<DitherLineChart
				label="L"
				data={[
					{ day: "Mon", a: 1 },
					{ day: "Tue", a: null },
					{ day: "Wed", a: 3 },
					{ day: "Thu", a: null },
				]}
				x="day"
				series={one}
			/>,
		);
		expect(container.querySelectorAll("circle[r='2']")).toHaveLength(2);
	});

	test("markers: none, last and all", () => {
		const count = (markers: "none" | "all" | "last") => {
			const { container, unmount } = render(
				<DitherLineChart
					label="L"
					data={rows}
					x="day"
					series={one}
					markers={markers}
				/>,
			);
			const n = container.querySelectorAll("circle[r='3']").length;
			unmount();
			return n;
		};
		expect(count("none")).toBe(0);
		expect(count("last")).toBe(1);
		expect(count("all")).toBe(rows.length);
	});

	test("references draw a dashed line with its label and widen the axis to include it", () => {
		const { container } = render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={one}
				references={[
					{ value: 100, label: "SLO" },
					{ value: Number.NaN, label: "ignored" },
				]}
			/>,
		);
		expect(screen.getByText("SLO")).toBeInTheDocument();
		expect(screen.queryByText("ignored")).toBeNull();
		const dashed = container.querySelectorAll("line[stroke-dasharray='5 4']");
		expect(dashed).toHaveLength(1);
		expect(axisLabels(container)).toContain("100");
	});

	test("a dashed series dashes its line", () => {
		const { container } = render(
			<DitherLineChart label="L" data={rows} x="day" series={two} />,
		);
		const lines = [...container.querySelectorAll("path[stroke-width='1.5']")];
		expect(lines).toHaveLength(2);
		expect(lines[0]).not.toHaveAttribute("stroke-dasharray");
		expect(lines[1]).toHaveAttribute("stroke-dasharray", "5 4");
	});

	test("a band is filled and gets its own table column and readout row", async () => {
		const { container } = render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={[
					{
						label: "p50",
						value: "a",
						band: { low: "lo", high: "hi" },
					},
				]}
			/>,
		);
		const filled = [...container.querySelectorAll("svg path")].filter(
			(p) =>
				p.getAttribute("fill") !== "none" && p.getAttribute("stroke") === null,
		);
		expect(filled.length).toBeGreaterThan(0);
		expect(
			screen.getByRole("columnheader", { name: "p50 range" }),
		).toBeInTheDocument();
		const mon = screen.getByRole("row", { name: /Mon/ });
		expect(within(mon).getByText("8 to 12")).toBeInTheDocument();
		const user = userEvent.setup();
		await user.click(screen.getByRole("group", { name: "L" }));
		await user.keyboard("{Home}");
		expect(screen.getByRole("status")).toHaveTextContent("p50 range");
		expect(screen.getByRole("status")).toHaveTextContent("8–12");
	});

	test("`range` is an alias of `band`", () => {
		render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={[
					{ label: "p50", value: "a", range: { low: "lo", high: "hi" } },
				]}
			/>,
		);
		expect(
			screen.getByRole("columnheader", { name: "p50 range" }),
		).toBeInTheDocument();
	});

	test("fill=solid and a sub-pixel cell both use a translucent tint instead of a dither pattern", () => {
		const series = [
			{
				label: "p50",
				value: "a" as const,
				band: { low: "lo" as const, high: "hi" as const },
			},
		] as const;
		const { container, rerender } = render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={series}
				fill="solid"
			/>,
		);
		const fills = () =>
			[...container.querySelectorAll("svg path")]
				.map((p) => p.getAttribute("fill") ?? "")
				.filter((f) => f !== "none");
		expect(fills().some((f) => f.startsWith("color-mix("))).toBe(true);
		rerender(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={series}
				cell={1}
			/>,
		);
		expect(fills().some((f) => f.startsWith("color-mix("))).toBe(true);
	});

	test("gradient, dotted and hatched bands reference a dither pattern; each texture is its own pattern", () => {
		const series = [
			{
				label: "p50",
				value: "a" as const,
				band: { low: "lo" as const, high: "hi" as const },
			},
		] as const;
		const urlOf = (fill: "gradient" | "dotted" | "hatched") => {
			const { container, unmount } = render(
				<DitherLineChart
					label="L"
					data={rows}
					x="day"
					series={series}
					fill={fill}
				/>,
			);
			const url = [...container.querySelectorAll("svg path")]
				.map((p) => p.getAttribute("fill") ?? "")
				.find((f) => f.startsWith("url(#"));
			unmount();
			return url;
		};
		const urls = [urlOf("gradient"), urlOf("dotted"), urlOf("hatched")];
		for (const url of urls) expect(url).toMatch(/^url\(#.+\)$/);
		expect(new Set(urls).size).toBe(3);
	});

	test("a band with a missing edge reports no data for that row", () => {
		const data: Row[] = [
			{ day: "Mon", a: 1, lo: 0, hi: 2 },
			{ day: "Tue", a: 2, lo: null, hi: 3 },
			{ day: "Wed", a: 3, lo: 2, hi: 4 },
		];
		render(
			<DitherLineChart
				label="L"
				data={data}
				x="day"
				series={[{ label: "p50", value: "a", band: { low: "lo", high: "hi" } }]}
			/>,
		);
		const tue = screen.getByRole("row", { name: /Tue/ });
		expect(within(tue).getByText("no data")).toBeInTheDocument();
	});
});

describe("DitherLineChart accessibility and interaction", () => {
	test("interactive chart is a focusable group described by key help", () => {
		render(
			<DitherLineChart label="Latency" data={rows} x="day" series={one} />,
		);
		const group = screen.getByRole("group", { name: "Latency" });
		expect(group).toHaveAttribute("tabindex", "0");
		const help = group.getAttribute("aria-describedby") ?? "";
		expect(document.getElementById(help)).toHaveTextContent(
			"Use the arrow keys",
		);
		expect(
			screen.getByRole("img", { name: "Latency. Latency." }),
		).toBeInTheDocument();
	});

	test("interactive={false} is a static labelled image without a focus stop or readout", () => {
		render(
			<DitherLineChart
				label="Latency"
				data={rows}
				x="day"
				series={two}
				interactive={false}
			/>,
		);
		const [img] = screen.getAllByRole("img", {
			name: "Latency. Latency, Errors.",
		});
		expect(img).toHaveAttribute("tabindex", "-1");
		expect(screen.queryByRole("group", { name: "Latency" })).toBeNull();
		expect(screen.queryByText(/Use the arrow keys/)).toBeNull();
	});

	test("tooltip={false} removes the focus stop but keeps the group", () => {
		render(
			<DitherLineChart
				label="Latency"
				data={rows}
				x="day"
				series={one}
				tooltip={false}
			/>,
		);
		expect(screen.getByRole("group", { name: "Latency" })).toHaveAttribute(
			"tabindex",
			"-1",
		);
		expect(screen.queryByText(/Use the arrow keys/)).toBeNull();
	});

	test("the sr-only table lists every datum", () => {
		render(
			<DitherLineChart label="Latency" data={rows} x="day" series={two} />,
		);
		const table = screen.getByRole("table");
		expect(
			within(table).getByText("Latency", { selector: "caption" }),
		).toBeTruthy();
		expect(within(table).getAllByRole("row")).toHaveLength(rows.length + 1);
		const thu = within(table).getByRole("row", { name: /Thu/ });
		expect(
			within(thu)
				.getAllByRole("cell")
				.map((c) => c.textContent),
		).toEqual(["40", "12"]);
	});

	test("arrow keys walk the data, Home/End jump, Escape clears; the readout and live region follow", async () => {
		const user = userEvent.setup();
		render(
			<DitherLineChart label="Latency" data={rows} x="day" series={two} />,
		);
		const group = screen.getByRole("group", { name: "Latency" });
		expect(screen.queryByRole("status")).toBeNull();
		await user.click(group);
		await user.keyboard("{ArrowRight}");
		let status = screen.getByRole("status");
		expect(status).toHaveTextContent("Mon");
		expect(status).toHaveTextContent("Latency");
		expect(status).toHaveTextContent("10");
		await user.keyboard("{ArrowRight}");
		expect(screen.getByRole("status")).toHaveTextContent("Tue");
		expect(screen.getByRole("status")).toHaveTextContent("30");
		await user.keyboard("{End}");
		status = screen.getByRole("status");
		expect(status).toHaveTextContent("Thu");
		expect(status).toHaveTextContent("40");
		expect(status).toHaveTextContent("12");
		await user.keyboard("{ArrowRight}");
		expect(screen.getByRole("status")).toHaveTextContent("Thu");
		await user.keyboard("{Home}");
		expect(screen.getByRole("status")).toHaveTextContent("Mon");
		const live = group.querySelector("[aria-live='polite']");
		expect(live).toHaveTextContent("Mon: Latency 10, Errors 4");
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("status")).toBeNull();
		expect(live).toHaveTextContent("");
	});

	test("a missing reading shows an em dash in the readout", async () => {
		const user = userEvent.setup();
		render(
			<DitherLineChart
				label="L"
				data={[
					{ day: "Mon", a: 1 },
					{ day: "Tue", a: null },
				]}
				x="day"
				series={one}
			/>,
		);
		await user.click(screen.getByRole("group", { name: "L" }));
		await user.keyboard("{End}");
		expect(screen.getByRole("status")).toHaveTextContent("—");
	});

	test("pointer position maps to the nearest datum and the crosshair follows", () => {
		const { container } = render(
			<DitherLineChart label="Latency" data={rows} x="day" series={one} />,
		);
		const overlay = svgOf(container).querySelector("rect");
		if (!overlay) throw new Error("no overlay");
		// plot spans x 40..468; the far right selects the last datum.
		fireEvent.pointerMove(overlay, { clientX: 468 });
		expect(screen.getByRole("status")).toHaveTextContent("Thu");
		fireEvent.pointerMove(overlay, { clientX: 40 });
		expect(screen.getByRole("status")).toHaveTextContent("Mon");
		const crosshair = svgOf(container).querySelector("line.stroke-line-strong");
		expect(crosshair).toHaveAttribute("x1", "40");
		fireEvent.pointerDown(overlay, { clientX: 468 });
		expect(screen.getByRole("status")).toHaveTextContent("Thu");
		// Clamps beyond the plot.
		fireEvent.pointerMove(overlay, { clientX: 5000 });
		expect(screen.getByRole("status")).toHaveTextContent("Thu");
		fireEvent.pointerLeave(overlay);
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("blurring the chart clears the readout", async () => {
		const user = userEvent.setup();
		render(
			<DitherLineChart label="Latency" data={rows} x="day" series={one} />,
		);
		await user.click(screen.getByRole("group", { name: "Latency" }));
		await user.keyboard("{Home}");
		expect(screen.getByRole("status")).toBeInTheDocument();
		await user.tab();
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("Escape clears a locked legend spotlight", async () => {
		const user = userEvent.setup();
		render(<DitherLineChart label="L" data={rows} x="day" series={two} />);
		const errors = screen.getByRole("button", { name: "Spotlight Errors" });
		await user.click(errors);
		expect(errors).toHaveAttribute("aria-pressed", "true");
		await user.click(screen.getByRole("group", { name: "L" }));
		await user.keyboard("{Escape}");
		expect(errors).toHaveAttribute("aria-pressed", "false");
	});
});

describe("DitherLineChart legend", () => {
	test("a single series has no legend unless asked; two or more do", () => {
		const { unmount } = render(
			<DitherLineChart label="L" data={rows} x="day" series={one} />,
		);
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
		unmount();
		render(<DitherLineChart label="L" data={rows} x="day" series={two} />);
		expect(screen.getByRole("list", { name: "Legend" })).toBeInTheDocument();
	});

	test("legend={true} shows one for a single series and legend={false} hides it", () => {
		const { unmount } = render(
			<DitherLineChart label="L" data={rows} x="day" series={one} legend />,
		);
		expect(screen.getByRole("list", { name: "Legend" })).toBeInTheDocument();
		unmount();
		render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={two}
				legend={false}
			/>,
		);
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
	});

	test("spotlight dims the other series while a legend chip is hovered", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherLineChart label="L" data={rows} x="day" series={two} />,
		);
		const groups = () =>
			[...svgOf(container).querySelectorAll(":scope > g[aria-hidden]")].map(
				(g) => (g as unknown as SVGGElement).style.opacity,
			);
		expect(groups().every((o) => o === "1")).toBe(true);
		await user.hover(screen.getByRole("button", { name: "Spotlight Errors" }));
		expect(groups()).toContain("1");
		expect(groups().some((o) => o !== "1")).toBe(true);
		await user.unhover(
			screen.getByRole("button", { name: "Spotlight Errors" }),
		);
		expect(groups().every((o) => o === "1")).toBe(true);
	});

	test("legendAction=toggle hides a series, but never the last one", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={two}
				legendAction="toggle"
			/>,
		);
		expect(container.querySelectorAll("path[stroke-width='1.5']")).toHaveLength(
			2,
		);
		const errors = screen.getByRole("button", { name: /Errors/ });
		const latency = screen.getByRole("button", { name: /Latency/ });
		await user.click(errors);
		expect(errors).toHaveAttribute("aria-pressed", "false");
		expect(container.querySelectorAll("path[stroke-width='1.5']")).toHaveLength(
			1,
		);
		await user.click(latency);
		expect(latency).toHaveAttribute("aria-pressed", "true");
		expect(container.querySelectorAll("path[stroke-width='1.5']")).toHaveLength(
			1,
		);
		await user.click(errors);
		expect(container.querySelectorAll("path[stroke-width='1.5']")).toHaveLength(
			2,
		);
	});
});

describe("DitherLineChart colour, bloom and motion", () => {
	test("colors by position and a series' own color win in that order", () => {
		const { container } = render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={[
					{ label: "A", value: "a" },
					{ label: "B", value: "b", color: "#123456" },
				]}
				colors={["#abcdef", "#ffffff"]}
			/>,
		);
		const lines = container.querySelectorAll("path[stroke-width='1.5']");
		expect(lines[0]).toHaveAttribute("stroke", "#abcdef");
		expect(lines[1]).toHaveAttribute("stroke", "#123456");
	});

	test("bloom adds a blurred copy of each line; off adds none", () => {
		const off = render(
			<DitherLineChart label="L" data={rows} x="day" series={one} />,
		);
		expect(off.container.querySelectorAll("feGaussianBlur")).toHaveLength(0);
		off.unmount();
		const { container } = render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={one}
				bloom="high"
			/>,
		);
		expect(container.querySelectorAll("feGaussianBlur")).toHaveLength(1);
		expect(container.querySelector("path[stroke-width='3']")).not.toBeNull();
	});

	test("entrance hides the lines until the chart is seen, then plays once", () => {
		const motion = mediaMatches("full motion");
		const { container } = render(
			<DitherLineChart label="L" data={rows} x="day" series={one} />,
		);
		const wrapper = () =>
			container.querySelector("path[stroke-width='1.5']")
				?.parentElement as unknown as SVGGElement;
		expect(wrapper().style.opacity).toBe("0");
		env.intersect(true);
		expect(wrapper().style.opacity).toBe("");
		motion.mockRestore();
	});

	test("animate={false} never hides anything", () => {
		const motion = mediaMatches("full motion");
		const { container } = render(
			<DitherLineChart
				label="L"
				data={rows}
				x="day"
				series={one}
				animate={false}
			/>,
		);
		const wrapper = container.querySelector("path[stroke-width='1.5']")
			?.parentElement as unknown as SVGGElement;
		expect(wrapper.style.opacity).toBe("");
		motion.mockRestore();
	});

	describe("preferences", () => {
		let spy: { mockRestore: () => void } | undefined;
		afterEach(() => spy?.mockRestore());

		test("reduced motion never hides the lines even before the chart is seen", () => {
			spy = mediaMatches("(prefers-reduced-motion: reduce)");
			const { container } = render(
				<DitherLineChart label="L" data={rows} x="day" series={one} />,
			);
			const wrapper = container.querySelector("path[stroke-width='1.5']")
				?.parentElement as unknown as SVGGElement;
			expect(wrapper.style.opacity).toBe("");
		});

		test("forced colours replace the dither pattern with a plain tint", () => {
			spy = mediaMatches("(forced-colors: active)");
			const { container } = render(
				<DitherLineChart
					label="L"
					data={rows}
					x="day"
					series={[
						{ label: "p50", value: "a", band: { low: "lo", high: "hi" } },
					]}
				/>,
			);
			const fills = [...container.querySelectorAll("svg path")]
				.map((p) => p.getAttribute("fill") ?? "")
				.filter((f) => f !== "none");
			expect(fills.some((f) => f.startsWith("color-mix("))).toBe(true);
			expect(fills.some((f) => f.startsWith("url("))).toBe(false);
		});
	});
});
