import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { useMemo, useState } from "react";
import type { Service } from "#/api/models";
import { Relative } from "#/components/common/relative";
import { Button } from "#/components/ui/button";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { StatusIcon } from "#/components/ui/status-icon";
import { formatMs } from "#/features/metrics/chart-format";
import { isStack } from "#/features/stacks/model";
import { AvailabilityText } from "#/features/uptime/availability-text";
import {
	AVAILABILITY_WINDOWS,
	buildCells,
	CHECK_INTERVAL_SECONDS,
	checksStoppedAt,
	DEFAULT_RANGE,
	failingHint,
	formatSpan,
	formatUtcStamp,
	latencySummary,
	STRIP_RANGES,
	type StripRange,
	stateView,
} from "#/features/uptime/model";
import { serviceUptimeQuery } from "#/features/uptime/queries";
import { ToneLegend, UptimeStrip } from "#/features/uptime/uptime-strip";
import { useInspectorActions } from "../inspector-context";
import { liveUptimeError } from "./model";
import { OverviewSection } from "./section";

const RANGE_OPTIONS = STRIP_RANGES.map((value) => ({
	value,
	label: value,
}));

/** One availability window: a micro label over the figure. */
function Figure({ label, percent }: { label: string; percent: number | null }) {
	return (
		<div className="flex min-w-0 flex-col gap-1.5">
			<dt className="type-micro">{label}</dt>
			<dd
				className={cn(
					"truncate font-semibold text-lg leading-none tabular-nums",
					percent === null ? "text-ink-4" : "text-ink-1",
				)}
			>
				<AvailabilityText percent={percent} />
			</dd>
		</div>
	);
}

/**
 * Uptime: the state the control plane judged from the agent's checks, how
 * long it has held, availability over 24 hours to 90 days, one strip of the
 * checks with a range toggle (1h to 90d), latency and the latest outages. A
 * stack without a check yet shows nothing; any other service says it has no
 * checks yet and why.
 */
export function UptimeCard({ service }: { service: Service }) {
	const { openTab } = useInspectorActions();
	const [range, setRange] = useState<StripRange>(DEFAULT_RANGE);
	const recent = useQuery(serviceUptimeQuery(service.id, range));
	const report = recent.data;
	const stack = isStack(service);

	const paused = report?.state === "paused";
	const cells = useMemo(
		() =>
			report
				? buildCells(
						report.series,
						report.granularity,
						paused ? report.since : null,
					)
				: [],
		[report, paused],
	);
	const latency = useMemo(() => latencySummary(report?.series), [report]);

	if (recent.isPending) {
		return (
			<OverviewSection id="overview-uptime" title="Uptime">
				<Skeleton className="h-40 w-full rounded-card" />
			</OverviewSection>
		);
	}
	if (!report) {
		if (stack) return null;
		return (
			<OverviewSection id="overview-uptime" title="Uptime">
				<p className="py-2 text-ink-3 text-xs">
					Uptime did not load. It will refresh by itself.
				</p>
			</OverviewSection>
		);
	}

	const view = stateView(report.state);
	const hasData =
		report.lastCheckAt !== null ||
		AVAILABILITY_WINDOWS.some((w) => report.availability[w] !== null) ||
		(report.incidents ?? []).length > 0;
	const action = {
		label: "Settings",
		onClick: () => openTab("settings"),
	};

	if (!hasData) {
		if (stack) return null;
		const off = !report.monitor.enabled;
		return (
			<OverviewSection id="overview-uptime" title="Uptime" action={action}>
				<div className="flex flex-col gap-1.5 py-1">
					<p className="flex items-center gap-2 font-semibold text-ink-1 text-sm leading-none">
						<StatusIcon kind={off ? "stopped" : "unknown"} label="" />
						{off ? "Monitoring is off" : "No checks yet"}
					</p>
					<p className="type-secondary">
						{off
							? "Turn it on in Settings to probe this service and record its availability."
							: `The agent probes a running service every ${CHECK_INTERVAL_SECONDS} seconds; availability appears after the first check.`}
					</p>
				</div>
			</OverviewSection>
		);
	}

	const hint = failingHint(report);
	const outages = (report.incidents ?? []).slice(0, 5);
	// Checks that stopped arriving: say since when, not "No data since 1d ago",
	// and hide what no longer describes the service (the empty 24h figure and
	// the last error of an earlier failure).
	const stoppedAt = checksStoppedAt({
		state: report.state,
		monitored: report.monitor.enabled,
		lastCheckAt: report.lastCheckAt,
	});
	const lastError = stoppedAt === null ? liveUptimeError(report) : null;

	return (
		<OverviewSection id="overview-uptime" title="Uptime" action={action}>
			<div className="rule-rows">
				<div className="flex flex-col gap-1.5 py-2">
					<div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
						<StatusIcon kind={view.kind} label="" className="size-4" />
						<span className="font-semibold text-ink-1 text-sm leading-none">
							{view.label}
						</span>
						{stoppedAt !== null ? (
							<>
								<span className="text-ink-3 text-xs leading-none">
									No checks since {formatUtcStamp(stoppedAt)}
								</span>
								<Button
									variant="link"
									size="default"
									className="font-sans text-xs max-[759px]:min-h-10"
									onClick={() => openTab("settings")}
								>
									Monitor settings
								</Button>
							</>
						) : report.since ? (
							<span className="text-ink-3 text-xs leading-none">
								since <Relative iso={report.since} />
							</span>
						) : null}
						{hint ? (
							<span
								title="Failed checks in a row. An outage opens when the count reaches the threshold."
								className="flex items-center gap-1.5 font-medium text-status-staged-on text-xs leading-none"
							>
								<StatusIcon kind="warning" label="" />
								{hint}
							</span>
						) : null}
					</div>
					{lastError ? (
						<p
							title={lastError}
							className="break-words font-mono text-2xs text-status-failed-on leading-[1.4]"
						>
							Last error: {lastError}
						</p>
					) : null}
				</div>

				<dl className="grid grid-cols-4 gap-3 py-3">
					{AVAILABILITY_WINDOWS.filter(
						(span) => !(stoppedAt !== null && span === "24h"),
					).map((span) => (
						<Figure
							key={span}
							label={span}
							percent={report.availability[span]}
						/>
					))}
				</dl>

				<div className="flex flex-col gap-2 py-2">
					<div className="flex items-center justify-between gap-3">
						<span className="type-label">Recent checks</span>
						<Segmented<StripRange>
							aria-label="Range of the recent checks"
							value={range}
							onValueChange={setRange}
							options={RANGE_OPTIONS}
						/>
					</div>
					<div
						className={cn(
							recent.isPlaceholderData && "opacity-60 transition-opacity",
						)}
					>
						<UptimeStrip
							cells={cells}
							granularity={report.granularity}
							range={report.range}
							label={`Recent checks of ${service.name}`}
							outages={report.incidents ?? []}
							lastCheckAt={report.lastCheckAt}
							lastError={report.lastError}
						/>
					</div>
				</div>

				<ToneLegend cells={cells} />

				<dl className="grid grid-cols-2 gap-3 py-3">
					<div className="flex flex-col gap-1.5">
						<dt className="type-micro">Average latency · {range}</dt>
						<dd className="font-semibold text-ink-1 text-sm leading-none tabular-nums">
							{latency.avg === null ? "—" : formatMs(latency.avg)}
						</dd>
					</div>
					<div className="flex flex-col gap-1.5">
						<dt className="type-micro">Slowest check · {range}</dt>
						<dd className="font-semibold text-ink-1 text-sm leading-none tabular-nums">
							{latency.max === null ? "—" : formatMs(latency.max)}
						</dd>
					</div>
				</dl>

				<div className="flex flex-col gap-1 py-2">
					<span className="type-label">Recent outages</span>
					{outages.length === 0 ? (
						<p className="py-1 text-ink-3 text-xs">No outages recorded.</p>
					) : (
						<ol className="rule-rows">
							{outages.map((outage) => (
								<li
									key={outage.id}
									className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3"
								>
									<StatusIcon
										kind={outage.resolvedAt ? "stopped" : "failed"}
										label=""
									/>
									<div className="flex min-w-0 flex-col gap-1.5">
										<span className="truncate font-medium text-ink-1 text-xs leading-none">
											{outage.resolvedAt ? "Outage" : "Ongoing outage"} ·{" "}
											{formatSpan(outage.durationSeconds)}
										</span>
										{outage.cause ? (
											<span
												title={outage.cause}
												className="truncate text-[10.5px] text-ink-3 leading-none"
											>
												{outage.cause}
											</span>
										) : null}
									</div>
									<div className="text-[10.5px] text-ink-3 leading-none">
										<Relative iso={outage.startedAt} />
									</div>
								</li>
							))}
						</ol>
					)}
				</div>
			</div>
		</OverviewSection>
	);
}
