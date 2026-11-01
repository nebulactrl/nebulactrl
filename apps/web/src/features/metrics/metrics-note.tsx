import { cn } from "cn";
import type { MetricsMeta } from "./model";
import { exceedsRetention } from "./ranges";

/** stepSeconds / retentionDays from a metrics response; 0 means not reported. */
export function metaOf(
	data: { stepSeconds: number; retentionDays: number } | undefined,
): MetricsMeta {
	const meta: MetricsMeta = {};
	if (data?.stepSeconds) meta.stepSeconds = data.stepSeconds;
	if (data?.retentionDays) meta.retentionDays = data.retentionDays;
	return meta;
}

function stepLabel(seconds: number): string {
	if (seconds < 60) return `${seconds} s`;
	if (seconds < 3_600) return `${Math.round(seconds / 60)} min`;
	if (seconds < 86_400) return `${Math.round(seconds / 3_600)} h`;
	return `${Math.round(seconds / 86_400)} d`;
}

export interface MetricsNoteProps {
	meta: MetricsMeta;
	range: string;
	className?: string;
}

/**
 * "History kept for 7 days · one point per 5 min". A range longer than the
 * kept history says so in words, because the chart then shows less than
 * the selector promises.
 */
export function MetricsNote({ meta, range, className }: MetricsNoteProps) {
	const { retentionDays, stepSeconds } = meta;
	if (!retentionDays && !stepSeconds) return null;
	const short = retentionDays ? exceedsRetention(range, retentionDays) : false;
	return (
		<p
			className={cn(
				"flex flex-wrap items-center gap-x-2 gap-y-1 text-ink-3 text-xs leading-snug",
				className,
			)}
		>
			{retentionDays ? (
				<span>
					History kept for {retentionDays}{" "}
					{retentionDays === 1 ? "day" : "days"}.
				</span>
			) : null}
			{stepSeconds ? (
				<span>One point per {stepLabel(stepSeconds)}.</span>
			) : null}
			{short ? (
				<span className="text-status-staged-on">
					{range} reaches further back than the history kept, so only the last{" "}
					{retentionDays} {retentionDays === 1 ? "day" : "days"} show.
				</span>
			) : null}
		</p>
	);
}
