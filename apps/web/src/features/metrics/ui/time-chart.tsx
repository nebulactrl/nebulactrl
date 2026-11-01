import type { ReactElement } from "react";
import type { TooltipContentProps } from "recharts";
import { ReferenceLine, XAxis } from "recharts";
import { timeTicks } from "#/components/ui/chart-time";
import { bucketAxisLabel, dualTime } from "../chart-format";

/** The chart palette's quiet strokes, as tokens (no colour literals). */
export const GRID_STROKE = "var(--color-line-faint)";
export const TICK_FILL = "var(--color-ink-4)";
export const CURSOR_STROKE = "var(--color-line-strong)";
/** Axis tick text: 11px mono, ink-4. */
export const TICK_STYLE = {
	fill: TICK_FILL,
	fontSize: 11,
	fontFamily: "var(--font-mono)",
} as const;
/** Dashed reference lines (limit, quota): control-grey, amber once crossed. */
export const LIMIT_STROKE = "var(--color-line-control)";
export const LIMIT_CROSSED_STROKE = "var(--color-warn)";
export const LIMIT_CROSSED_FILL = "var(--color-status-staged-on)";

export interface TimeDomain {
	from: number;
	to: number;
	stepSeconds: number;
}

/**
 * The shared time x axis: numeric epoch-ms with round local-clock ticks.
 * Underlying values stay UTC instants; tick labels are in the visitor's zone
 * and the tooltip prints both.
 */
export function timeXAxis(domain: TimeDomain): ReactElement {
	return (
		<XAxis
			dataKey="t"
			type="number"
			scale="time"
			domain={[domain.from, domain.to]}
			ticks={timeTicks(domain.from, domain.to, 5)}
			tickFormatter={(ms: number) =>
				bucketAxisLabel(ms, Math.min(domain.stepSeconds, 3_600))
			}
			tickLine={false}
			tick={TICK_STYLE}
			axisLine={{ stroke: GRID_STROKE }}
			tickMargin={8}
			minTickGap={24}
		/>
	);
}

export interface TooltipRow {
	/** Series dataKey. */
	key: string;
	label: string;
	/** A token reference such as `var(--color-accent-ink)`. */
	color: string;
	format: (value: number) => string;
}

type TimeTooltipProps = Partial<TooltipContentProps> & {
	rows: readonly TooltipRow[];
	domain: TimeDomain;
};

/**
 * Crosshair tooltip: the header prints the instant in local time and UTC,
 * each row names its series in text beside the colour swatch.
 */
export function TimeTooltip({
	active,
	payload,
	rows,
	domain,
}: TimeTooltipProps) {
	if (!active || !payload?.length) return null;
	const first = payload[0]?.payload as { t?: number } | undefined;
	const t = first?.t;
	if (typeof t !== "number") return null;
	const when = dualTime(t, {
		stepSeconds: domain.stepSeconds,
		spanMs: domain.to - domain.from,
	});
	const byKey = new Map(payload.map((entry) => [String(entry.dataKey), entry]));
	return (
		<div className="surface-pop grid min-w-44 gap-2 rounded-menu p-2.5 font-mono text-2xs text-ink-2 leading-snug">
			<div className="grid gap-0.5 text-ink-1">
				<span>
					{when.local} <span className="text-ink-4">local</span>
				</span>
				<span>
					{when.utc} <span className="text-ink-4">UTC</span>
				</span>
			</div>
			<div className="grid gap-1.5">
				{rows.map((row) => {
					const entry = byKey.get(row.key);
					const value = entry?.value;
					if (typeof value !== "number") return null;
					return (
						<div key={row.key} className="flex items-center gap-2">
							<span
								aria-hidden
								className="size-2 shrink-0 rounded-[2px]"
								style={{ backgroundColor: row.color }}
							/>
							<span className="flex-1 text-ink-2">{row.label}</span>
							<span className="font-medium text-ink-1 tabular-nums">
								{row.format(value)}
							</span>
						</div>
					);
				})}
			</div>
		</div>
	);
}

export interface DeployMarker {
	id: string;
	/** Epoch ms. */
	at: number;
	label: string;
}

/**
 * Dotted vertical lines where a deployment began, labelled
 * with the commit or release. Markers outside the domain are skipped.
 */
export function deployMarkerLines(
	markers: readonly DeployMarker[],
	domain: TimeDomain,
): ReactElement[] {
	const visible = markers
		.filter((m) => m.at >= domain.from && m.at <= domain.to)
		.sort((a, b) => a.at - b.at);
	// Every marker gets its line, but a label only when there is room for it:
	// at least 18% of the span after the previous label, and shortened.
	const minGap = (domain.to - domain.from) * 0.18;
	let lastLabelAt = Number.NEGATIVE_INFINITY;
	return visible.map((m) => {
		const labelled = m.at - lastLabelAt >= minGap;
		if (labelled) lastLabelAt = m.at;
		return (
			<ReferenceLine
				key={m.id}
				x={m.at}
				stroke={CURSOR_STROKE}
				strokeDasharray="2 3"
				ifOverflow="hidden"
				{...(labelled
					? {
							label: {
								value:
									m.label.length > 10 ? `${m.label.slice(0, 10)}…` : m.label,
								position: "insideTopRight" as const,
								fill: TICK_FILL,
								fontSize: 10,
							},
						}
					: {})}
			/>
		);
	});
}
