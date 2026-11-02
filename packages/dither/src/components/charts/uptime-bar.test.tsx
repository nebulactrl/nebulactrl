import { describe, expect, spyOn, test } from "bun:test";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { installChartEnv } from "./test-env";
import {
	DitherUptimeBar,
	type DitherUptimeBarProps,
	type UptimeDay,
} from "./uptime-bar";

const env = installChartEnv();

const days: UptimeDay[] = [
	{ date: "2026-10-01", uptime: 1 },
	{ date: "2026-10-02", uptime: 0.97, detail: "Elevated latency in eu-west" },
	{ date: "2026-10-03", uptime: 0.5 },
	{ date: "2026-10-04" },
	{ date: "2026-10-05", status: "up" },
];

const bar = (props: Partial<DitherUptimeBarProps> = {}) =>
	(
		<DitherUptimeBar label="API" days={days} {...props} />
	) as React.ReactElement;

const svgOf = (c: HTMLElement) =>
	c.querySelector("button svg") as SVGSVGElement;
const caps = (c: HTMLElement) =>
	[...svgOf(c).querySelectorAll("rect[ry]")] as SVGRectElement[];
const overlay = (c: HTMLElement) =>
	svgOf(c).querySelector("rect[fill='transparent']") as SVGRectElement;
const live = (c: HTMLElement) => c.querySelector("[aria-live='polite']");
const keys = () =>
	screen.getByRole("button", { name: /Use the arrow keys to inspect days/ });
const rowsOf = () =>
	within(screen.getByRole("table")).getAllByRole("row").slice(1);
const cellsOf = (row: HTMLElement) =>
	within(row)
		.getAllByRole("cell")
		.map((c) => c.textContent);

describe("DitherUptimeBar headline and table", () => {
	test("the headline is the mean over the days with data, floored, with the caption", () => {
		render(bar());
		// 1, 0.97, 0.5, (no data), 1 (status up) -> mean 0.8675
		expect(screen.getByText("86.75%")).toBeInTheDocument();
		expect(screen.getByText("uptime")).toBeInTheDocument();
	});

	test("uptime overrides the headline; caption customises or hides the word", () => {
		const { rerender } = render(
			bar({ uptime: 0.9999, caption: "this quarter" }),
		);
		expect(screen.getByText("99.99%")).toBeInTheDocument();
		expect(screen.getByText("this quarter")).toBeInTheDocument();
		rerender(bar({ uptime: 0.9999, caption: false }));
		expect(screen.queryByText("uptime")).toBeNull();
		expect(screen.getByText("99.99%")).toBeInTheDocument();
	});

	test("a perfect period reads 100%, never a rounded-up 99.995", () => {
		const { container, rerender } = render(bar({ uptime: 1 }));
		const headline = () => container.querySelector("span.ml-auto")?.textContent;
		expect(headline()).toBe("100% uptime");
		rerender(bar({ uptime: 0.99996 }));
		expect(headline()).toBe("99.99% uptime");
	});

	test("no data at all has no headline figure", () => {
		render(bar({ days: [{ date: "2026-10-01" }, { date: "2026-10-02" }] }));
		expect(screen.queryByText(/%$/)).toBeNull();
	});

	test("the table lists every day with its status word, uptime and detail", () => {
		render(bar());
		const rows = rowsOf();
		expect(rows).toHaveLength(5);
		expect(
			within(rows[0] as HTMLElement).getByRole("rowheader"),
		).toHaveTextContent("Oct 1, 2026");
		expect(cellsOf(rows[0] as HTMLElement)).toEqual([
			"Operational",
			"100%",
			"",
		]);
		expect(cellsOf(rows[1] as HTMLElement)).toEqual([
			"Degraded",
			"97.00%",
			"Elevated latency in eu-west",
		]);
		expect(cellsOf(rows[2] as HTMLElement)).toEqual(["Down", "50.00%", ""]);
		expect(cellsOf(rows[3] as HTMLElement)).toEqual(["No data", "", ""]);
		expect(cellsOf(rows[4] as HTMLElement)).toEqual(["Operational", "", ""]);
		expect(screen.getByRole("table")).toHaveTextContent("API, 86.75% uptime");
	});

	test("an uptime above 1 is clamped in the table", () => {
		render(bar({ days: [{ date: "2026-10-01", uptime: 3 }] }));
		expect(cellsOf(rowsOf()[0] as HTMLElement)[1]).toBe("100%");
	});

	test("explicit status wins over a derived one", () => {
		render(bar({ days: [{ date: "2026-10-01", uptime: 1, status: "down" }] }));
		expect(cellsOf(rowsOf()[0] as HTMLElement)[0]).toBe("Down");
	});

	test("thresholds move where a derived status flips", () => {
		render(
			bar({
				days: [
					{ date: "2026-10-01", uptime: 0.97 },
					{ date: "2026-10-02", uptime: 0.97 },
				],
				thresholds: { up: 0.96 },
			}),
		);
		expect(cellsOf(rowsOf()[0] as HTMLElement)[0]).toBe("Operational");
	});

	test("note is an alias of detail, detail winning; null uptime means no data", () => {
		render(
			bar({
				days: [
					{ date: "2026-10-01", note: "from note", uptime: null },
					{ date: "2026-10-02", note: "n", detail: "d" },
				],
			}),
		);
		const rows = rowsOf();
		expect(cellsOf(rows[0] as HTMLElement)).toEqual([
			"No data",
			"",
			"from note",
		]);
		expect(cellsOf(rows[1] as HTMLElement)[2]).toBe("d");
	});

	test("ISO dates are written in the locale (UTC); display strings pass through", () => {
		const { rerender } = render(bar({ locale: "de-DE" }));
		expect(
			within(rowsOf()[0] as HTMLElement).getByRole("rowheader"),
		).toHaveTextContent("1. Okt. 2026");
		rerender(bar({ days: [{ date: "Mar 14", uptime: 1 }] }));
		expect(
			within(rowsOf()[0] as HTMLElement).getByRole("rowheader"),
		).toHaveTextContent("Mar 14");
	});

	test("words rename the statuses in the table and the legend", () => {
		render(
			bar({
				words: {
					up: "Succeeded",
					down: "Failed",
					partial: "Partial",
					none: "No backup",
				},
			}),
		);
		const legend = screen.getByRole("list", { name: "Legend" });
		expect(
			within(legend)
				.getAllByRole("listitem")
				.map((l) => l.textContent),
		).toEqual(["Succeeded", "Partial", "Failed", "No backup"]);
		expect(cellsOf(rowsOf()[2] as HTMLElement)[0]).toBe("Failed");
	});

	test("the legend names the statuses; 'No data' only when some day has none", () => {
		const { unmount } = render(bar());
		const items = () =>
			within(screen.getByRole("list", { name: "Legend" }))
				.getAllByRole("listitem")
				.map((l) => l.textContent);
		expect(items()).toEqual(["Operational", "Degraded", "Down", "No data"]);
		unmount();
		render(bar({ days: [{ date: "2026-10-01", uptime: 1 }] }));
		expect(items()).toEqual(["Operational", "Degraded", "Down"]);
	});

	test("legend={false} removes the legend and the headline", () => {
		render(bar({ legend: false }));
		expect(screen.queryByRole("list", { name: "Legend" })).toBeNull();
		expect(screen.queryByText("86.75%")).toBeNull();
	});

	test("first and last dates are printed under the bar", () => {
		const { container } = render(bar());
		const row = container.querySelector(".text-ink-4");
		expect(row).toHaveTextContent("Oct 1, 2026");
		expect(row).toHaveTextContent("Oct 5, 2026");
		const one = render(bar({ days: [{ date: "2026-10-01", uptime: 1 }] }));
		expect(one.container.querySelector(".text-ink-4")?.textContent).toBe(
			"Oct 1, 2026",
		);
	});

	test("responsive keeps only the latest 30 days on narrow containers, 60 and 90 on wider ones", () => {
		const long = Array.from({ length: 120 }, (_, i) => ({
			date: new Date(Date.UTC(2026, 0, 1 + i)).toISOString().slice(0, 10),
			uptime: 1,
		}));
		const count = (width: number) => {
			env.width = width;
			const { unmount } = render(bar({ days: long, responsive: true }));
			const n = rowsOf().length;
			unmount();
			return n;
		};
		expect(count(300)).toBe(30);
		expect(count(500)).toBe(60);
		expect(count(700)).toBe(90);
		env.width = 480;
		render(bar({ days: long }));
		expect(rowsOf()).toHaveLength(120);
	});
});

describe("DitherUptimeBar geometry", () => {
	test("one cell per day, spread over the width in day order", () => {
		const { container } = render(bar());
		// Up days and down days carry caps; 5 days: up, degraded, down, unknown, up.
		const c = caps(container);
		expect(c).toHaveLength(3);
		const xs = c.map((r) => Number(r.getAttribute("x")));
		// cell width 20, gap 2 -> step 22: day 0, 2 and 4.
		expect(xs).toEqual([0, 44, 88]);
		expect(c.map((r) => r.getAttribute("width"))).toEqual(["20", "20", "20"]);
	});

	test("height sets the bar and caps rest on its top; gap spaces cells", () => {
		const { container } = render(bar({ height: 50, gap: 6 }));
		expect(svgOf(container)).toHaveAttribute("height", "50");
		expect(caps(container)[0]).toHaveAttribute("y", "0");
		expect(caps(container)[1]).toHaveAttribute("x", String(2 * 26));
	});

	test("more days than fit share cells, the worst status winning", () => {
		const long = Array.from({ length: 200 }, (_, i) => ({
			date: `d${i}`,
			status: i === 100 ? ("down" as const) : ("up" as const),
		}));
		const { container } = render(bar({ days: long }));
		// 480px / (3+2) -> 96 cells for 200 days; every cell is >= 3px.
		const widths = new Set(
			caps(container).map((r) => Number(r.getAttribute("width"))),
		);
		expect([...widths].every((w) => w >= 3)).toBe(true);
		expect(caps(container).length).toBe(96);
		const red = caps(container).filter(
			(r) => r.getAttribute("fill") === "var(--color-bad)",
		);
		expect(red).toHaveLength(1);
	});

	test("nothing is drawn without days or before the bar is 8px wide", () => {
		const none = render(bar({ days: [] }));
		expect(none.container.querySelector("button")).toBeNull();
		none.unmount();
		env.width = 5;
		const narrow = render(bar());
		expect(narrow.container.querySelector("button")).toBeNull();
	});

	test("status colours: defaults, then per-status overrides including dithercn's none", () => {
		const def = render(bar());
		expect(caps(def.container).map((r) => r.getAttribute("fill"))).toEqual([
			"var(--color-status-live)",
			"var(--color-bad)",
			"var(--color-status-live)",
		]);
		def.unmount();
		const { container } = render(
			bar({ colors: { up: "#111111", down: "#222222", none: "#333333" } }),
		);
		expect(caps(container).map((r) => r.getAttribute("fill"))).toEqual([
			"#111111",
			"#222222",
			"#111111",
		]);
		const legendSwatches = [
			...container.querySelectorAll("ul[aria-label='Legend'] svg rect"),
		];
		expect(
			legendSwatches.some((r) => r.getAttribute("fill") === "#111111"),
		).toBe(true);
	});

	test("fill variants for up days: gradient has several tone bands, dotted one pattern, solid a tint", () => {
		const bodies = (props: Partial<DitherUptimeBarProps>) => {
			const { container, unmount } = render(
				bar({ days: [{ date: "2026-10-01", status: "up" }], ...props }),
			);
			const paths = [...svgOf(container).querySelectorAll("path")].map((p) =>
				p.getAttribute("fill"),
			);
			unmount();
			return paths;
		};
		const gradient = bodies({});
		expect(gradient.length).toBeGreaterThan(3);
		expect(new Set(gradient).size).toBe(gradient.length);
		expect(bodies({ fill: "dotted" })).toHaveLength(1);
		expect(bodies({ fill: "solid" })[0]).toMatch(
			/^color-mix\(in oklab, .* 70%, transparent\)$/,
		);
	});

	test("degraded cells are hatched, down near-solid, no-data dotted: three different patterns", () => {
		const { container } = render(
			bar({
				days: [
					{ date: "a", status: "degraded" },
					{ date: "b", status: "down" },
					{ date: "c", status: "unknown" },
				],
			}),
		);
		const fills = [...svgOf(container).querySelectorAll("path")].map((p) =>
			p.getAttribute("fill"),
		);
		expect(fills).toHaveLength(3);
		expect(new Set(fills).size).toBe(3);
		for (const f of fills) expect(f).toMatch(/^url\(#/);
	});

	test("small cells and forced colours flatten textures to tints that also differ in height", () => {
		const { container } = render(
			bar({
				cell: 1,
				height: 40,
				days: [
					{ date: "a", status: "up" },
					{ date: "b", status: "degraded" },
					{ date: "c", status: "down" },
					{ date: "d", status: "unknown" },
				],
			}),
		);
		const bodies = [...svgOf(container).querySelectorAll("path")];
		expect(
			bodies.every((p) => p.getAttribute("fill")?.startsWith("color-mix")),
		).toBe(true);
		// Heights: up .8, degraded .55, down 1, unknown .25 of 40px; capped ones have caps at their top.
		const ys = caps(container).map((r) => Number(r.getAttribute("y")));
		expect(ys).toEqual([40 - 32, 40 - 40]);
	});

	test("forced colours do the same", () => {
		const m = spyOn(window, "matchMedia").mockImplementation(
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
			[...svgOf(container).querySelectorAll("path")].every((p) =>
				p.getAttribute("fill")?.startsWith("color-mix"),
			),
		).toBe(true);
		m.mockRestore();
	});

	test("bloom glows behind degraded and down days only", () => {
		const off = render(bar());
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const { container } = render(bar({ bloom: "low" }));
		expect(container.querySelectorAll("feGaussianBlur")).toHaveLength(2);
	});
});

describe("DitherUptimeBar interaction", () => {
	test("one button names the bar and its day count; the drawing is hidden", () => {
		const { container } = render(bar());
		expect(screen.getByRole("group", { name: "API" })).toBeInTheDocument();
		expect(
			screen.getByRole("button", {
				name: "API. 5 days. Use the arrow keys to inspect days.",
			}),
		).toBeInTheDocument();
		expect(svgOf(container)).toHaveAttribute("aria-hidden", "true");
		expect(
			document.getElementById(keys().getAttribute("aria-describedby") ?? ""),
		).toHaveTextContent("Use the arrow keys");
	});

	test("arrow keys walk the cells; the live region reads date, status, uptime and detail", async () => {
		const user = userEvent.setup();
		const { container } = render(bar());
		await user.tab();
		await user.keyboard("{ArrowRight}");
		expect(live(container)).toHaveTextContent(
			"Oct 1, 2026, Operational, 100% uptime",
		);
		await user.keyboard("{ArrowRight}");
		expect(live(container)).toHaveTextContent(
			"Oct 2, 2026, Degraded, 97.00% uptime. Elevated latency in eu-west",
		);
		await user.keyboard("{End}");
		expect(live(container)).toHaveTextContent(
			"Oct 5, 2026, Operational, 100% uptime",
		);
		await user.keyboard("{ArrowLeft}");
		expect(live(container)).toHaveTextContent("Oct 4, 2026, No data");
		expect(live(container)?.textContent).not.toContain("uptime");
		await user.keyboard("{Home}");
		expect(live(container)).toHaveTextContent("Oct 1, 2026");
		await user.keyboard("{Escape}");
		expect(live(container)).toHaveTextContent("");
	});

	test("the readout card shows the date, status word with colour, uptime and detail", async () => {
		const user = userEvent.setup();
		const { container } = render(bar());
		await user.tab();
		await user.keyboard("{ArrowRight}{ArrowRight}");
		const card = container.querySelector<HTMLElement>(".surface-pop");
		expect(card).toHaveTextContent("Oct 2, 2026");
		expect(card).toHaveTextContent("Degraded");
		expect(card).toHaveTextContent("97.00%");
		expect(card).toHaveTextContent("Elevated latency in eu-west");
		expect(
			card?.querySelector<HTMLElement>("[aria-hidden]")?.style.background,
		).toBe("var(--color-warn)");
		expect(
			svgOf(container).querySelector("rect.stroke-line-strong"),
		).not.toBeNull();
		await user.keyboard("{Escape}");
		expect(container.querySelector(".surface-pop")).toBeNull();
	});

	test("a merged cell reads as a date range with the day count and the worst day's detail", async () => {
		const user = userEvent.setup();
		const long = Array.from({ length: 200 }, (_, i) => ({
			date: `d${i}`,
			status: i === 1 ? ("down" as const) : ("up" as const),
			...(i === 1 ? { detail: "Database outage" } : {}),
		}));
		const { container } = render(bar({ days: long }));
		await user.tab();
		await user.keyboard("{Home}");
		const card = container.querySelector<HTMLElement>(".surface-pop");
		expect(card).toHaveTextContent("d0 – d1");
		expect(card).toHaveTextContent("Down");
		expect(card).toHaveTextContent("2 days, worst shown");
		expect(card).toHaveTextContent("Database outage");
	});

	test("pointer position selects the cell under it; leaving clears", () => {
		const { container } = render(bar());
		const o = overlay(container);
		fireEvent.pointerMove(o, { clientX: 50 });
		expect(live(container)).toHaveTextContent(
			"Oct 3, 2026, Down, 50.00% uptime",
		);
		expect(container.querySelector(".surface-pop")).toHaveTextContent(
			"Oct 3, 2026",
		);
		fireEvent.pointerDown(o, { clientX: 9999 });
		expect(container.querySelector(".surface-pop")).toHaveTextContent(
			"Oct 5, 2026",
		);
		fireEvent.pointerMove(o, { clientX: -20 });
		expect(container.querySelector(".surface-pop")).toHaveTextContent(
			"Oct 1, 2026",
		);
		fireEvent.pointerLeave(o);
		expect(container.querySelector(".surface-pop")).toBeNull();
	});

	test("blur clears the readout; unrelated keys do nothing", async () => {
		const user = userEvent.setup();
		const { container } = render(bar());
		await user.tab();
		await user.keyboard("x");
		expect(container.querySelector(".surface-pop")).toBeNull();
		await user.keyboard("{Home}");
		fireEvent.blur(keys());
		expect(container.querySelector(".surface-pop")).toBeNull();
	});

	test("the readout stays inside the bar at both ends", async () => {
		const user = userEvent.setup();
		const { container } = render(bar());
		await user.tab();
		await user.keyboard("{Home}");
		expect(
			Number.parseFloat(
				container.querySelector<HTMLElement>(".surface-pop")?.style.left ?? "",
			),
		).toBe(0);
		await user.keyboard("{End}");
		const left = Number.parseFloat(
			container.querySelector<HTMLElement>(".surface-pop")?.style.left ?? "",
		);
		expect(left).toBeGreaterThan(0);
		expect(left).toBeLessThanOrEqual(480 - 192);
	});
});
