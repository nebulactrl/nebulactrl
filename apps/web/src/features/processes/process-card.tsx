import { Position } from "@xyflow/react";
import { cn } from "cn";
import { ServerIcon } from "lucide-react";
import type { Service } from "#/api/models";
import { Badge } from "#/components/ui/badge";
import { StatusPill } from "#/components/ui/lamp";
import { StatusIcon } from "#/components/ui/status-icon";
import { DIMMED_OPACITY } from "#/features/canvas/canvas-context";
import { JackHandle } from "#/features/canvas/jack-handle";
import type { Plugs } from "#/features/canvas/lib/cable";
import { sourceSubtitle } from "#/features/canvas/lib/source";
import { cardStatus } from "#/features/canvas/lib/status";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { useInspectorPrefetch } from "#/features/canvas/use-inspector-prefetch";
import { useTicker } from "#/features/canvas/use-ticker";
import { NodeConfigChip } from "#/features/project-config";
import { useStackNavigation } from "#/features/stacks/url";
import {
	nodeProcessSummary,
	PROCESS_NODE_WIDTH,
	processCountText,
} from "./model";

const JACK_TOP = 26;

/**
 * What a process row's icon does when its state changes: the colour and the
 * opacity ease over 160ms, nothing moves, and reduced motion changes it at
 * once. The classes on the svg are what make the colour follow; the wrapper
 * carries the same transition for the opacity of the icon as a whole.
 */
export const PROCESS_STATUS_ICON =
	"inline-flex shrink-0 transition-[color,opacity] duration-[160ms] ease-out motion-reduce:transition-none [&_svg]:transition-[color,opacity] [&_svg]:duration-[160ms] [&_svg]:ease-out motion-reduce:[&_svg]:transition-none";

/**
 * A service that runs several processes on the canvas: one node (one service
 * for selection, deploys and the inspector) with a row per process, each with
 * its status icon, start command, replicas and port. Clicking a row opens the
 * inspector on the Overview with that process highlighted (`?process=`);
 * clicking anywhere else on the card selects and opens the service. The card's
 * state is the service's rollup, so the worst process decides it. Rows appear,
 * leave and reorder instantly: only a process' icon changes, by colour and
 * opacity.
 */
export function ProcessCard({
	service,
	plugs,
	selected,
	dimmed,
}: {
	service: Service;
	plugs: Plugs;
	selected: boolean;
	dimmed: boolean;
}) {
	const { show } = useStackNavigation();
	const base = cardStatus(service);
	const now = useTicker(base.clockFrom !== null);
	const status = base.clockFrom === null ? base : cardStatus(service, now);
	const summary = nodeProcessSummary(service);
	const prefetch = useInspectorPrefetch(service);
	const unreachable =
		service.server?.status === "offline" ||
		service.server?.status === "revoked";

	return (
		<div
			data-service-id={service.id}
			data-process-card="true"
			className="group/node relative transition-opacity duration-quick"
			style={{
				width: PROCESS_NODE_WIDTH,
				opacity: dimmed ? DIMMED_OPACITY : 1,
			}}
			{...prefetch}
		>
			{service.stagedKind ? (
				<Badge
					tone="staged"
					title={
						service.stagedKind === "new"
							? "New service: not deployed yet"
							: "Staged changes waiting to deploy"
					}
					className="surface-float absolute -top-2.5 right-3 z-10 h-5 rounded-md px-1.5 font-sans font-semibold text-[11px]"
				>
					Staged
				</Badge>
			) : null}
			<NodeConfigChip
				environmentId={service.environmentId}
				slug={service.slug}
				className="surface-float absolute -top-2.5 left-3 z-10 h-5 rounded-md px-1.5"
			/>
			<div
				className="surface-raised rounded-[12px] text-left"
				style={{
					outline: selected ? "2px solid var(--color-accent-ink)" : undefined,
					outlineOffset: selected ? 2 : undefined,
				}}
			>
				<div className="flex items-center gap-2.5 px-[13px] pt-[11px] pb-2">
					<ServiceIcon service={service} size={28} />
					<div className="min-w-0 flex-1">
						<div
							title={service.name}
							className="truncate font-semibold text-[14px] text-ink-1 leading-[1.15]"
						>
							{service.name}
						</div>
						<div
							title={sourceSubtitle(service)}
							className="mt-[3px] truncate font-mono text-[11px] text-ink-3 leading-none"
						>
							{processCountText(summary.total)} · {sourceSubtitle(service)}
						</div>
					</div>
					<StatusPill kind={status.kind}>{status.pill}</StatusPill>
				</div>
				<ul
					aria-label={`Processes of ${service.name}`}
					className="m-0 flex list-none flex-col border-line border-t p-0"
				>
					{summary.shown.length === 0 ? (
						<li className="px-[13px] py-1.5 text-[11.5px] text-ink-3">
							No processes yet
						</li>
					) : null}
					{summary.shown.map((row) => (
						<li key={row.name}>
							<button
								type="button"
								onClick={() =>
									show({
										service: service.id,
										tab: "overview",
										process: row.name,
									})
								}
								title={`${row.name}: ${row.stateLabel}`}
								className="nodrag flex h-7 w-full cursor-pointer items-center gap-2 px-[13px] text-left outline-none hover:bg-white/5 focus-visible:focus-ring"
							>
								<span className={PROCESS_STATUS_ICON}>
									<StatusIcon kind={row.kind} label="" />
								</span>
								<span className="shrink-0 font-medium font-mono text-[11.5px] text-ink-1">
									{row.name}
								</span>
								{row.showDefaultTag ? (
									<span className="shrink-0 text-[10.5px] text-ink-3">
										default
									</span>
								) : null}
								<span className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-ink-3">
									{row.commandSummary}
								</span>
								{row.badge ? (
									<span
										className={cn(
											"shrink-0 text-[10.5px]",
											row.badge === "staged"
												? "text-status-staged-on"
												: "text-ink-3",
										)}
									>
										{row.badge}
									</span>
								) : null}
								<span className="shrink-0 font-mono text-[10.5px] text-ink-2 tabular-nums">
									{row.replicasText}
								</span>
								{row.port > 0 ? (
									<span className="shrink-0 font-mono text-[10.5px] text-ink-3 tabular-nums">
										:{row.port}
									</span>
								) : null}
								<span className="sr-only"> {row.stateLabel}</span>
							</button>
						</li>
					))}
					{summary.more > 0 ? (
						<li className="flex h-7 items-center px-[13px] font-mono text-[11px] text-ink-3">
							+{summary.more} more
						</li>
					) : null}
				</ul>
				<div className="flex items-center justify-between gap-2 border-line border-t px-[13px] py-2">
					<span
						title={
							service.server
								? `Runs on ${service.server.name}`
								: "Not placed on a server"
						}
						className={cn(
							"inline-flex h-5 min-w-0 max-w-[170px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] leading-none",
							unreachable ? "text-status-degraded-on" : "text-ink-3",
						)}
					>
						<ServerIcon aria-hidden className="size-3 shrink-0" />
						<span className="truncate">
							{service.server?.name ?? "no server"}
						</span>
					</span>
					<span
						title={status.detail}
						className="min-w-0 truncate text-right font-semibold text-[11px] text-ink-2 leading-none"
					>
						{status.line}
					</span>
				</div>
			</div>
			<JackHandle
				type="source"
				id="left"
				position={Position.Left}
				top={JACK_TOP}
				plugged={plugs.left}
				label={`Link jack on the left of ${service.name}`}
			/>
			<JackHandle
				type="source"
				id="right"
				position={Position.Right}
				top={JACK_TOP}
				plugged={plugs.right}
				label={`Link jack on the right of ${service.name}`}
			/>
			{plugs.top ? (
				<JackHandle
					type="target"
					id="top"
					position={Position.Top}
					plugged
					connectable={false}
					label={`Internet jack on top of ${service.name}`}
				/>
			) : null}
			{dimmed ? (
				<span className="sr-only">Does not match the filter</span>
			) : null}
		</div>
	);
}
