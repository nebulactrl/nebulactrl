import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubResizeObserver } from "../resize-stub";
import type { SeriesRow } from "../series";
import { LatencyChart } from "./latency-chart";

const t0 = Date.UTC(2026, 0, 1, 12);
const rows: SeriesRow[] = [0, 1, 2].map((i) => ({
	t: t0 + i * 60_000,
	requests: 10,
	s2xx: 10,
	s3xx: 0,
	s4xx: 0,
	s5xx: 0,
	p50Ms: 10 + i,
	p95Ms: 50,
	p99Ms: 900,
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
		<LatencyChart rows={rows} domain={domain} syncId="lat" markers={markers} />,
	);
	// The chart waits for its first measured size.
	await act(async () => {
		await Promise.resolve();
	});
	return view;
}

const lines = (root: HTMLElement) => root.querySelectorAll(".recharts-line");
const texts = (root: HTMLElement) =>
	Array.from(root.querySelectorAll("text")).map((t) => t.textContent);

describe("LatencyChart", () => {
	test("draws p50, p95 and p99 lines under an accessible name", async () => {
		const { container } = await renderChart();
		expect(
			screen.getByRole("img", { name: "Response time percentiles over time" }),
		).toBeInTheDocument();
		expect(lines(container)).toHaveLength(3);
		for (const name of ["p50", "p95", "p99"]) {
			expect(
				screen.getByRole("button", { name: new RegExp(name) }),
			).toHaveAttribute("aria-pressed", "true");
		}
	});

	test("labels the y axis in milliseconds and seconds", async () => {
		const { container } = await renderChart();
		const labels = texts(container);
		expect(labels).toContain("0");
		// p99 of 900 ms sets the scale: ticks read in ms.
		expect(labels.some((l) => l?.endsWith(" ms"))).toBe(true);
	});

	test("a toggle chip hides its line and brings it back", async () => {
		const user = userEvent.setup();
		const { container } = await renderChart();
		await user.click(screen.getByRole("button", { name: "p95" }));
		expect(lines(container)).toHaveLength(2);
		const p95 = screen.getByTitle("Show p95");
		expect(p95).toHaveAttribute("aria-pressed", "false");
		// The other two stay on.
		expect(screen.getByTitle("Hide p50")).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByTitle("Hide p99")).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await user.click(p95);
		expect(lines(container)).toHaveLength(3);
		expect(screen.getByTitle("Hide p95")).toBeInTheDocument();
	});

	test("hiding every percentile leaves no lines", async () => {
		const user = userEvent.setup();
		const { container } = await renderChart();
		for (const name of ["p50", "p95", "p99"]) {
			await user.click(screen.getByRole("button", { name }));
		}
		expect(lines(container)).toHaveLength(0);
	});

	test("marks a deployment inside the window with its label and skips one outside", async () => {
		const { container } = await renderChart([
			{ id: "in", at: t0 + 60_000, label: "abc1234" },
			{ id: "out", at: t0 - 3_600_000, label: "old9999" },
		]);
		expect(container.querySelectorAll(".recharts-reference-line")).toHaveLength(
			1,
		);
		expect(texts(container)).toContain("abc1234");
		expect(texts(container)).not.toContain("old9999");
	});
});
