import { type Node, type NodeProps, Position } from "@xyflow/react";
import { cn } from "cn";
import {
	BoxIcon,
	GitBranchIcon,
	HardDriveIcon,
	ServerIcon,
	TimerIcon,
} from "lucide-react";
import { useReducedMotion } from "motion/react";
import { memo } from "react";
import type { Service } from "#/api/models";
import { Badge } from "#/components/ui/badge";
import { StatusPill } from "#/components/ui/lamp";
import { DatabaseLine } from "#/features/databases/card-line";
import { ReplicaChip } from "#/features/databases/replicas/card-chips";
import { MeshViaChip } from "#/features/mesh/canvas-mesh";
import { NetworkChip } from "#/features/networks";
import { hasProcesses } from "#/features/processes/model";
import { ProcessCard } from "#/features/processes/process-card";
import { NodeConfigChip } from "#/features/project-config";
import { RouteCard } from "#/features/proxy/route-card";
import { TailnetChip } from "#/features/service-settings/tailnet-node-chip";
import { StackCard } from "#/features/stacks/stack-card";
import { formatMegabytes } from "#/lib/format";
import { DIMMED_OPACITY, useCanvasUi } from "./canvas-context";
import { JackHandle } from "./jack-handle";
import type { Plugs } from "./lib/cable";
import { NODE_WIDTH } from "./lib/layout";
import { sourceSubtitle } from "./lib/source";
import {
	type CardStatus,
	cardStatus,
	type LineTone,
	type MemoryTone,
	memoryFraction,
	memoryTone,
	PHASE_LABEL,
	PHASES,
	type PhaseState,
} from "./lib/status";
import { ServiceIcon } from "./service-icon";
import { useInspectorPrefetch } from "./use-inspector-prefetch";
import { type CardMemory, useServiceMemory } from "./use-service-memory";
import { useTicker } from "./use-ticker";

export type ServiceFlowNode = Node<
	{
		service: Service;
		/** Which jacks hold a cable's plug. */
		plugs: Plugs;
	},
	"service"
>;

/** The jacks sit level with the icon tile's centre. */
const JACK_TOP = 26;

const LINE_TONE: Record<LineTone, string> = {
	ok: "text-status-live-on",
	accent: "text-status-deploying-on",
	bad: "text-status-failed-on",
	warn: "text-status-degraded-on",
	muted: "text-ink-3",
};

const PHASE_STATE_NAME: Record<PhaseState, string> = {
	off: "waiting",
	live: "running",
	done: "done",
	fail: "failed",
};

const MEMORY_FILL: Record<MemoryTone, string> = {
	ok: "from-accent-ink to-accent-brand",
	warn: "from-warn to-warn",
	bad: "from-bad to-bad",
};

/** Build, Release and Verify as three engraved LEDs with their names. */
function PhaseStrip({
	status,
	skipsBuild,
}: {
	status: CardStatus;
	/** An image service has nothing to build: its Build LED stays off. */
	skipsBuild: boolean;
}) {
	const stateOf = (index: number): PhaseState => {
		const state = status.phases[index] ?? "off";
		return skipsBuild && index === 0 && state === "done" ? "off" : state;
	};
	return (
		<div className="mt-[11px] flex items-end justify-between gap-2">
			<div
				role="img"
				aria-label={PHASES.map((phase, index) => {
					const state =
						skipsBuild && index === 0 && stateOf(index) === "off"
							? "skipped"
							: PHASE_STATE_NAME[stateOf(index)];
					const extra =
						phase === "release" && status.preDeploy ? " (pre-deploy)" : "";
					return `${PHASE_LABEL[phase]} ${state}${extra}`;
				}).join(", ")}
				className="flex gap-[5px]"
			>
				{PHASES.map((phase, index) => (
					<div
						key={phase}
						className="relative flex min-w-[47px] flex-col items-stretch gap-1"
					>
						<span className="phase-strip">
							<i
								data-s={stateOf(index)}
								className="h-[6px]! w-full! rounded-[2px]!"
							/>
						</span>
						<span className="font-mono text-[10px] text-ink-4 uppercase leading-none tracking-[0.04em]">
							{PHASE_LABEL[phase]}
						</span>
						{phase === "release" && status.preDeploy ? (
							<span
								aria-hidden
								className="absolute top-full left-0 mt-[2px] whitespace-nowrap font-mono text-[10px] text-accent-ink leading-none tracking-[0.04em]"
							>
								pre-deploy
							</span>
						) : null}
					</div>
				))}
			</div>
			<span
				title={status.detail}
				className={cn(
					"min-w-0 truncate whitespace-nowrap text-right font-semibold text-[11px] leading-none tabular-nums",
					LINE_TONE[status.lineTone],
				)}
			>
				{status.line}
			</span>
		</div>
	);
}

function MemoryRow({ memory }: { memory: CardMemory }) {
	const used = formatMegabytes(memory.usedBytes / (1024 * 1024));
	const fraction = memoryFraction(memory.usedBytes, memory.limitBytes);
	return (
		<div className="mt-2.5">
			<div className="mb-[5px] flex justify-between font-mono text-[10.5px] text-ink-3 leading-none">
				<span>Memory</span>
				<span className="tabular-nums">
					{fraction === null
						? used
						: `${used} / ${formatMegabytes(memory.limitBytes / (1024 * 1024))}`}
				</span>
			</div>
			{fraction === null ? null : (
				<>
					<meter
						className="sr-only"
						aria-label="Memory in use"
						min={0}
						max={100}
						value={Math.round(fraction * 100)}
					/>
					<div
						aria-hidden
						className="surface-well h-1 overflow-hidden rounded-full"
					>
						<div
							className={cn(
								"h-full rounded-full bg-linear-to-b",
								MEMORY_FILL[memoryTone(fraction)],
							)}
							style={{ width: `${Math.max(2, fraction * 100)}%` }}
						/>
					</div>
				</>
			)}
		</div>
	);
}

function VolumeStrip({ volumes }: { volumes: Service["volumes"] }) {
	return (
		<div className="flex flex-col gap-1 rounded-b-[11px] border-line border-t bg-surface-well px-[13px] py-2 shadow-[inset_0_2px_4px_rgb(2_4_10/0.55)]">
			{volumes.map((volume) => (
				<div
					key={volume.id}
					title={`${volume.name} at ${volume.mountPath}`}
					className="flex items-center gap-1.5 font-mono text-[10.5px] text-ink-3 leading-none"
				>
					<HardDriveIcon aria-hidden className="size-3 shrink-0" />
					<span className="shrink-0 text-ink-2">{volume.name}</span>
					<span className="ml-auto min-w-0 truncate">{volume.mountPath}</span>
				</div>
			))}
		</div>
	);
}

function ServiceCard({
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
	const reduced = useReducedMotion() ?? false;
	const base = cardStatus(service);
	const now = useTicker(base.clockFrom !== null);
	const status = base.clockFrom === null ? base : cardStatus(service, now);
	const memory = useServiceMemory(service);
	const SourceIcon = service.sourceType === "git" ? GitBranchIcon : BoxIcon;
	const beam = status.inFlight && !reduced;
	const unreachable =
		service.server?.status === "offline" ||
		service.server?.status === "revoked";
	const deployed = service.observed !== null;
	const replicas = service.scaling;
	const prefetch = useInspectorPrefetch(service);

	return (
		<div
			data-service-id={service.id}
			className="group/node relative transition-opacity duration-quick"
			style={{ width: NODE_WIDTH, opacity: dimmed ? DIMMED_OPACITY : 1 }}
			{...prefetch}
		>
			{service.stagedKind ? (
				<Badge
					tone="staged"
					title={
						service.stagedKind === "new"
							? "New service: not deployed yet"
							: `${service.pendingChangeCounts.variables + service.pendingChangeCounts.settings} staged change(s) waiting to deploy`
					}
					className="surface-float absolute -top-2.5 right-3 z-10 h-5 rounded-md px-1.5 font-sans font-semibold text-[11px]"
				>
					Staged
				</Badge>
			) : null}
			{/* Beside the Staged chip, on the other corner: what nebula.toml says of this card. */}
			<NodeConfigChip
				environmentId={service.environmentId}
				slug={service.slug}
				className="surface-float absolute -top-2.5 left-3 z-10 h-5 rounded-md px-1.5"
			/>
			<div
				className={cn(
					beam && "beam",
					status.inFlight && reduced && "ring-2 ring-accent-ink/70",
				)}
				style={{
					borderRadius: beam ? 14 : 12,
					...(beam ? { padding: 2, boxShadow: "var(--shadow-raised)" } : {}),
					outline: selected ? "2px solid var(--color-accent-ink)" : undefined,
					outlineOffset: selected ? 2 : undefined,
				}}
			>
				<div
					className={cn(
						"surface-raised relative flex flex-col rounded-[12px] text-left transition-[border-color] duration-quick hover:border-line-strong",
					)}
					style={
						beam ? { boxShadow: "none", borderColor: "transparent" } : undefined
					}
				>
					<div className="px-[13px] pt-[11px] pb-[13px]">
						<div className="flex items-center gap-2.5">
							<ServiceIcon service={service} size={28} />
							<div className="min-w-0 flex-1">
								<div
									title={service.name}
									className="truncate font-semibold text-[14px] text-ink-1 leading-[1.15] tracking-[-0.005em]"
								>
									{service.name}
								</div>
								{service.config.database ? (
									<DatabaseLine spec={service.config.database} />
								) : (
									<div
										title={sourceSubtitle(service)}
										className="mt-[3px] flex items-center gap-1 font-mono text-[11px] text-ink-3 leading-none"
									>
										<SourceIcon aria-hidden className="size-[11px] shrink-0" />
										<span className="min-w-0 truncate">
											{sourceSubtitle(service)}
										</span>
									</div>
								)}
							</div>
							<StatusPill kind={status.kind}>{status.pill}</StatusPill>
						</div>

						<PhaseStrip
							status={status}
							skipsBuild={service.sourceType === "image"}
						/>

						{service.config.database ? <ReplicaChip service={service} /> : null}

						<div className="mt-[11px] flex items-center justify-between gap-2">
							<span className="flex min-w-0 items-center gap-1.5">
								<span
									title={
										service.server
											? `Runs on ${service.server.name}`
											: "Not placed on a server"
									}
									className={cn(
										"inline-flex h-5 min-w-0 max-w-[150px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] leading-none",
										unreachable ? "text-status-degraded-on" : "text-ink-3",
									)}
								>
									<ServerIcon aria-hidden className="size-3 shrink-0" />
									<span className="truncate">
										{service.server?.name ?? "no server"}
									</span>
								</span>
								{service.tailnet ? (
									<TailnetChip tailnet={service.tailnet} />
								) : null}
								{service.networks.length > 0 ? (
									<NetworkChip networks={service.networks} />
								) : null}
								<MeshViaChip serviceId={service.id} />
							</span>
							<span className="flex items-center gap-2 text-[11.5px] text-ink-2 leading-none">
								{service.stale ? (
									<span
										title="No recent report from the server: this state may be out of date"
										className="flex items-center gap-0.5 font-mono text-[10px] text-warning-text"
									>
										<TimerIcon aria-hidden className="size-3" />
										Stale
									</span>
								) : null}
								<span>
									<span className="font-mono tabular-nums">
										{deployed
											? `${replicas.ready}/${replicas.replicas}`
											: replicas.replicas}
									</span>{" "}
									{deployed || replicas.replicas !== 1 ? "replicas" : "replica"}
								</span>
							</span>
						</div>

						{memory ? <MemoryRow memory={memory} /> : null}
					</div>
					{service.volumes.length > 0 ? (
						<VolumeStrip volumes={service.volumes} />
					) : null}
				</div>
			</div>
			<JackHandle
				type="source"
				id="left"
				position={Position.Left}
				top={JACK_TOP + (beam ? 2 : 0)}
				plugged={plugs.left}
				label={`Link jack on the left of ${service.name}`}
			/>
			<JackHandle
				type="source"
				id="right"
				position={Position.Right}
				top={JACK_TOP + (beam ? 2 : 0)}
				plugged={plugs.right}
				label={`Link jack on the right of ${service.name}`}
			/>
			{/* The Internet cable's own jack: top centre, present only while one plugs in. */}
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
			{/* Dimmed and stale must not rely on opacity alone. */}
			{dimmed ? (
				<span className="sr-only">Does not match the filter</span>
			) : null}
		</div>
	);
}

function ServiceNodeImpl({ data, selected }: NodeProps<ServiceFlowNode>) {
	const { matchIds } = useCanvasUi();
	const dimmed = matchIds !== null && !matchIds.has(data.service.id);
	if (hasProcesses(data.service)) {
		return (
			<ProcessCard
				service={data.service}
				plugs={data.plugs}
				selected={selected}
				dimmed={dimmed}
			/>
		);
	}
	if (data.service.kind === "route") {
		return (
			<RouteCard
				service={data.service}
				plugs={data.plugs}
				selected={selected}
				dimmed={dimmed}
			/>
		);
	}
	if (data.service.kind === "stack") {
		return (
			<StackCard
				service={data.service}
				plugs={data.plugs}
				selected={selected}
				dimmed={dimmed}
			/>
		);
	}
	return (
		<ServiceCard
			service={data.service}
			plugs={data.plugs}
			selected={selected}
			dimmed={dimmed}
		/>
	);
}

/**
 * Rerenders only when the service resource, its plugs or its selection
 * change: React Query hands back the same service object while a poll finds
 * nothing new, so a quiet canvas of 100 cards stays still.
 */
export const ServiceNode = memo(
	ServiceNodeImpl,
	(prev, next) =>
		prev.data.service === next.data.service &&
		prev.data.plugs.left === next.data.plugs.left &&
		prev.data.plugs.right === next.data.plugs.right &&
		prev.selected === next.selected,
);
