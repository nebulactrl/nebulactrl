import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import {
	type DeployMarker,
	deployMarkerLines,
	type TimeDomain,
	TimeTooltip,
	type TooltipRow,
} from "./time-chart";

const T = Date.parse("2025-01-01T12:34:56Z");
const DOMAIN: TimeDomain = { from: T - 3_600_000, to: T, stepSeconds: 10 };
const ROWS: TooltipRow[] = [
	{
		key: "cpu",
		label: "CPU",
		color: "var(--color-accent-ink)",
		format: (v) => `${v.toFixed(1)}%`,
	},
	{
		key: "cpuMax",
		label: "Peak",
		color: "var(--color-warn)",
		format: (v) => `${v.toFixed(1)}%`,
	},
];

const entry = (
	dataKey: string,
	value: number | undefined,
	t: number | null = T,
) =>
	({
		dataKey,
		value,
		payload: { t: t ?? undefined, cpu: 12.34, cpuMax: 20 },
	}) as never;

describe("TimeTooltip", () => {
	test("prints the instant in local time and UTC with seconds for a fine step", () => {
		render(
			<TimeTooltip
				active
				payload={[entry("cpu", 12.34)]}
				rows={ROWS}
				domain={DOMAIN}
			/>,
		);
		expect(screen.getByText("UTC")).toBeInTheDocument();
		expect(screen.getByText("local")).toBeInTheDocument();
		const utc = screen.getByText("UTC").parentElement;
		expect(utc).toHaveTextContent("12:34:56 UTC");
	});

	test("leaves the seconds out for a step of a minute or more", () => {
		render(
			<TimeTooltip
				active
				payload={[entry("cpu", 1)]}
				rows={ROWS}
				domain={{ ...DOMAIN, stepSeconds: 60 }}
			/>,
		);
		expect(screen.getByText("UTC").parentElement).toHaveTextContent(
			"12:34 UTC",
		);
		expect(screen.getByText("UTC").parentElement).not.toHaveTextContent(
			"12:34:56",
		);
	});

	test("adds the date once the span crosses a day", () => {
		render(
			<TimeTooltip
				active
				payload={[entry("cpu", 1)]}
				rows={ROWS}
				domain={{ from: T - 7 * 86_400_000, to: T, stepSeconds: 3_600 }}
			/>,
		);
		expect(screen.getByText("UTC").parentElement).toHaveTextContent(
			/1 Jan (at )?12:34 UTC/,
		);
	});

	test("names each series beside its formatted value", () => {
		render(
			<TimeTooltip
				active
				payload={[entry("cpu", 12.34), entry("cpuMax", 20)]}
				rows={ROWS}
				domain={DOMAIN}
			/>,
		);
		expect(screen.getByText("CPU").parentElement).toHaveTextContent("CPU12.3%");
		expect(screen.getByText("Peak").parentElement).toHaveTextContent(
			"Peak20.0%",
		);
	});

	test("a series without a numeric value in this payload is left out", () => {
		render(
			<TimeTooltip
				active
				payload={[entry("cpu", 12.34), entry("cpuMax", undefined)]}
				rows={ROWS}
				domain={DOMAIN}
			/>,
		);
		expect(screen.getByText("CPU")).toBeInTheDocument();
		expect(screen.queryByText("Peak")).not.toBeInTheDocument();
	});

	test("shows nothing when the cursor is not on the plot", () => {
		const { container, rerender } = render(
			<TimeTooltip
				active={false}
				payload={[entry("cpu", 1)]}
				rows={ROWS}
				domain={DOMAIN}
			/>,
		);
		expect(container).toBeEmptyDOMElement();
		rerender(<TimeTooltip active payload={[]} rows={ROWS} domain={DOMAIN} />);
		expect(container).toBeEmptyDOMElement();
		rerender(<TimeTooltip active rows={ROWS} domain={DOMAIN} />);
		expect(container).toBeEmptyDOMElement();
	});

	test("shows nothing when the hovered row has no timestamp", () => {
		const { container } = render(
			<TimeTooltip
				active
				payload={[entry("cpu", 1, null)]}
				rows={ROWS}
				domain={DOMAIN}
			/>,
		);
		expect(container).toBeEmptyDOMElement();
	});

	test("each row's swatch uses the row colour", () => {
		render(
			<TimeTooltip
				active
				payload={[entry("cpu", 1)]}
				rows={ROWS}
				domain={DOMAIN}
			/>,
		);
		const row = screen.getByText("CPU").parentElement as HTMLElement;
		const swatch = within(row).getByText("", { selector: "span[aria-hidden]" });
		expect(swatch.style.backgroundColor).toBe("var(--color-accent-ink)");
	});
});

describe("deployMarkerLines", () => {
	const domain: TimeDomain = { from: 0, to: 1_000, stepSeconds: 10 };
	const marker = (id: string, at: number, label = id): DeployMarker => ({
		id,
		at,
		label,
	});
	const propsOf = (lines: ReturnType<typeof deployMarkerLines>) =>
		lines.map((line) => line.props as { x: number; label?: { value: string } });

	test("draws one line per marker inside the domain, oldest first", () => {
		const lines = deployMarkerLines(
			[marker("b", 500), marker("a", 100), marker("c", 900)],
			domain,
		);
		expect(lines.map((l) => l.key)).toEqual(["a", "b", "c"]);
		expect(propsOf(lines).map((p) => p.x)).toEqual([100, 500, 900]);
	});

	test("skips markers outside the domain and keeps those on its edges", () => {
		const lines = deployMarkerLines(
			[
				marker("before", -1),
				marker("start", 0),
				marker("end", 1_000),
				marker("after", 1_001),
			],
			domain,
		);
		expect(lines.map((l) => l.key)).toEqual(["start", "end"]);
	});

	test("labels a marker only when 18% of the span lies after the previous label", () => {
		const props = propsOf(
			deployMarkerLines(
				[
					marker("a", 100),
					marker("b", 200),
					marker("c", 280),
					marker("d", 300),
				],
				domain,
			),
		);
		expect(props.map((p) => p.label?.value)).toEqual([
			"a",
			undefined,
			"c",
			undefined,
		]);
	});

	test("shortens a long label to ten characters and an ellipsis", () => {
		const [props] = propsOf(
			deployMarkerLines([marker("a", 100, "0123456789abcdef")], domain),
		);
		expect(props?.label?.value).toBe("0123456789…");
		const [exact] = propsOf(
			deployMarkerLines([marker("a", 100, "0123456789")], domain),
		);
		expect(exact?.label?.value).toBe("0123456789");
	});

	test("no markers draw nothing", () => {
		expect(deployMarkerLines([], domain)).toEqual([]);
	});
});
