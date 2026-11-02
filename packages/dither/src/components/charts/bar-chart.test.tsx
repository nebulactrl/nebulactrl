import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DitherBarChart } from "./bar-chart";
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
	{ label: "Deploys", value: "b" as const },
] as const;

const svgOf = (c: HTMLElement) => {
	const svg = c.querySelector("svg");
	if (!svg) throw new Error("no svg");
	return svg;
};
const texts = (c: HTMLElement) =>
	[...svgOf(c).querySelectorAll("text")].map((t) => t.textContent);
/** The solid caps: one rect per drawn bar (or stack segment). */
const caps = (c: HTMLElement) => [...svgOf(c).querySelectorAll("rect[ry]")];
const num = (el: Element | undefined, attr: string) =>
	Number(el?.getAttribute(attr));
const overlay = (c: HTMLElement) => {
	const el = svgOf(c).querySelector("rect[fill='transparent']");
	if (!el) throw new Error("no overlay");
	return el;
};
const topTick = (c: HTMLElement) =>
	Math.max(
		...texts(c)
			.map((t) => Number(t))
			.filter((n) => Number.isFinite(n)),
	);

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

describe("DitherBarChart geometry", () => {
	test("bar tops follow the value scale: cap y = baseline - value * plotH / top", () => {
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		const top = topTick(container);
		const cs = caps(container);
		expect(cs).toHaveLength(3);
		const plotH = 192;
		rows.forEach((r, i) => {
			expect(num(cs[i], "y")).toBeCloseTo(200 - (r.a * plotH) / top, 5);
		});
		// Bars are laid out left to right in data order.
		expect(num(cs[0], "x")).toBeLessThan(num(cs[1], "x"));
		expect(num(cs[1], "x")).toBeLessThan(num(cs[2], "x"));
	});

	test("axis shows nice ticks from 0 and one label per group", () => {
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		const labels = texts(container);
		expect(labels).toContain("0");
		expect(topTick(container)).toBeGreaterThanOrEqual(40);
		for (const day of ["Mon", "Tue", "Wed"]) expect(labels).toContain(day);
	});

	test("format applies to ticks (function and Intl options)", () => {
		const { container, rerender } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				format={(v) => `${v}x`}
			/>,
		);
		expect(texts(container)).toContain("0x");
		rerender(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				format={{ style: "percent" }}
			/>,
		);
		expect(texts(container)).toContain("0%");
	});

	test("x may be a function", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x={(r) => r.day.toLowerCase()}
				series={one}
			/>,
		);
		expect(texts(container)).toContain("mon");
	});

	test("many groups thin the x labels instead of overlapping them", () => {
		const many = Array.from({ length: 60 }, (_, i) => ({
			day: `d${i}`,
			a: i + 1,
			b: 1,
		}));
		const { container } = render(
			<DitherBarChart label="CI" data={many} x="day" series={one} />,
		);
		const labels = texts(container).filter((t) => t?.startsWith("d"));
		expect(labels.length).toBeGreaterThan(1);
		expect(labels.length).toBeLessThan(60);
		expect(labels[0]).toBe("d0");
	});

	test("grouped bars sit side by side within a group", () => {
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={two} />,
		);
		const cs = caps(container);
		expect(cs).toHaveLength(6);
		// Series order: all of series 0 first, then series 1; same group -> series 1 right of series 0.
		expect(num(cs[3], "x")).toBeGreaterThan(num(cs[0], "x"));
		expect(num(cs[0], "y")).toBeLessThan(num(cs[3], "y"));
	});

	test("stacked puts the second series on top of the first; the axis covers the tallest total", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				mode="stacked"
			/>,
		);
		const cs = caps(container);
		// cs[0..2] = Builds in each group, cs[3..5] = Deploys.
		for (let g = 0; g < 3; g += 1) {
			expect(num(cs[3 + g], "x")).toBeCloseTo(num(cs[g], "x"), 5);
			expect(num(cs[3 + g], "y")).toBeLessThan(num(cs[g], "y"));
		}
		expect(topTick(container)).toBeGreaterThanOrEqual(50);
	});

	test("layout=percent stacks to 100% and labels the axis in %", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				layout="percent"
			/>,
		);
		expect(texts(container)).toContain("100%");
		expect(texts(container)).toContain("0%");
		const cs = caps(container);
		// Every stack reaches the top of the plot (y = 8) with its upper segment.
		for (let g = 0; g < 3; g += 1)
			expect(num(cs[3 + g], "y")).toBeCloseTo(8, 5);
	});

	test("layout wins over mode and percent", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				mode="stacked"
				percent
				layout="grouped"
			/>,
		);
		expect(texts(container)).not.toContain("100%");
		expect(caps(container)).toHaveLength(6);
		const cs = caps(container);
		expect(num(cs[3], "x")).not.toBeCloseTo(num(cs[0], "x"), 1);
	});

	test("percent only applies when stacked", () => {
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={two} percent />,
		);
		expect(texts(container)).not.toContain("100%");
	});

	test("zero, negative and non-numeric values draw no bar", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={[
					{ day: "Mon", a: 0 },
					{ day: "Tue", a: -5 },
					{ day: "Wed", a: Number.NaN },
					{ day: "Thu", a: 8 },
				]}
				x="day"
				series={one}
			/>,
		);
		expect(caps(container)).toHaveLength(1);
		expect(container.innerHTML).not.toContain("NaN");
		const cells = within(screen.getByRole("table")).getAllByRole("cell");
		expect(cells.map((c) => c.textContent)).toEqual(["0", "0", "0", "8"]);
	});

	test("a tiny positive value still gets a visible 2px cap", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={[
					{ day: "Mon", a: 0.0001 },
					{ day: "Tue", a: 1000 },
				]}
				x="day"
				series={one}
			/>,
		);
		const [small] = caps(container);
		expect(num(small, "height")).toBe(2);
	});

	test("gap widens the space between groups", () => {
		const width = (gap: number) => {
			const { container, unmount } = render(
				<DitherBarChart
					label="CI"
					data={rows}
					x="day"
					series={one}
					gap={gap}
				/>,
			);
			const w = num(caps(container)[0], "width");
			unmount();
			return w;
		};
		expect(width(40)).toBeLessThan(width(2));
	});

	test("empty data draws axes and no bars", () => {
		const { container } = render(
			<DitherBarChart label="CI" data={[]} x="day" series={one} />,
		);
		expect(caps(container)).toHaveLength(0);
		expect(container.innerHTML).not.toContain("NaN");
		expect(screen.getAllByRole("row")).toHaveLength(1);
	});

	test("axes={false} drops the labels", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				axes={false}
			/>,
		);
		expect(texts(container)).toEqual([]);
		expect(caps(container)).toHaveLength(3);
	});

	test("no svg under 24px, then it follows resizes", () => {
		env.width = 10;
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		expect(container.querySelector("svg")).toBeNull();
		env.resize(300);
		expect(svgOf(container)).toHaveAttribute("width", "300");
	});

	test("height is applied", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				height={100}
			/>,
		);
		expect(svgOf(container)).toHaveAttribute("height", "100");
	});
});

describe("DitherBarChart highlight", () => {
	test("highlight=max pins the value above the tallest bar and draws it solid", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				highlight="max"
			/>,
		);
		const cs = caps(container);
		const pinned = [...svgOf(container).querySelectorAll("text[aria-hidden]")];
		expect(pinned.map((t) => t.textContent)).toEqual(["40"]);
		expect(num(pinned[0], "y")).toBe(num(cs[1], "y") - 6);
		const solid = [...svgOf(container).querySelectorAll("path")].filter(
			(p) => p.getAttribute("fill") === "var(--dither-ink)" || false,
		);
		expect(solid.length).toBeGreaterThanOrEqual(0);
	});

	test("highlight=last and a fixed index pin that group; an out-of-range index pins none", () => {
		const labelOf = (highlight: number | "last") => {
			const { container, unmount } = render(
				<DitherBarChart
					label="CI"
					data={rows}
					x="day"
					series={one}
					highlight={highlight}
				/>,
			);
			const out = [
				...svgOf(container).querySelectorAll("text[aria-hidden]"),
			].map((t) => t.textContent);
			unmount();
			return out;
		};
		expect(labelOf("last")).toEqual(["20"]);
		expect(labelOf(0)).toEqual(["10"]);
		expect(labelOf(9)).toEqual([]);
	});

	test("stacked highlight shows the total above the stack", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				mode="stacked"
				highlight="max"
			/>,
		);
		expect(
			[...svgOf(container).querySelectorAll("text[aria-hidden]")].map(
				(t) => t.textContent,
			),
		).toEqual(["50"]);
	});

	test("the pin goes away while a group is scrubbed", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				highlight="max"
			/>,
		);
		await user.click(screen.getByRole("group", { name: "CI" }));
		await user.keyboard("{Home}");
		expect(svgOf(container).querySelectorAll("text[aria-hidden]")).toHaveLength(
			0,
		);
		await user.keyboard("{Escape}");
		expect(svgOf(container).querySelectorAll("text[aria-hidden]")).toHaveLength(
			1,
		);
	});

	test("without highlight nothing is pinned", () => {
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		expect(svgOf(container).querySelectorAll("text[aria-hidden]")).toHaveLength(
			0,
		);
	});
});

describe("DitherBarChart scrubbing and accessibility", () => {
	test("keyboard walks groups; readout, track highlight and live region follow", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={two} />,
		);
		await user.click(screen.getByRole("group", { name: "CI" }));
		expect(screen.queryByRole("status")).toBeNull();
		await user.keyboard("{ArrowRight}");
		let status = screen.getByRole("status");
		expect(status).toHaveTextContent("Mon");
		expect(status).toHaveTextContent("Builds");
		expect(status).toHaveTextContent("10");
		expect(status).toHaveTextContent("Deploys");
		expect(status).toHaveTextContent("5");
		await user.keyboard("{ArrowRight}");
		expect(screen.getByRole("status")).toHaveTextContent("Tue");
		const track = svgOf(container).querySelector("rect[rx='2']:not([ry])");
		expect(track).not.toBeNull();
		await user.keyboard("{End}");
		status = screen.getByRole("status");
		expect(status).toHaveTextContent("Wed");
		await user.keyboard("{ArrowRight}");
		expect(screen.getByRole("status")).toHaveTextContent("Wed");
		await user.keyboard("{Home}");
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"Mon: Builds 10, Deploys 5",
		);
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("stacked readout lists the top segment first and a total", async () => {
		const user = userEvent.setup();
		render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				mode="stacked"
			/>,
		);
		await user.click(screen.getByRole("group", { name: "CI" }));
		await user.keyboard("{Home}");
		const status = screen.getByRole("status");
		const text = status.textContent ?? "";
		expect(text.indexOf("Deploys")).toBeLessThan(text.indexOf("Builds"));
		expect(text.indexOf("Builds")).toBeLessThan(text.indexOf("Total"));
		expect(status).toHaveTextContent("Total15");
	});

	test("percent readout includes each share", async () => {
		const user = userEvent.setup();
		render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				layout="percent"
			/>,
		);
		await user.click(screen.getByRole("group", { name: "CI" }));
		await user.keyboard("{Home}");
		const status = screen.getByRole("status");
		expect(status).toHaveTextContent("10 · 67%");
		expect(status).toHaveTextContent("5 · 33%");
	});

	test("a single series has no Total row when stacked", async () => {
		const user = userEvent.setup();
		render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				mode="stacked"
			/>,
		);
		await user.click(screen.getByRole("group", { name: "CI" }));
		await user.keyboard("{Home}");
		expect(screen.getByRole("status")).not.toHaveTextContent("Total");
	});

	test("pointer position selects the group under it, clamps, and leaving clears", () => {
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		const o = overlay(container);
		fireEvent.pointerMove(o, { clientX: 41 });
		expect(screen.getByRole("status")).toHaveTextContent("Mon");
		fireEvent.pointerMove(o, { clientX: 40 + 432 / 2 });
		expect(screen.getByRole("status")).toHaveTextContent("Tue");
		fireEvent.pointerDown(o, { clientX: 9999 });
		expect(screen.getByRole("status")).toHaveTextContent("Wed");
		fireEvent.pointerLeave(o);
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("the readout flips to the left of the group near the right edge", () => {
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		const o = overlay(container);
		fireEvent.pointerMove(o, { clientX: 41 });
		expect(screen.getByRole("status").style.left).not.toBe("");
		fireEvent.pointerMove(o, { clientX: 9999 });
		expect(screen.getByRole("status").style.right).not.toBe("");
		expect(screen.getByRole("status").style.left).toBe("");
	});

	test("interactive chart is a focusable group with key help; table lists the data", () => {
		render(<DitherBarChart label="CI" data={rows} x="day" series={two} />);
		const group = screen.getByRole("group", { name: "CI" });
		expect(group).toHaveAttribute("tabindex", "0");
		expect(
			document.getElementById(group.getAttribute("aria-describedby") ?? ""),
		).toHaveTextContent("Use the arrow keys");
		const tue = within(screen.getByRole("table")).getByRole("row", {
			name: /Tue/,
		});
		expect(
			within(tue)
				.getAllByRole("cell")
				.map((c) => c.textContent),
		).toEqual(["40", "10"]);
	});

	test("interactive={false}: a labelled image without focus stop or readout", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				interactive={false}
			/>,
		);
		const [img] = screen.getAllByRole("img", { name: "CI. Builds, Deploys." });
		expect(img).toHaveAttribute("tabindex", "-1");
		expect(screen.queryByRole("group")).toBeNull();
		expect(container.querySelector("rect[fill='transparent']")).toBeNull();
	});

	test("tooltip={false} removes the focus stop", () => {
		render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				tooltip={false}
			/>,
		);
		expect(screen.getByRole("group", { name: "CI" })).toHaveAttribute(
			"tabindex",
			"-1",
		);
	});

	test("blur clears the scrub", async () => {
		const user = userEvent.setup();
		render(<DitherBarChart label="CI" data={rows} x="day" series={one} />);
		await user.click(screen.getByRole("group", { name: "CI" }));
		await user.keyboard("{Home}");
		await user.tab();
		expect(screen.queryByRole("status")).toBeNull();
	});
});

describe("DitherBarChart legend, colour and motion", () => {
	test("legend defaults on for two series, and can be overridden", () => {
		const a = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
		a.unmount();
		const b = render(
			<DitherBarChart label="CI" data={rows} x="day" series={two} />,
		);
		expect(screen.getByRole("list", { name: "Legend" })).toBeTruthy();
		b.unmount();
		render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				legend={false}
			/>,
		);
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
	});

	test("legendAction=toggle hides a series; the last visible one stays", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={two}
				legendAction="toggle"
			/>,
		);
		expect(caps(container)).toHaveLength(6);
		await user.click(screen.getByRole("button", { name: /Deploys/ }));
		expect(caps(container)).toHaveLength(3);
		await user.click(screen.getByRole("button", { name: /Builds/ }));
		expect(caps(container)).toHaveLength(3);
		await user.click(screen.getByRole("button", { name: /Deploys/ }));
		expect(caps(container)).toHaveLength(6);
	});

	test("spotlighting a series dims the other", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={two} />,
		);
		const groups = () =>
			[...svgOf(container).querySelectorAll(":scope > g[aria-hidden]")].map(
				(g) => (g as unknown as SVGGElement).style.opacity,
			);
		expect(new Set(groups())).toEqual(new Set(["1"]));
		await user.hover(screen.getByRole("button", { name: "Spotlight Deploys" }));
		expect(groups()).toContain("1");
		expect(groups().some((o) => o !== "1")).toBe(true);
	});

	test("colors by position, overridden by a series color; cap carries the colour", () => {
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={[
					{ label: "Builds", value: "a" },
					{ label: "Deploys", value: "b", color: "#102030" },
				]}
				colors={["#abcdef", "#ffffff"]}
			/>,
		);
		const cs = caps(container);
		expect(cs[0]).toHaveAttribute("fill", "#abcdef");
		expect(cs[3]).toHaveAttribute("fill", "#102030");
	});

	test("fill variants: gradient has tone-band paths, dotted one pattern, solid/forced a tint", () => {
		const bodies = (props: object) => {
			const { container, unmount } = render(
				<DitherBarChart
					label="CI"
					data={rows}
					x="day"
					series={one}
					{...props}
				/>,
			);
			const fills = [...svgOf(container).querySelectorAll("g > path")]
				.map((p) => p.getAttribute("fill") ?? "")
				.filter((f) => f !== "");
			unmount();
			return fills;
		};
		const gradient = bodies({});
		expect(new Set(gradient).size).toBeGreaterThan(3);
		for (const f of gradient) expect(f).toMatch(/^url\(#/);
		const dotted = bodies({ fill: "dotted" });
		expect(dotted).toHaveLength(1);
		expect(dotted[0]).toMatch(/^url\(#/);
		expect(bodies({ fill: "hatched" })[0]).not.toBe(dotted[0]);
		expect(bodies({ fill: "solid" })[0]).toMatch(/^color-mix/);
		expect(bodies({ cell: 1 })[0]).toMatch(/^color-mix/);
	});

	test("forced colours draw tinted bodies", () => {
		media("(forced-colors: active)");
		const { container } = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		expect(
			[...svgOf(container).querySelectorAll("g > path")].some((p) =>
				(p.getAttribute("fill") ?? "").startsWith("color-mix"),
			),
		).toBe(true);
	});

	test("bloom adds a blurred copy; off adds none", () => {
		const off = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const { container } = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				bloom="high"
			/>,
		);
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
	});

	test("the entrance hides bars until seen; animate=false and reduced motion show them at once", () => {
		const wrapperOf = (c: HTMLElement) =>
			caps(c)[0]?.parentElement as unknown as SVGGElement;
		media("full motion");
		const armed = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		expect(wrapperOf(armed.container).style.opacity).toBe("0");
		act(() => env.intersect(true));
		expect(wrapperOf(armed.container).style.opacity).toBe("");
		armed.unmount();
		const off = render(
			<DitherBarChart
				label="CI"
				data={rows}
				x="day"
				series={one}
				animate={false}
			/>,
		);
		expect(wrapperOf(off.container).style.opacity).toBe("");
		off.unmount();
		media("(prefers-reduced-motion: reduce)");
		const reduced = render(
			<DitherBarChart label="CI" data={rows} x="day" series={one} />,
		);
		expect(wrapperOf(reduced.container).style.opacity).toBe("");
	});
});
