import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { stubChartSize } from "../chart-size";
import {
	ResourceAreaChart,
	type ResourceAreaChartProps,
} from "./resource-area-chart";

const T0 = Date.parse("2025-01-01T10:00:00Z");
const DOMAIN = { from: T0, to: T0 + 3_600_000, stepSeconds: 60 };
type Row = { t: number; cpu: number | null };
const ROWS: Row[] = [0, 15, 30, 45, 60].map((m, i) => ({
	t: T0 + m * 60_000,
	cpu: 10 + i * 10,
}));

let restore: () => void;
beforeEach(() => {
	restore = stubChartSize(600, 200);
});
afterEach(() => restore());

function chart(
	over: Partial<ResourceAreaChartProps<Row>> = {},
	withAxisFormat = true,
) {
	return render(
		<ResourceAreaChart<Row>
			rows={ROWS}
			dataKey="cpu"
			name="CPU"
			color="var(--color-accent-ink)"
			domain={DOMAIN}
			format={(v) => `${v.toFixed(1)}%`}
			{...(withAxisFormat
				? { axisFormat: (v: number) => `${Math.round(v)}%` }
				: {})}
			syncId="test"
			{...over}
		/>,
	);
}

const texts = (container: HTMLElement) =>
	[...container.querySelectorAll("text")].map((node) => node.textContent);

describe("ResourceAreaChart", () => {
	test("is one image named after the series", () => {
		chart({ name: "Memory" });
		expect(
			screen.getByRole("img", { name: "Memory over time" }),
		).toBeInTheDocument();
	});

	test("the height prop sets the plot height", () => {
		chart({ height: 104 });
		expect(screen.getByRole("img", { name: "CPU over time" })).toHaveStyle({
			height: "104px",
		});
	});

	test("draws the series as an area path and a grid", () => {
		const { container } = chart();
		expect(container.querySelector(".recharts-cartesian-grid")).not.toBeNull();
		expect(
			container.querySelector(".recharts-area, .recharts-layer path"),
		).not.toBeNull();
	});

	// happy-dom measures no text, so Recharts keeps only the top y tick: it
	// shows where the axis ends, which is what these tests check.
	test("the y axis ends 8% above the highest value, printed through axisFormat", () => {
		const { container } = chart();
		expect(texts(container)).toContain("54%");
	});

	test("without axisFormat the y axis falls back to the value format", () => {
		const { container } = chart({}, false);
		expect(texts(container)).toContain("54.0%");
	});

	test("fixedMax pins the axis to [0, max] whatever the data", () => {
		const { container } = chart({ fixedMax: 100 });
		expect(texts(container)).toContain("100%");
	});

	test("minMax stretches the axis to include a limit larger than the data", () => {
		const { container } = chart({ minMax: 400 });
		expect(texts(container)).toContain("432%");
	});

	test("the x axis prints clock ticks across the domain", () => {
		const { container } = chart();
		expect(texts(container).some((t) => /^\d\d:\d\d$/.test(t ?? ""))).toBe(
			true,
		);
	});

	test("a limit draws a dashed line with its label", () => {
		const { container } = chart({
			limit: { y: 80, label: "quota 80%", crossed: false },
		});
		expect(texts(container)).toContain("quota 80%");
		const line = container.querySelector(".recharts-reference-line-line");
		expect(line).toHaveAttribute("stroke", "var(--color-line-control)");
		expect(line).toHaveAttribute("stroke-dasharray", "5 4");
	});

	test("a crossed limit says reached and turns amber", () => {
		const { container } = chart({
			limit: { y: 40, label: "limit 40%", crossed: true },
		});
		expect(texts(container)).toContain("limit 40% — reached");
		expect(
			container.querySelector(".recharts-reference-line-line"),
		).toHaveAttribute("stroke", "var(--color-warn)");
	});

	test("without a limit there is no reference line", () => {
		const { container } = chart();
		expect(container.querySelector(".recharts-reference-line")).toBeNull();
	});

	test("a limit above the data stays on the plot (the axis extends to it)", () => {
		const { container } = chart({
			limit: { y: 500, label: "limit 500%", crossed: false },
		});
		expect(
			container.querySelector(".recharts-reference-line-line"),
		).not.toBeNull();
		expect(texts(container)).toContain("limit 500%");
	});

	test("deployment markers inside the domain appear with their label", () => {
		const { container } = chart({
			markers: [
				{ id: "in", at: T0 + 1_800_000, label: "a1b2c3d" },
				{ id: "out", at: T0 - 1_000, label: "before0" },
			],
		});
		expect(texts(container)).toContain("a1b2c3d");
		expect(texts(container)).not.toContain("before0");
		expect(container.querySelectorAll(".recharts-reference-line")).toHaveLength(
			1,
		);
	});

	test("gap rows (null values) render without breaking the chart", () => {
		const rows: Row[] = [
			{ t: T0, cpu: 10 },
			{ t: T0 + 600_000, cpu: null },
			{ t: T0 + 1_200_000, cpu: 30 },
		];
		const { container } = chart({ rows });
		expect(
			screen.getByRole("img", { name: "CPU over time" }),
		).toBeInTheDocument();
		expect(container.querySelector("svg")).not.toBeNull();
	});

	test("empty rows render an empty plot without an area", () => {
		const { container } = chart({ rows: [] });
		expect(
			screen.getByRole("img", { name: "CPU over time" }),
		).toBeInTheDocument();
		expect(container.querySelector("svg")).not.toBeNull();
		expect(
			container.querySelector(".recharts-area-curve, .recharts-curve"),
		).toBeNull();
	});
});
