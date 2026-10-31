import {
	DitherLineChart,
	type DitherLineSeries,
} from "@nebulactrl/dither/charts/line-chart";
import type { ChartBloom } from "@nebulactrl/dither/charts/shared";
import { useId, useMemo, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";

interface Row {
	minute: string;
	p50: number;
	p95: number;
	p99: number;
	/** Last week's p95; missing for a stretch to show the gap. */
	lastWeek: number | null;
}

/** Latency in ms over 36 minutes; deterministic, with a short outage in the comparison series. */
const ROWS: readonly Row[] = Array.from({ length: 36 }, (_, i) => {
	const p50 = 78 + 14 * Math.sin(i / 3.2) + (i > 24 ? (i - 24) * 2.5 : 0);
	const p95 = p50 * (2.05 + 0.25 * Math.sin(i / 1.9));
	return {
		minute: `12:${String(i).padStart(2, "0")}`,
		p50: Math.round(p50),
		p95: Math.round(p95),
		p99: Math.round(p95 * (1.35 + 0.15 * Math.cos(i / 2.3))),
		lastWeek:
			i >= 13 && i <= 16 ? null : Math.round(165 + 25 * Math.sin(i / 4.1)),
	};
});

const SLO = [{ value: 250, label: "SLO 250 ms" }] as const;

const ms = (value: number) => `${Math.round(value)} ms`;

export function LineChartDemo() {
	const [curve, setCurve] = useState<"monotone" | "linear">("monotone");
	const [markers, setMarkers] = useState<"none" | "all" | "last">("last");
	const [band, setBand] = useState(true);
	const [slo, setSlo] = useState(true);
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const bandId = useId();
	const sloId = useId();

	const series = useMemo<
		readonly [DitherLineSeries<Row>, ...DitherLineSeries<Row>[]]
	>(() => {
		const p95: DitherLineSeries<Row> = {
			label: "p95",
			value: "p95",
			color: "info",
		};
		return [
			band ? { ...p95, band: { low: "p50", high: "p99" } } : p95,
			{
				label: "Last week",
				value: "lastWeek",
				color: "muted",
				dashed: true,
			},
		];
	}, [band]);

	return (
		<section className="surface-raised flex flex-col gap-4 rounded-card p-4">
			<h3 className="type-micro text-ink-3">Line chart · request latency</h3>
			<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
				<Segmented
					aria-label="Curve"
					value={curve}
					onValueChange={setCurve}
					options={[
						{ value: "monotone", label: "Smooth" },
						{ value: "linear", label: "Linear" },
					]}
				/>
				<Segmented
					aria-label="Markers"
					value={markers}
					onValueChange={setMarkers}
					options={[
						{ value: "none", label: "No dots" },
						{ value: "last", label: "Last" },
						{ value: "all", label: "All" },
					]}
				/>
				<Segmented
					aria-label="Bloom"
					value={bloom}
					onValueChange={setBloom}
					options={[
						{ value: "off", label: "Bloom off" },
						{ value: "low", label: "Low" },
						{ value: "high", label: "High" },
					]}
				/>
				<label
					htmlFor={bandId}
					className="flex items-center gap-2 font-mono text-2xs text-ink-2"
				>
					p50–p99 band
					<Switch
						id={bandId}
						size="sm"
						aria-label="Show p50 to p99 band"
						checked={band}
						onCheckedChange={setBand}
					/>
				</label>
				<label
					htmlFor={sloId}
					className="flex items-center gap-2 font-mono text-2xs text-ink-2"
				>
					SLO line
					<Switch
						id={sloId}
						size="sm"
						aria-label="Show SLO reference line"
						checked={slo}
						onCheckedChange={setSlo}
					/>
				</label>
			</div>
			<DitherLineChart
				label="Request latency, p95 with p50 to p99 band, against last week"
				data={ROWS}
				x="minute"
				series={series}
				curve={curve}
				markers={markers}
				bloom={bloom}
				{...(slo ? { references: SLO } : {})}
				format={ms}
				height={240}
			/>
		</section>
	);
}
