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
import { cardStatus } from "#/features/canvas/lib/status";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { componentCountText, nodeSummary, STACK_NODE_WIDTH } from "./model";
import { useStackNavigation } from "./url";

const JACK_TOP = 26;

/**
 * A Compose stack on the canvas: one node (one service for selection, deploys
 * and the inspector) drawn as a group-style card. A dashed frame holds the
 * header (name, aggregate state, staged chip) and one row per component with
 * its status icon, first route and restarts. A row opens the inspector on the
 * Overview with that component highlighted (`?component=`).
 */
export function StackCard({
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
	const status = cardStatus(service);
	const summary = nodeSummary(service);
	const unreachable =
		service.server?.status === "offline" ||
		service.server?.status === "revoked";

	return (
		<div
			data-service-id={service.id}
			data-stack="true"
			className="group/node relative transition-opacity duration-quick"
			style={{ width: STACK_NODE_WIDTH, opacity: dimmed ? DIMMED_OPACITY : 1 }}
		>
			{service.stagedKind ? (
				<Badge
					tone="staged"
					title={
						service.stagedKind === "new"
							? "New stack: not deployed yet"
							: "Staged changes waiting to deploy"
					}
					className="surface-float absolute -top-2.5 right-3 z-10 h-5 rounded-md px-1.5 font-sans font-semibold text-[11px]"
				>
					Staged
				</Badge>
			) : null}
			<div
				className="surface-raised rounded-[12px] border border-line-control border-dashed text-left"
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
						<div className="mt-[3px] font-mono text-[11px] text-ink-3 leading-none">
							Compose · {componentCountText(summary.total)}
						</div>
					</div>
					<StatusPill kind={status.kind}>{status.pill}</StatusPill>
				</div>
				<ul
					aria-label={`Components of ${service.name}`}
					className="m-0 flex list-none flex-col border-line border-t p-0"
				>
					{summary.shown.length === 0 ? (
						<li className="px-[13px] py-1.5 text-[11.5px] text-ink-3">
							No components yet
						</li>
					) : null}
					{summary.shown.map((component) => (
						<li key={component.name}>
							<button
								type="button"
								onClick={() =>
									show({
										service: service.id,
										tab: "overview",
										component: component.name,
									})
								}
								title={`${component.name}: ${component.state.label}`}
								className="nodrag flex h-7 w-full cursor-pointer items-center gap-2 px-[13px] text-left outline-none hover:bg-white/5 focus-visible:focus-ring"
							>
								<StatusIcon
									kind={component.state.kind}
									label={component.state.label}
								/>
								<span className="shrink-0 font-medium font-mono text-[11.5px] text-ink-1">
									{component.name}
								</span>
								<span className="min-w-0 flex-1 truncate text-right font-mono text-[10.5px] text-ink-3">
									{component.route ?? ""}
								</span>
								{component.restarts > 0 ? (
									<span className="shrink-0 font-mono text-[10.5px] text-warning-text">
										{component.restarts}↻
										<span className="sr-only"> restarts</span>
									</span>
								) : null}
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
