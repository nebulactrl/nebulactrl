import { cn } from "cn";
import { useId, useMemo, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import {
	compactNumber,
	formatByteCount,
	formatRate,
} from "#/features/metrics/chart-format";
import { ChartCard } from "#/features/metrics/ui/chart-card";
import { EYEBROW } from "#/features/metrics/ui/eyebrow";
import type { DeployMarker } from "#/features/metrics/ui/time-chart";
import { useChartDomain } from "#/features/metrics/use-chart-domain";
import type { TrafficRange } from "../metrics/ranges";
import { computeDelta, POLARITY } from "./delta";
import { latencyText } from "./latency-text";
import { topServerErrors } from "./lists";
import { classShare, toSeriesRows } from "./series";
import type { TrafficReport } from "./types";
import { BarList } from "./ui/bar-list";
import { ActiveHours } from "./ui/heatmap";
import { KpiTile } from "./ui/kpi-tile";
import { LatencyChart } from "./ui/latency-chart";
import { OriginsCard } from "./ui/origins-card";
import { RuledSection } from "./ui/ruled-section";
import { StatusAreaChart } from "./ui/status-area-chart";
import { StatusCodeList } from "./ui/status-codes";

type SourceTab = "referrers" | "browsers" | "os" | "devices" | "hosts";

/** Each empty Sources tab says why it is empty and what to try. */
const SOURCE_EMPTY: Record<SourceTab, string> = {
	referrers:
		"No referrers in this range. Requests without a Referer header show as Direct, so this means no requests arrived. Try a longer range.",
	browsers:
		"No browsers in this range. They come from the User-Agent header and bots are excluded. Try a longer range.",
	os: "No operating systems in this range. They come from the User-Agent header and bots are excluded. Try a longer range.",
	devices:
		"No devices in this range. They come from the User-Agent header and bots are excluded. Try a longer range.",
	hosts:
		"No hostnames in this range. Hosts appear once the service's domain receives requests.",
};

export interface TrafficViewProps {
	report: TrafficReport;
	range: TrafficRange;
	/** When the data arrived: anchors the chart's right edge. */
	updatedAt: number;
	/** Count pages (HTML documents) or every request in Top paths. */
	pagesOnly: boolean;
	onPagesOnlyChange: (next: boolean) => void;
	/** Clicking a host in the Hosts list filters the whole tab to it. */
	onHostSelect?: ((host: string) => void) | undefined;
	/** `summary` stops after the status chart (the environment overview). */
	variant?: "full" | "summary";
	/** The server the origins card's empty states link to (its traffic settings). */
	geoServerId?: string | undefined;
	className?: string;
}

function markersOf(report: TrafficReport): DeployMarker[] {
	return (report.deployments ?? []).map((d) => ({
		id: d.id,
		at: Date.parse(d.at),
		label: d.sha7 ?? d.id.slice(0, 7),
	}));
}

function Secondary({
	label,
	value,
	title,
}: {
	label: string;
	value: string;
	title?: string;
}) {
	return (
		<div title={title} className="flex min-w-0 flex-col gap-1">
			<dt className={cn(EYEBROW, "truncate")}>{label}</dt>
			<dd className="truncate font-medium font-mono text-ink-1 text-sm">
				{value}
			</dd>
		</div>
	);
}

/**
 * The traffic dashboard for one report: KPI tiles with deltas, status and
 * latency charts, ranked lists and the active-hours grid. Pure of data
 * fetching, so every state of the report renders the same wherever it lives
 * (inspector tab, server page, environment overview).
 */
export function TrafficView({
	report,
	range,
	updatedAt,
	pagesOnly,
	onPagesOnlyChange,
	onHostSelect,
	geoServerId,
	variant = "full",
	className,
}: TrafficViewProps) {
	const syncId = useId();
	const [tab, setTab] = useState<SourceTab>("referrers");
	const rows = useMemo(() => toSeriesRows(report), [report]);
	const markers = useMemo(() => markersOf(report), [report]);
	const domain = useChartDomain(range, updatedAt, report.range.stepSeconds);
	const { summary, comparison } = report;
	const delta = (
		key: keyof typeof POLARITY,
		current: number,
		previous: number | undefined,
	) =>
		comparison ? computeDelta(current, previous, POLARITY[key]) : undefined;
	const deltaLabel = comparison ? `vs previous ${range}` : undefined;
	// Nothing in this window and nothing in the one before it: a "0%" chip
	// would compare silence with silence.
	const silent = summary.requests === 0 && (comparison?.requests ?? 0) === 0;
	const botShare =
		summary.requests > 0 ? summary.botRequests / summary.requests : 0;
	const serverErrors = topServerErrors(report.topPaths);

	return (
		<div className={cn("@container flex flex-col gap-4", className)}>
			<div className="grid grid-cols-2 gap-2.5 @3xl:grid-cols-4">
				<KpiTile
					label="Requests"
					text={compactNumber(summary.requests)}
					delta={
						silent
							? undefined
							: delta("requests", summary.requests, comparison?.requests)
					}
					deltaLabel={deltaLabel}
					spark={report.sparklines?.requests}
					color="var(--color-accent-ink)"
				/>
				<KpiTile
					label="Visitors"
					text={compactNumber(summary.visitors)}
					hint="unique, counted per UTC day"
					delta={
						silent
							? undefined
							: delta("visitors", summary.visitors, comparison?.visitors)
					}
					deltaLabel={deltaLabel}
					spark={report.sparklines?.visitors}
					color="var(--color-status-live)"
				/>
				<KpiTile
					label="Bandwidth"
					text={formatByteCount(summary.bytesOut)}
					hint="sent"
					delta={
						silent
							? undefined
							: delta("bytesOut", summary.bytesOut, comparison?.bytesOut)
					}
					deltaLabel={deltaLabel}
					spark={report.sparklines?.bytesOut}
					color="var(--color-info)"
				/>
				<KpiTile
					label="p95 latency"
					text={latencyText(summary.requests, summary.p95Ms)}
					delta={
						silent
							? undefined
							: delta("p95Ms", summary.p95Ms, comparison?.p95Ms)
					}
					deltaLabel={deltaLabel}
					spark={report.sparklines?.p95Ms}
					color="var(--color-warn)"
				/>
			</div>

			<dl className="surface-raised grid grid-cols-3 gap-x-4 gap-y-3 rounded-card px-4 py-3 @3xl:grid-cols-6">
				<Secondary
					label="Error rate"
					value={formatRate(summary.errorRate)}
					title="5xx responses divided by all requests"
				/>
				<Secondary
					label="Client errors"
					value={formatRate(summary.clientErrorRate)}
					title="4xx responses divided by all requests"
				/>
				<Secondary
					label="p50"
					value={latencyText(summary.requests, summary.p50Ms)}
				/>
				<Secondary
					label="p99"
					value={latencyText(summary.requests, summary.p99Ms)}
				/>
				<Secondary
					label="Peak rps"
					value={summary.peakRps.toFixed(summary.peakRps < 10 ? 1 : 0)}
				/>
				<Secondary
					label="Bot share"
					value={formatRate(botShare)}
					title="Requests identified as crawlers; they never count as visitors"
				/>
			</dl>

			<ChartCard
				title="Requests by status class"
				subtitle={`${compactNumber(summary.requests)} requests · ${formatRate(classShare(rows, "s5xx"))} server errors`}
			>
				<StatusAreaChart
					rows={rows}
					domain={domain}
					markers={markers}
					syncId={syncId}
				/>
			</ChartCard>

			{variant === "summary" ? (
				<OriginsCard report={report} serverId={geoServerId} />
			) : null}

			{variant === "full" ? (
				<>
					<ChartCard
						title="Latency"
						subtitle="Response time including the body, per bucket."
					>
						<LatencyChart
							rows={rows}
							domain={domain}
							markers={markers}
							syncId={syncId}
						/>
					</ChartCard>

					<div className="grid gap-x-6 gap-y-4 @2xl:grid-cols-2">
						<RuledSection
							title={pagesOnly ? "Top pages" : "Top paths"}
							actions={
								<Segmented
									aria-label="Paths shown"
									font="sans"
									value={pagesOnly ? "pages" : "all"}
									onValueChange={(v) => onPagesOnlyChange(v === "pages")}
									options={[
										{ value: "pages", label: "Pages" },
										{ value: "all", label: "All requests" },
									]}
								/>
							}
						>
							<BarList
								items={report.topPaths}
								detail
								blankLabel="/"
								empty={
									pagesOnly
										? "No page views in this range. Try All requests, or a longer range."
										: "No requests in this range. Try a longer range."
								}
							/>
						</RuledSection>
						<RuledSection title="Status codes">
							<StatusCodeList codes={report.statusCodes} />
						</RuledSection>
					</div>

					<RuledSection
						title="Sources"
						actions={
							<Segmented
								aria-label="Source breakdown"
								font="sans"
								value={tab}
								onValueChange={setTab}
								options={[
									{ value: "referrers", label: "Referrers" },
									{ value: "browsers", label: "Browsers" },
									{ value: "os", label: "OS" },
									{ value: "devices", label: "Devices" },
									{ value: "hosts", label: "Hosts" },
								]}
							/>
						}
					>
						{tab === "referrers" ? (
							<BarList
								items={report.topReferrers}
								blankLabel="Direct / none"
								empty={SOURCE_EMPTY.referrers}
							/>
						) : null}
						{tab === "browsers" ? (
							<BarList
								items={report.topBrowsers}
								blankLabel="Unknown"
								empty={SOURCE_EMPTY.browsers}
							/>
						) : null}
						{tab === "os" ? (
							<BarList
								items={report.topOS}
								blankLabel="Unknown"
								empty={SOURCE_EMPTY.os}
							/>
						) : null}
						{tab === "devices" ? (
							<BarList
								items={report.topDevices}
								blankLabel="Unknown"
								empty={SOURCE_EMPTY.devices}
							/>
						) : null}
						{tab === "hosts" ? (
							<BarList
								items={report.topHosts}
								empty={SOURCE_EMPTY.hosts}
								{...(onHostSelect ? { onSelect: onHostSelect } : {})}
							/>
						) : null}
					</RuledSection>

					<OriginsCard report={report} serverId={geoServerId} />

					<div className="grid gap-x-6 gap-y-4 @2xl:grid-cols-2">
						<RuledSection
							title="Top not found"
							subtitle="Paths that returned 404."
						>
							<BarList
								items={report.notFound}
								color="var(--color-warn)"
								empty="No 404s in this range."
							/>
						</RuledSection>
						<RuledSection
							title="Top server errors"
							subtitle="Paths with 5xx responses."
						>
							<BarList
								items={serverErrors.map((item) => ({
									...item,
									n: item.s5xx ?? 0,
								}))}
								color="var(--color-bad)"
								empty="No 5xx responses in this range."
							/>
						</RuledSection>
					</div>

					{report.activeHours ? (
						<ChartCard
							title="Active hours"
							subtitle="When requests arrive, by weekday and UTC hour."
						>
							<ActiveHours grid={report.activeHours} />
						</ChartCard>
					) : null}

					{report.unrouted && report.unrouted.requests > 0 ? (
						<RuledSection
							title="Unrouted hosts"
							subtitle={`${compactNumber(report.unrouted.requests)} requests reached this server for a hostname no service routes — scanners or a DNS record pointing here by mistake.`}
						>
							<BarList
								items={report.unrouted.hosts}
								color="var(--color-status-stopped)"
							/>
						</RuledSection>
					) : null}
				</>
			) : null}

			<p className="text-ink-3 text-xs leading-snug">
				Visitors are counted per UTC day; bots are excluded. Top lists are
				approximate for ranges of 24 hours or more. History kept:{" "}
				{report.range.retention.minuteHours} h per minute,{" "}
				{report.range.retention.hourlyDays} d per hour,{" "}
				{report.range.retention.dailyDays} d per day.
				{report.range.truncated
					? " This range reaches further back than the history kept, so older data is missing."
					: ""}
			</p>
		</div>
	);
}
