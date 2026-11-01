import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import {
	BugIcon,
	CheckIcon,
	CircleDashedIcon,
	TriangleAlertIcon,
	XIcon,
} from "lucide-react";
import { useState } from "react";
import { deploymentEventsQuery } from "#/api/hooks";
import type { Deployment } from "#/api/models";
import { useLogStream } from "#/api/sse";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import { Spinner } from "#/components/ui/spinner";
import {
	BuiltOnChips,
	PlacementNotices,
} from "#/features/build-placement/deployment-placement";
import { isPlacementEvent } from "#/features/build-placement/model";
import { DownloadLogsButton } from "#/features/logs/download/download-logs-button";
import { LogView } from "#/features/logs/log-view";
import { useDebugParam, useDebugShortcut } from "./debug/use-debug-param";
import { isInFlight, stepLabel } from "./labels";
import { WaitingForServer } from "./recovery-banner";
import { foldSteps, stepDuration } from "./steps-model";

function clock(iso: string): string {
	const date = new Date(iso);
	return Number.isNaN(date.getTime())
		? ""
		: date.toLocaleTimeString([], { hour12: false });
}

/**
 * The glyph of a timeline step: a check, a cross, a warning triangle (a step
 * that failed without failing the deploy), a spinner or a dashed circle.
 */
export function EventMark({ status }: { status: string }) {
	if (status === "succeeded")
		return <CheckIcon aria-hidden className="size-3 text-status-live-on" />;
	if (status === "failed")
		return <XIcon aria-hidden className="size-3 text-status-failed-on" />;
	if (status === "warning")
		return (
			<TriangleAlertIcon
				aria-hidden
				className="size-3 text-status-degraded-on"
			/>
		);
	if (status === "started")
		return <Spinner aria-hidden role="presentation" className="size-3" />;
	return <CircleDashedIcon aria-hidden className="size-3 text-ink-4" />;
}

/**
 * One deployment opened: the step timeline (one row per step, folded from
 * its start and result, with its duration; outcomes such as a rollback get
 * their own row) and the build/deploy log. The log is a live follow while
 * the deployment runs and the stored lines once it has finished.
 */
export function DeploymentDetail({ deployment }: { deployment: Deployment }) {
	const events = useQuery(deploymentEventsQuery(deployment.id));
	const running = isInFlight(deployment.status);
	const debug = useDebugParam();
	// The debug panel follows this deployment's log itself (with the debug
	// stream added); the tab holds at most two live streams, so this view lets
	// go of its own while the panel is open.
	const stream = useLogStream({
		kind: "deployment",
		id: deployment.id,
		maxLines: 10_100,
		enabled: debug.deploymentId === undefined,
	});
	const [follow, setFollow] = useState(true);
	const all = events.data ?? [];
	const notices = all.filter(isPlacementEvent);
	const steps = foldSteps(all.filter((event) => !isPlacementEvent(event)));
	useDebugShortcut(() => debug.open(deployment.id));
	return (
		<div className="flex min-w-0 flex-col gap-4 p-3.5">
			{running ? <WaitingForServer deployment={deployment} /> : null}
			<BuiltOnChips
				buildServerId={deployment.buildServerId}
				serverId={deployment.serverId}
			/>
			<PlacementNotices events={notices} />

			<div>
				<div className="mb-2.5 flex items-center gap-2">
					<h4 className="type-label">Steps</h4>
					<Button
						variant="ghost"
						size="xs"
						className="ml-auto"
						onClick={() => debug.open(deployment.id)}
						aria-keyshortcuts="d"
						title="Open the debug panel: raw events, debug log, release and agent commands"
					>
						<BugIcon aria-hidden />
						Debug
						<Kbd variant="key">D</Kbd>
					</Button>
				</div>
				{events.isPending ? (
					<p className="type-secondary">Loading steps…</p>
				) : steps.length === 0 ? (
					<p className="type-secondary">No steps recorded yet.</p>
				) : (
					<ol className="flex flex-col" aria-label="Deployment steps">
						{steps.map((row, index) => {
							const last = index === steps.length - 1;
							const failed = row.state === "failed";
							const warned = row.state === "warning";
							const outcome = row.step === "status";
							const label = outcome
								? row.state.replaceAll("_", " ")
								: stepLabel(row.step);
							const took = stepDuration(row);
							return (
								<li
									key={row.key}
									className="grid grid-cols-[20px_minmax(0,1fr)] gap-x-2.5"
								>
									<span className="relative flex flex-col items-center">
										<span className="surface-well grid size-5 shrink-0 place-items-center rounded-full">
											<EventMark status={row.state} />
										</span>
										{last ? null : (
											<span
												aria-hidden
												className="my-0.5 w-px flex-1 bg-line"
											/>
										)}
									</span>
									<span
										className={cn(
											"flex min-w-0 flex-col gap-0.5",
											last ? "pb-0" : "pb-2.5",
										)}
									>
										<span className="flex items-baseline gap-2 text-xs leading-5">
											<span
												className={cn(
													"min-w-0 truncate font-medium",
													failed
														? "text-status-failed-on"
														: warned
															? "text-status-degraded-on"
															: "text-ink-1",
												)}
												title={outcome ? label : `${label} · ${row.state}`}
											>
												{label}
												{!outcome && row.state === "started" ? (
													<span className="font-normal text-ink-3">
														{" "}
														· running
													</span>
												) : null}
											</span>
											<span
												className="ml-auto shrink-0 font-mono text-[11px] text-ink-4 tabular-nums"
												title={`Started ${clock(row.startedAt)}`}
											>
												{took || clock(row.startedAt)}
											</span>
										</span>
										{row.message ? (
											<span
												className="line-clamp-2 break-words text-[11.5px] text-ink-3 leading-[1.45]"
												title={row.message}
											>
												{row.message}
											</span>
										) : null}
									</span>
								</li>
							);
						})}
					</ol>
				)}
			</div>

			<div>
				<h4 className="type-label mb-2.5 flex items-center gap-2">
					Build and deploy log
					<span className="font-normal text-[11px] text-ink-3">
						{running
							? stream.status === "live"
								? "following live"
								: "connecting…"
							: `${stream.lines.length} lines`}
					</span>
					<span className="ml-auto font-normal">
						<DownloadLogsButton
							target={{ kind: "deployment", id: deployment.id }}
						/>
					</span>
				</h4>
				<LogView
					className="surface-well h-[260px] rounded-card"
					lines={stream.lines}
					follow={follow && running}
					onLetGo={() => setFollow(false)}
					label="Deployment log"
					showStream
					empty={
						running ||
						stream.status === "connecting" ||
						stream.status === "waiting"
							? "Waiting for the first log line…"
							: "No log lines were recorded for this deployment."
					}
				/>
			</div>
		</div>
	);
}
