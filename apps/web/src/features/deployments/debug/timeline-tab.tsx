import { cn } from "cn";
import { CircleDashedIcon } from "lucide-react";
import { useMemo } from "react";
import type { EventView } from "#/api/gen/types.gen";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Skeleton } from "#/components/ui/skeleton";
import { EventMark } from "../deployment-detail";
import { stepLabel } from "../labels";
import {
	buildTimeline,
	clockWithMs,
	formatOffset,
	summarizeTimeline,
	type TimelineRow,
} from "./timeline-model";

const COLUMNS = "grid-cols-[92px_88px_80px_minmax(0,1fr)]";

function rowLabel(row: TimelineRow): string {
	return row.step === "status"
		? `status → ${row.status.replaceAll("_", " ")}`
		: stepLabel(row.step);
}

/**
 * Every raw event, in the order the control plane recorded them: each step's
 * `started` and result and every status transition, which the folded Steps
 * view hides. Offsets are from the deployment's creation and from the event
 * before it, so a slow step shows as a long gap.
 */
export function TimelineTab({
	events,
	pending,
	error,
	onRetry,
	originIso,
	live,
}: {
	events: readonly EventView[] | undefined;
	pending: boolean;
	error: boolean;
	onRetry(): void;
	originIso: string;
	/** The deployment is still running: only then does its newest `started` row spin. */
	live: boolean;
}) {
	const rows = useMemo(
		() => buildTimeline(events ?? [], originIso),
		[events, originIso],
	);
	const summary = useMemo(() => summarizeTimeline(rows), [rows]);
	const slowestRow = rows.find((row) => row.id === summary.slowest?.id);

	if (pending) {
		return (
			<div className="flex flex-col gap-2 p-5" aria-busy>
				{["a", "b", "c", "d", "e"].map((key) => (
					<Skeleton key={key} className="h-6 w-full" />
				))}
			</div>
		);
	}
	if (error) {
		return (
			<ErrorState
				title="The timeline did not load"
				body="Nothing was changed. Try again."
				retry={onRetry}
				className="p-5"
			/>
		);
	}
	if (rows.length === 0) {
		return (
			<EmptyState
				illustration="radar"
				title="No events yet"
				body="The control plane records an event when a deployment changes status and when the agent starts or finishes a step."
				frame="none"
				className="p-5"
			/>
		);
	}

	return (
		<div className="flex min-w-0 flex-col">
			<p className="border-line border-b px-5 py-3 text-ink-3 text-xs">
				{summary.count} events
				{summary.totalMs === null
					? ""
					: ` · ${formatOffset(summary.totalMs).replace("+", "")} from creation to the last event`}
				{summary.slowest && slowestRow
					? ` · longest wait ${formatOffset(summary.slowest.ms).replace("+", "")} before ${rowLabel(slowestRow)} ${slowestRow.step === "status" ? "" : slowestRow.status}`.trimEnd()
					: ""}
			</p>
			<div
				aria-hidden
				className={cn(
					"type-micro sticky top-0 z-10 grid items-center gap-x-3 border-line border-b bg-surface-float px-5 py-2 text-ink-3",
					COLUMNS,
				)}
			>
				<span>Time</span>
				<span className="text-right">Since start</span>
				<span className="text-right">Since last</span>
				<span>Event</span>
			</div>
			<ol aria-label="Raw deployment events" className="flex flex-col">
				{rows.map((row, index) => {
					const failed = row.status === "failed";
					const slow = row.id === summary.slowest?.id;
					return (
						<li
							key={row.id}
							data-slowest={slow || undefined}
							className={cn(
								"grid items-baseline gap-x-3 border-line-faint border-b px-5 py-1.5 font-mono text-[11.5px] leading-[1.5] last:border-b-0",
								COLUMNS,
							)}
						>
							<time
								dateTime={row.at}
								className="text-ink-3 tabular-nums"
								title={row.at}
							>
								{clockWithMs(row.at)}
							</time>
							<span className="text-right text-ink-2 tabular-nums">
								{formatOffset(row.sinceStart)}
							</span>
							<span
								className={cn(
									"text-right tabular-nums",
									slow ? "font-medium text-status-degraded-on" : "text-ink-3",
								)}
								title={slow ? "The longest wait of this deployment" : undefined}
							>
								{formatOffset(row.sinceLast)}
							</span>
							<span className="flex min-w-0 flex-col gap-0.5 font-sans">
								<span className="flex items-center gap-2">
									<span className="grid size-4 shrink-0 place-items-center">
										{row.status === "started" &&
										!(live && index === rows.length - 1) ? (
											<CircleDashedIcon
												aria-hidden
												className="size-3 text-ink-4"
											/>
										) : (
											<EventMark status={row.status} />
										)}
									</span>
									<span
										className={cn(
											"min-w-0 truncate font-medium text-xs",
											failed ? "text-status-failed-on" : "text-ink-1",
										)}
									>
										{rowLabel(row)}
										{row.step === "status" ? null : (
											<span className="font-normal text-ink-3">
												{" "}
												· {row.status}
											</span>
										)}
									</span>
									{slow ? (
										<span className="type-micro shrink-0 text-status-degraded-on">
											longest wait
										</span>
									) : null}
								</span>
								{row.message ? (
									<span className="whitespace-pre-wrap break-words pl-6 text-[11.5px] text-ink-3 leading-[1.5]">
										{row.message}
									</span>
								) : null}
							</span>
						</li>
					);
				})}
			</ol>
		</div>
	);
}
