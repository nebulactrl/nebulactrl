import type { ChartBloom, FillVariant } from "@nebulactrl/dither/charts/shared";
import {
	DitherUptimeBar,
	type UptimeDay,
} from "@nebulactrl/dither/charts/uptime-bar";
import { useId, useMemo, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";
import { Switch } from "#/components/ui/switch";

const DAY_MS = 86_400_000;
const START = Date.UTC(2026, 6, 28);

const dateLabel = (index: number) =>
	new Date(START + index * DAY_MS).toLocaleDateString("en", {
		month: "short",
		day: "numeric",
		timeZone: "UTC",
	});

/** Ninety deterministic days: two outages, a few degraded days, a no-data stretch at the start. */
const DAYS: readonly UptimeDay[] = Array.from(
	{ length: 90 },
	(_, i): UptimeDay => {
		const date = dateLabel(i);
		if (i < 3)
			return { date, status: "unknown", detail: "Monitoring not yet enabled" };
		if (i === 23) {
			return {
				date,
				status: "down",
				uptime: 0.91,
				detail: "Database restore took 2h 10m",
			};
		}
		if (i === 71) {
			return {
				date,
				status: "down",
				uptime: 0.97,
				detail: "eu-west edge unreachable",
			};
		}
		if (i === 41 || i === 42) {
			return {
				date,
				status: "degraded",
				uptime: 0.993,
				detail: "Elevated API latency",
			};
		}
		if (i === 57) {
			return {
				date,
				status: "degraded",
				uptime: 0.996,
				detail: "Delayed deploy webhooks",
			};
		}
		return { date, status: "up", uptime: 1 };
	},
);

type Range = "30" | "60" | "90";

/** Thirty nightly backups: one failed run and two days without one. ISO dates, written by the bar. */
const BACKUPS: readonly UptimeDay[] = Array.from(
	{ length: 30 },
	(_, i): UptimeDay => {
		const date = new Date(Date.UTC(2026, 8, 27 + i)).toISOString().slice(0, 10);
		if (i === 4 || i === 5) return { date, uptime: null };
		if (i === 17) {
			return { date, status: "down", note: "Snapshot failed: volume busy" };
		}
		return { date, status: "up", uptime: 1 };
	},
);

export function UptimeBarDemo() {
	const [range, setRange] = useState<Range>("90");
	const [legend, setLegend] = useState(true);
	const [cell, setCell] = useState(2);
	const [fill, setFill] = useState<FillVariant>("gradient");
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const legendId = useId();
	const cellLabelId = useId();
	const days = useMemo(() => DAYS.slice(DAYS.length - Number(range)), [range]);

	return (
		<section className="surface-raised flex flex-col gap-4 rounded-card p-4">
			<h3 className="type-micro text-ink-3">Uptime bar · status page</h3>
			<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
				<Segmented
					aria-label="Range"
					value={range}
					onValueChange={setRange}
					options={[
						{ value: "30", label: "30d" },
						{ value: "60", label: "60d" },
						{ value: "90", label: "90d" },
					]}
				/>
				<label
					htmlFor={legendId}
					className="flex items-center gap-2 font-mono text-2xs text-ink-2"
				>
					Legend
					<Switch
						id={legendId}
						size="sm"
						aria-label="Show legend"
						checked={legend}
						onCheckedChange={setLegend}
					/>
				</label>
				<Segmented
					aria-label="Up-day fill"
					value={fill}
					onValueChange={setFill}
					options={[
						{ value: "gradient", label: "Gradient" },
						{ value: "dotted", label: "Dotted" },
						{ value: "hatched", label: "Hatched" },
						{ value: "solid", label: "Solid" },
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
				<div className="flex items-center gap-2 font-mono text-2xs text-ink-2">
					<span id={cellLabelId}>Cell {cell}px</span>
					<Slider
						aria-labelledby={cellLabelId}
						className="w-28"
						min={1}
						max={4}
						step={1}
						value={cell}
						onValueChange={setCell}
					/>
				</div>
			</div>
			<DitherUptimeBar
				label={`API availability, last ${range} days`}
				days={days}
				legend={legend}
				cell={cell}
				fill={fill}
				bloom={bloom}
			/>
			<h4 className="type-micro text-ink-3">Backup history · custom words</h4>
			<DitherUptimeBar
				label="Nightly backups, last 30 days"
				days={BACKUPS}
				colors={{ up: "blue", down: "orange", none: "grey" }}
				words={{ up: "Succeeded", down: "Failed", none: "No backup" }}
				caption="succeeded"
				height={28}
				cell={3}
			/>
		</section>
	);
}
