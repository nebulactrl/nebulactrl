import { describe, expect, test } from "bun:test";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { buildCells, type OutageLike, type PointLike } from "./model";
import { ToneLegend, UptimeStrip } from "./uptime-strip";

const point = (at: string, patch: Partial<PointLike> = {}): PointLike => ({
	at,
	checks: 60,
	up: 60,
	degraded: 0,
	down: 0,
	maintenance: 0,
	avgLatencyMs: 0,
	maxLatencyMs: 0,
	...patch,
});

/** Ten minute cells: all fine except 10:03 (down, with latency) and a quiet 10:06-10:08. */
function minuteCells() {
	const pts: PointLike[] = [];
	for (let m = 0; m < 10; m++) {
		const at = `2026-10-29T10:0${m}:00Z`;
		if (m === 3) {
			pts.push(
				point(at, {
					up: 57,
					down: 3,
					avgLatencyMs: 120,
					maxLatencyMs: 900,
				}),
			);
		} else if (m >= 6 && m <= 8) {
			pts.push(point(at, { checks: 0, up: 0 }));
		} else {
			pts.push(point(at));
		}
	}
	return buildCells(pts, "minute");
}

const baseProps = {
	granularity: "minute" as const,
	range: "1h" as const,
	label: "Recent checks of api",
	outages: [] as OutageLike[],
	lastCheckAt: null,
	lastError: "",
};

const strip = () => screen.getByRole("group");

/** happy-dom lays nothing out; give the strip a 100px-wide box at x=0. */
function sizeStrip() {
	strip().getBoundingClientRect = () =>
		({
			left: 0,
			width: 100,
			top: 0,
			height: 28,
			right: 100,
			bottom: 28,
		}) as DOMRect;
}

describe("UptimeStrip", () => {
	test("renders nothing without cells", () => {
		const { container } = render(<UptimeStrip {...baseProps} cells={[]} />);
		expect(container).toBeEmptyDOMElement();
	});

	test("names the strip with a summary of all checks for assistive tech", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		expect(strip()).toHaveAccessibleName(
			"Recent checks of api: 420 checks in the last hour: 417 up, 3 down",
		);
		expect(strip()).toHaveAttribute("tabindex", "0");
	});

	test("labels the left edge with the range start and the right with now", () => {
		render(<UptimeStrip {...baseProps} range="7d" cells={minuteCells()} />);
		expect(screen.getByText("7d ago")).toBeInTheDocument();
		expect(screen.getByText("now")).toBeInTheDocument();
	});

	test("a hidden table lists every cell with checks and folds empty runs into one row", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		const table = screen.getByRole("table", { name: "Recent checks of api" });
		const rows = within(table).getAllByRole("row").slice(1);
		// Seven cells with checks (10:00-10:05 and 10:09) plus one folded run.
		expect(rows).toHaveLength(8);
		const down = rows.find((row) => within(row).queryByText("Down"));
		expect(down).toHaveTextContent("2026-10-29 10:03 UTC");
		expect(down).toHaveTextContent("60 checks: 57 up, 3 down");
		const empty = rows.find((row) => within(row).queryByText("No data"));
		expect(empty).toHaveTextContent(
			"2026-10-29 10:06 UTC to 2026-10-29 10:08 UTC",
		);
		expect(empty).toHaveTextContent("No checks for 3 minutes");
	});

	test("shows no tooltip until a cell is active", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
		expect(strip()).not.toHaveAttribute("aria-describedby");
	});

	test("pointing at a cell shows its time, state, checks and latency", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		sizeStrip();
		// 10 cells over 100px: x=35 is the fourth cell (10:03).
		fireEvent.pointerMove(strip(), { clientX: 35 });
		const tip = screen.getByRole("tooltip");
		expect(tip).toHaveTextContent("2026-10-29 10:03 UTC");
		expect(tip).toHaveTextContent("Down");
		expect(tip).toHaveTextContent("60 checks: 57 up, 3 down");
		expect(tip).toHaveTextContent("120 ms average");
		expect(tip).toHaveTextContent("900 ms slowest");
		expect(strip()).toHaveAttribute("aria-describedby", tip.id);
	});

	test("pointer positions beyond the edges clamp to the first and last cell", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		sizeStrip();
		fireEvent.pointerMove(strip(), { clientX: -40 });
		expect(screen.getByRole("tooltip")).toHaveTextContent("10:00 UTC");
		fireEvent.pointerDown(strip(), { clientX: 400 });
		expect(screen.getByRole("tooltip")).toHaveTextContent("10:09 UTC");
	});

	test("leaving the strip hides the tooltip", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		sizeStrip();
		fireEvent.pointerMove(strip(), { clientX: 5 });
		expect(screen.getByRole("tooltip")).toBeInTheDocument();
		fireEvent.pointerLeave(strip());
		expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
	});

	test("a strip without a measured width ignores the pointer", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		fireEvent.pointerMove(strip(), { clientX: 35 });
		expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
	});

	test("arrow keys start from the latest cell and move one cell, clamped at the ends", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		fireEvent.keyDown(strip(), { key: "ArrowLeft" });
		expect(screen.getByRole("tooltip")).toHaveTextContent("10:08 UTC");
		fireEvent.keyDown(strip(), { key: "ArrowRight" });
		fireEvent.keyDown(strip(), { key: "ArrowRight" });
		expect(screen.getByRole("tooltip")).toHaveTextContent("10:09 UTC");
		fireEvent.keyDown(strip(), { key: "Home" });
		expect(screen.getByRole("tooltip")).toHaveTextContent("10:00 UTC");
		fireEvent.keyDown(strip(), { key: "ArrowLeft" });
		expect(screen.getByRole("tooltip")).toHaveTextContent("10:00 UTC");
		fireEvent.keyDown(strip(), { key: "End" });
		expect(screen.getByRole("tooltip")).toHaveTextContent("10:09 UTC");
	});

	test("arrow keys are consumed, other keys are not, and Escape closes the tooltip", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		expect(fireEvent.keyDown(strip(), { key: "ArrowLeft" })).toBe(false);
		expect(fireEvent.keyDown(strip(), { key: "a" })).toBe(true);
		expect(screen.getByRole("tooltip")).toBeInTheDocument();
		fireEvent.keyDown(strip(), { key: "Escape" });
		expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
	});

	test("blur hides the tooltip", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		fireEvent.keyDown(strip(), { key: "Home" });
		expect(screen.getByRole("tooltip")).toBeInTheDocument();
		fireEvent.blur(strip());
		expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
	});

	test("a cell inside an outage explains the error with the outage's cause", () => {
		const outages: OutageLike[] = [
			{
				id: "o1",
				startedAt: "2026-10-29T10:02:30Z",
				resolvedAt: "2026-10-29T10:03:40Z",
				durationSeconds: 70,
				cause: "connection refused",
			},
		];
		render(
			<UptimeStrip {...baseProps} cells={minuteCells()} outages={outages} />,
		);
		sizeStrip();
		fireEvent.pointerMove(strip(), { clientX: 35 });
		expect(screen.getByRole("tooltip")).toHaveTextContent("connection refused");
		fireEvent.pointerMove(strip(), { clientX: 85 });
		expect(screen.getByRole("tooltip")).not.toHaveTextContent(
			"connection refused",
		);
	});

	test("the cell holding the last failed check shows the last error", () => {
		render(
			<UptimeStrip
				{...baseProps}
				cells={minuteCells()}
				lastCheckAt="2026-10-29T10:09:30Z"
				lastError="HTTP 503"
			/>,
		);
		fireEvent.keyDown(strip(), { key: "End" });
		expect(screen.getByRole("tooltip")).toHaveTextContent("HTTP 503");
		fireEvent.keyDown(strip(), { key: "Home" });
		expect(screen.getByRole("tooltip")).not.toHaveTextContent("HTTP 503");
	});

	test("a day strip states each day's availability and uses date-only times", () => {
		const cells = buildCells(
			[
				point("2026-10-28T00:00:00Z", { checks: 2880, up: 2870, down: 10 }),
				point("2026-10-29T00:00:00Z", { checks: 2880, up: 2880 }),
			],
			"day",
		);
		render(
			<UptimeStrip
				{...baseProps}
				granularity="day"
				range="90d"
				cells={cells}
			/>,
		);
		fireEvent.keyDown(strip(), { key: "Home" });
		const tip = screen.getByRole("tooltip");
		expect(tip).toHaveTextContent("2026-10-28 UTC");
		expect(tip).toHaveTextContent("99.65% available");
		expect(tip).toHaveTextContent("2,880 checks: 2,870 up, 10 down");
	});

	test("an hour strip does not state a daily availability and a cell without latency omits it", () => {
		render(<UptimeStrip {...baseProps} cells={minuteCells()} />);
		fireEvent.keyDown(strip(), { key: "Home" });
		const tip = screen.getByRole("tooltip");
		expect(tip).not.toHaveTextContent("available");
		expect(tip).not.toHaveTextContent("average");
	});
});

describe("ToneLegend", () => {
	test("lists only the states present, worst first", () => {
		render(<ToneLegend cells={minuteCells()} />);
		const items = within(
			screen.getByRole("list", { name: "How the cells are drawn" }),
		).getAllByRole("listitem");
		expect(items.map((item) => item.textContent)).toEqual([
			"Down",
			"Operational",
			"No data",
		]);
	});
});
