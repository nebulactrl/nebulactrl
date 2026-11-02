import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubResizeObserver } from "../resize-stub";
import type { SeriesRow } from "../series";
import { StatusAreaChart } from "./status-area-chart";

const t0 = Date.UTC(2026, 0, 1, 12);
const rows: SeriesRow[] = [0, 1, 2].map((i) => ({
	t: t0 + i * 60_000,
	requests: 1_000,
	s2xx: 800,
	s3xx: 0,
	s4xx: 3,
	s5xx: 1,
	p50Ms: 10,
	p95Ms: 50,
	p99Ms: 90,
}));
const domain = { from: t0, to: t0 + 120_000, stepSeconds: 60 };

let restore = () => {};
beforeEach(() => {
	restore = stubResizeObserver({ width: 600, height: 200 });
});
afterEach(() => restore());

async function renderChart(
	markers: { id: string; at: number; label: string }[] = [],
) {
	const view = render(
		<StatusAreaChart
			rows={rows}
			domain={domain}
			syncId="st"
			markers={markers}
		/>,
	);
	await act(async () => {
		await Promise.resolve();
	});
	return view;
}

const areas = (root: HTMLElement) => root.querySelectorAll(".recharts-area");
const chip = (label: string) => {
	const legend = screen.getByRole("list", { name: "Status classes" });
	return within(legend).getByRole("button", { name: new RegExp(`^${label}`) });
};

describe("StatusAreaChart", () => {
	test("each class chip carries its total in text", async () => {
		await renderChart();
		expect(chip("2xx")).toHaveTextContent("2.4k");
		expect(chip("3xx")).toHaveTextContent("0");
		expect(chip("4xx")).toHaveTextContent("9");
		expect(chip("5xx")).toHaveTextContent("3");
	});

	test("stacks one area per class under an accessible name", async () => {
		const { container } = await renderChart();
		expect(
			screen.getByRole("img", { name: "Requests by status class over time" }),
		).toBeInTheDocument();
		expect(areas(container)).toHaveLength(4);
		expect(chip("5xx")).toHaveAttribute("aria-pressed", "true");
	});

	test("pressing a chip hides that class and pressing it again restores it", async () => {
		const user = userEvent.setup();
		const { container } = await renderChart();
		await user.click(chip("2xx"));
		expect(areas(container)).toHaveLength(3);
		expect(chip("2xx")).toHaveAttribute("aria-pressed", "false");
		expect(chip("2xx")).toHaveAttribute("title", "Show 2xx");
		// The hidden class keeps its total in the chip.
		expect(chip("2xx")).toHaveTextContent("2.4k");
		await user.click(chip("2xx"));
		expect(areas(container)).toHaveLength(4);
		expect(chip("2xx")).toHaveAttribute("title", "Hide 2xx");
	});

	test("draws a labelled line for a deployment inside the window", async () => {
		const { container } = await renderChart([
			{ id: "d1", at: t0 + 60_000, label: "abc1234" },
		]);
		expect(container.querySelectorAll(".recharts-reference-line")).toHaveLength(
			1,
		);
		expect(container).toHaveTextContent("abc1234");
	});

	test("an empty series still renders the legend with zero totals", async () => {
		render(<StatusAreaChart rows={[]} domain={domain} syncId="empty" />);
		await act(async () => {
			await Promise.resolve();
		});
		expect(chip("2xx")).toHaveTextContent("0");
		expect(chip("5xx")).toHaveTextContent("0");
	});
});
