import { describe, expect, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DitherDonutChart, type DonutSlice } from "./donut-chart";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

const data: DonutSlice[] = [
	{ label: "Web", value: 50 },
	{ label: "API", value: 30 },
	{ label: "Jobs", value: 20 },
];

const group = () => screen.getByRole("group", { name: "Traffic" });
const centre = () => group().querySelector("div.absolute") as HTMLElement;
const drawing = (c: HTMLElement) =>
	c.querySelector("button svg, :scope > div svg") as SVGSVGElement;
const live = (c: HTMLElement) => c.querySelector("[aria-live='polite']");
const legendRows = () =>
	within(screen.getByRole("list", { name: "Legend" })).getAllByRole("listitem");
const tableRows = () =>
	within(screen.getByRole("table")).getAllByRole("row").slice(1);

describe("DitherDonutChart summary", () => {
	test("the hole shows the total and its caption", () => {
		render(<DitherDonutChart label="Traffic" data={data} />);
		expect(centre()).toHaveTextContent("100");
		expect(centre()).toHaveTextContent("Total");
	});

	test("centerLabel, unit and format shape the centre and every value", () => {
		render(
			<DitherDonutChart
				label="Traffic"
				data={data}
				centerLabel="Requests"
				unit=" GB"
				format={{ maximumFractionDigits: 0 }}
			/>,
		);
		expect(centre()).toHaveTextContent("100 GB");
		expect(centre()).toHaveTextContent("Requests");
		expect(legendRows()[0]).toHaveTextContent("50 GB");
	});

	test("big values are compacted by default", () => {
		render(
			<DitherDonutChart
				label="Traffic"
				data={[
					{ label: "A", value: 1500 },
					{ label: "B", value: 500 },
				]}
			/>,
		);
		expect(centre()).toHaveTextContent("2K");
	});

	test("legend rows carry value and share; the table repeats them", () => {
		render(<DitherDonutChart label="Traffic" data={data} />);
		expect(legendRows().map((r) => r.textContent)).toEqual([
			"Web5050%",
			"API3030%",
			"Jobs2020%",
		]);
		const [web] = tableRows();
		expect(
			within(web as HTMLElement)
				.getAllByRole("cell")
				.map((c) => c.textContent),
		).toEqual(["50", "50%"]);
		expect(
			screen.getByRole("columnheader", { name: "Share" }),
		).toBeInTheDocument();
	});

	test("shares: under 1% is <1%, under 10% has a decimal, otherwise whole", () => {
		render(
			<DitherDonutChart
				label="Traffic"
				data={[
					{ label: "Big", value: 9000 },
					{ label: "Mid", value: 950 },
					{ label: "Tiny", value: 5 },
					{ label: "Zero", value: 0 },
				]}
			/>,
		);
		const shares = tableRows().map(
			(r) => within(r).getAllByRole("cell")[1]?.textContent,
		);
		expect(shares).toEqual(["90%", "9.5%", "<1%", "0.0%"]);
	});

	test("negative, NaN and zero slices count as zero and draw nothing", () => {
		const { container } = render(
			<DitherDonutChart
				label="Traffic"
				data={[
					{ label: "A", value: 10 },
					{ label: "B", value: -4 },
					{ label: "C", value: Number.NaN },
				]}
			/>,
		);
		expect(centre()).toHaveTextContent("10");
		expect(drawing(container).querySelectorAll("g[style]")).toHaveLength(1);
		expect(container.innerHTML).not.toContain("NaN");
	});

	test("no data at all: an empty track ring, zero total and an empty legend", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={[]} />,
		);
		expect(centre()).toHaveTextContent("0");
		expect(
			drawing(container).querySelector("path[fill='var(--dither-track)']"),
		).not.toBeNull();
		expect(screen.queryAllByRole("listitem")).toHaveLength(0);
	});

	test("all-zero data shows the track and no slices", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={[{ label: "A", value: 0 }]} />,
		);
		expect(
			drawing(container).querySelector("path[fill='var(--dither-track)']"),
		).not.toBeNull();
		expect(drawing(container).querySelectorAll("g[style]")).toHaveLength(0);
	});

	test("legend={false} keeps the list for screen readers only", () => {
		render(<DitherDonutChart label="Traffic" data={data} legend={false} />);
		expect(screen.getByRole("list", { name: "Legend" }).className).toBe(
			"sr-only",
		);
		expect(screen.queryAllByRole("button", { name: /^Web/ })).toHaveLength(0);
	});
});

describe("DitherDonutChart geometry", () => {
	test("size sets the drawing box and never exceeds the container", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} size={120} />,
		);
		expect(drawing(container)).toHaveAttribute("width", "120");
		env.resize(80);
		expect(drawing(container)).toHaveAttribute("width", "80");
	});

	test("a very narrow container still draws at least 48px", () => {
		env.width = 10;
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} />,
		);
		expect(drawing(container)).toHaveAttribute("width", "48");
	});

	test("pie has no hole readout and a donut ring is cut at the hole; thickness changes it", () => {
		const pie = render(
			<DitherDonutChart label="Traffic" data={data} variant="pie" />,
		);
		expect(pie.container.querySelector("div.absolute")).toBeNull();
		pie.unmount();
		const inner = (thickness?: number) => {
			const { container, unmount } = render(
				<DitherDonutChart
					label="Traffic"
					data={data}
					{...(thickness ? { thickness } : {})}
				/>,
			);
			const rim =
				container
					.querySelector("svg g[style] path:last-child")
					?.getAttribute("d") ?? "";
			unmount();
			return rim;
		};
		expect(inner(0.2)).not.toBe(inner(0.8));
	});

	test("mode is an alias of variant and wins over it", () => {
		const { container } = render(
			<DitherDonutChart
				label="Traffic"
				data={data}
				variant="donut"
				mode="pie"
			/>,
		);
		expect(container.querySelector("div.absolute")).toBeNull();
	});

	test("the first slice starts under 12 o'clock and slices run clockwise in data order", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} />,
		);
		const rims = [
			...container.querySelectorAll("svg g[style] > path:last-child"),
		];
		expect(rims).toHaveLength(3);
		const startX = (d: string | null | undefined) =>
			Number(d?.match(/M(-?\d+(\.\d+)?)/)?.[1]);
		const startY = (d: string | null | undefined) =>
			Number(d?.match(/M-?\d+(\.\d+)? (-?\d+(\.\d+)?)/)?.[1]);
		// With the seam centred at the top the first slice starts just right of centre-top.
		const first = rims[0]?.getAttribute("d");
		expect(startX(first)).toBeGreaterThan(90);
		expect(startY(first)).toBeLessThan(10);
		// Later slices start further around the ring.
		expect(startX(rims[1]?.getAttribute("d"))).not.toBe(startX(first));
	});

	test("each slice is painted as a pattern in a cycling texture when fill is unset", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} />,
		);
		const fills = [
			...container.querySelectorAll("svg g[style] > path:not(:last-child)"),
		].map((p) => p.getAttribute("fill"));
		expect(fills).toHaveLength(3);
		for (const f of fills) expect(f).toMatch(/^url\(#/);
		expect(new Set(fills).size).toBe(3);
	});

	test("gradient uses four radial bands per slice, dotted one flat pattern, solid the colour", () => {
		const count = (props: object) => {
			const { container, unmount } = render(
				<DitherDonutChart
					label="Traffic"
					data={[data[0] as DonutSlice]}
					colors={["#abcdef"]}
					{...props}
				/>,
			);
			const paths = [...container.querySelectorAll("svg g[style] > path")];
			unmount();
			return paths;
		};
		expect(count({ fill: "gradient" })).toHaveLength(5);
		const dotted = count({ fill: "dotted" });
		expect(dotted).toHaveLength(2);
		expect(dotted[0]?.getAttribute("fill")).toMatch(/^url\(#/);
		const solid = count({ fill: "solid" });
		expect(solid[0]?.getAttribute("fill")).toBe("#abcdef");
		// A slice's own fill wins over the chart's.
		const { container } = render(
			<DitherDonutChart
				label="Traffic"
				fill="solid"
				data={[{ label: "A", value: 1, fill: "hatched" }]}
			/>,
		);
		expect(
			container.querySelector("svg g[style] > path")?.getAttribute("fill"),
		).toMatch(/^url\(#/);
	});

	test("forced colours and tiny cells flatten textures to the slice colour", () => {
		const { container } = render(
			<DitherDonutChart
				label="Traffic"
				data={data}
				cell={1}
				colors={["#111", "#222", "#333"]}
			/>,
		);
		const fills = [
			...container.querySelectorAll("svg g[style] > path:not(:last-child)"),
		].map((p) => p.getAttribute("fill"));
		expect(fills).toEqual(["#111", "#222", "#333"]);
	});

	test("colors by position, a slice colour winning", () => {
		const { container } = render(
			<DitherDonutChart
				label="Traffic"
				fill="solid"
				data={[
					{ label: "A", value: 1 },
					{ label: "B", value: 1, color: "#010203" },
				]}
				colors={["#aabbcc", "#ffffff"]}
			/>,
		);
		const rims = [
			...container.querySelectorAll("svg g[style] > path:last-child"),
		];
		expect(rims.map((r) => r.getAttribute("fill"))).toEqual([
			"#aabbcc",
			"#010203",
		]);
	});

	test("bloom adds a blurred copy of the slices", () => {
		const off = render(<DitherDonutChart label="Traffic" data={data} />);
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} bloom="high" />,
		);
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
	});

	test("gap spaces the slices further apart", () => {
		const rim = (gap: number) => {
			const { container, unmount } = render(
				<DitherDonutChart label="Traffic" data={data} gap={gap} />,
			);
			const d = container
				.querySelector("svg g[style] > path:last-child")
				?.getAttribute("d");
			unmount();
			return d;
		};
		expect(rim(1)).not.toBe(rim(10));
	});
});

describe("DitherDonutChart interaction", () => {
	test("the drawing is one keyboard button naming every slice; the svg is hidden", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} />,
		);
		const button = screen.getByRole("button", {
			name: /Traffic\. Web, API, Jobs\. Use the arrow keys/,
		});
		expect(button.getAttribute("aria-describedby")).toBeTruthy();
		expect(drawing(container)).toHaveAttribute("aria-hidden", "true");
	});

	test("arrow keys step clockwise and wrap; the hole and live region name the slice", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} />,
		);
		await user.tab();
		await user.keyboard("{ArrowRight}");
		expect(centre()).toHaveTextContent("50");
		expect(centre()).toHaveTextContent("50% · Web");
		expect(live(container)).toHaveTextContent("Web: 50, 50%");
		await user.keyboard("{ArrowDown}");
		expect(centre()).toHaveTextContent("30% · API");
		await user.keyboard("{ArrowRight}{ArrowRight}");
		expect(centre()).toHaveTextContent("50% · Web");
		await user.keyboard("{ArrowLeft}");
		expect(centre()).toHaveTextContent("20% · Jobs");
		await user.keyboard("{ArrowUp}");
		expect(centre()).toHaveTextContent("30% · API");
		await user.keyboard("{End}");
		expect(centre()).toHaveTextContent("Jobs");
		await user.keyboard("{Home}");
		expect(centre()).toHaveTextContent("Web");
		await user.keyboard("{Escape}");
		expect(centre()).toHaveTextContent("Total");
		expect(live(container)).toHaveTextContent("");
	});

	test("zero slices are skipped by the arrows", async () => {
		const user = userEvent.setup();
		render(
			<DitherDonutChart
				label="Traffic"
				data={[
					{ label: "A", value: 1 },
					{ label: "Z", value: 0 },
					{ label: "B", value: 1 },
				]}
			/>,
		);
		await user.tab();
		await user.keyboard("{ArrowRight}{ArrowRight}");
		expect(centre()).toHaveTextContent("· B");
	});

	test("Enter keeps a slice lit through blur; Enter again lets go", async () => {
		const user = userEvent.setup();
		render(<DitherDonutChart label="Traffic" data={data} />);
		await user.tab();
		await user.keyboard("{ArrowRight}{Enter}");
		expect(screen.getByRole("button", { name: /^Web/ })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		const donut = screen.getByRole("button", { name: /Traffic\./ });
		fireEvent.blur(donut);
		expect(centre()).toHaveTextContent("50% · Web");
		await user.keyboard("{Enter}");
		expect(screen.getByRole("button", { name: /^Web/ })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("Enter with nothing lit does nothing; Space also locks", async () => {
		const user = userEvent.setup();
		render(<DitherDonutChart label="Traffic" data={data} />);
		await user.tab();
		await user.keyboard("{Enter}");
		expect(screen.getByRole("button", { name: /^Web/ })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
		await user.keyboard("{ArrowRight} ");
		expect(screen.getByRole("button", { name: /^Web/ })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	test("Escape also releases a lock", async () => {
		const user = userEvent.setup();
		render(<DitherDonutChart label="Traffic" data={data} />);
		await user.tab();
		await user.keyboard("{ArrowRight}{Enter}{Escape}");
		expect(screen.getByRole("button", { name: /^Web/ })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
		expect(centre()).toHaveTextContent("Total");
	});

	test("hovering a slice lights it, dims the others and updates the hole; leaving restores", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} />,
		);
		const slices = () => [
			...container.querySelectorAll<SVGGElement>("svg g[style]"),
		];
		fireEvent.pointerEnter(slices()[1] as SVGGElement);
		expect(centre()).toHaveTextContent("30% · API");
		expect(slices().map((g) => g.style.opacity)).toEqual(["0.45", "1", "0.45"]);
		fireEvent.pointerMove(slices()[2] as SVGGElement);
		expect(centre()).toHaveTextContent("20% · Jobs");
		fireEvent.pointerLeave(drawing(container));
		expect(centre()).toHaveTextContent("Total");
		expect(slices().map((g) => g.style.opacity)).toEqual(["1", "1", "1"]);
	});

	test("clicking a slice locks it and clicking again unlocks", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} />,
		);
		const first = container.querySelector("svg g[style]") as SVGGElement;
		fireEvent.click(first);
		fireEvent.pointerLeave(drawing(container));
		expect(centre()).toHaveTextContent("Web");
		fireEvent.click(first);
		expect(centre()).toHaveTextContent("Total");
	});

	test("legend buttons spotlight on hover and focus, and lock on click", async () => {
		const user = userEvent.setup();
		render(<DitherDonutChart label="Traffic" data={data} />);
		const api = screen.getByRole("button", { name: /^API/ });
		await user.hover(api);
		expect(centre()).toHaveTextContent("30% · API");
		await user.unhover(api);
		expect(centre()).toHaveTextContent("Total");
		await user.click(api);
		expect(api).toHaveAttribute("aria-pressed", "true");
		await user.unhover(api);
		expect(centre()).toHaveTextContent("· API");
		await user.click(api);
		expect(api).toHaveAttribute("aria-pressed", "false");
		await user.unhover(api);
		expect(centre()).toHaveTextContent("Total");
		fireEvent.focus(api);
		expect(centre()).toHaveTextContent("· API");
		fireEvent.blur(api);
		expect(centre()).toHaveTextContent("Total");
	});

	test("Escape on a legend button releases the lock", async () => {
		const user = userEvent.setup();
		render(<DitherDonutChart label="Traffic" data={data} />);
		const api = screen.getByRole("button", { name: /^API/ });
		await user.click(api);
		await user.keyboard("{Escape}");
		expect(api).toHaveAttribute("aria-pressed", "false");
	});

	test("the other legend rows dim while one is lit", async () => {
		const user = userEvent.setup();
		render(<DitherDonutChart label="Traffic" data={data} />);
		await user.hover(screen.getByRole("button", { name: /^API/ }));
		expect(screen.getByRole("button", { name: /^Web/ }).className).toContain(
			"opacity-55",
		);
		expect(
			screen.getByRole("button", { name: /^API/ }).className,
		).not.toContain("opacity-55");
	});

	test("a pie reports the lit slice in a readout instead of the hole", async () => {
		const user = userEvent.setup();
		render(<DitherDonutChart label="Traffic" data={data} variant="pie" />);
		expect(screen.queryByRole("status")).toBeNull();
		await user.hover(screen.getByRole("button", { name: /^Jobs/ }));
		const readout = screen.getByRole("status");
		expect(readout).toHaveTextContent("Jobs");
		expect(readout).toHaveTextContent("20%20");
		await user.unhover(screen.getByRole("button", { name: /^Jobs/ }));
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("interactive={false}: a labelled image whose summary lists the values, no buttons", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} interactive={false} />,
		);
		expect(
			screen.getByRole("img", { name: "Traffic: Web 50, API 30, Jobs 20" }),
		).toBeInTheDocument();
		expect(screen.queryAllByRole("button")).toHaveLength(0);
		expect(live(container)).toBeNull();
		fireEvent.pointerEnter(
			container.querySelector("svg g[style]") as SVGGElement,
		);
		expect(screen.getByRole("img")).toHaveTextContent("Total");
	});
});

describe("DitherDonutChart entrance", () => {
	test("animate={false} draws the slices unmasked", () => {
		const { container } = render(
			<DitherDonutChart label="Traffic" data={data} animate={false} />,
		);
		expect(container.querySelector("mask")).toBeNull();
	});

	test("with motion allowed the slices are masked and hidden until the chart is seen", () => {
		const real = window.matchMedia;
		window.matchMedia = (q: string) =>
			({
				matches: false,
				media: q,
				addEventListener: () => {},
				removeEventListener: () => {},
			}) as unknown as MediaQueryList;
		try {
			const { container } = render(
				<DitherDonutChart label="Traffic" data={data} />,
			);
			const masked = () =>
				container.querySelector("g[mask]") as SVGGElement | null;
			expect(masked()?.style.opacity).toBe("0");
			act(() => env.intersect(true));
			expect(masked()?.style.opacity).toBe("");
		} finally {
			window.matchMedia = real;
		}
	});
});
