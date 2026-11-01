import { useQuery } from "@tanstack/react-query";
import { Position } from "@xyflow/react";
import { cn } from "cn";
import { GlobeIcon, NetworkIcon, ServerIcon, TimerIcon } from "lucide-react";
import { useReducedMotion } from "motion/react";
import { servicesQuery } from "#/api/hooks";
import type { Service } from "#/api/models";
import { Badge } from "#/components/ui/badge";
import { StatusPill } from "#/components/ui/lamp";
import { DIMMED_OPACITY } from "#/features/canvas/canvas-context";
import { JackHandle } from "#/features/canvas/jack-handle";
import type { Plugs } from "#/features/canvas/lib/cable";
import { NODE_WIDTH } from "#/features/canvas/lib/layout";
import { cardStatus, type LineTone } from "#/features/canvas/lib/status";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { useTicker } from "#/features/canvas/use-ticker";
import { routeBadge, routeSubtitle } from "./lib/l4";
import { routeLeds } from "./lib/route-leds";

const LINE_TONE: Record<LineTone, string> = {
	ok: "text-status-live-on",
	accent: "text-status-deploying-on",
	bad: "text-status-failed-on",
	warn: "text-status-degraded-on",
	muted: "text-ink-3",
};

const LED_NAME = {
	off: "waiting",
	live: "running",
	done: "done",
	fail: "failed",
} as const;

const JACK_TOP = 26;

/** The host chip: the first hostname, with how many more there are. */
function hostChip(service: Service): string | null {
	const first = service.domains[0];
	if (!first) return null;
	const more = service.domains.length - 1;
	return more > 0 ? `${first.hostname} +${more}` : first.hostname;
}

/**
 * A route service on the canvas: an external address or a TCP/UDP port that
 * the server's proxy publishes. No container, so no memory, replicas or
 * build: its LEDs are Route and Verify, its subtitle the address it forwards
 * to, and its badge the protocol.
 */
export function RouteCard({
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
	const route = service.config.route;
	const siblings = useQuery(servicesQuery(service.environmentId));
	const nameOf = (id: string) => siblings.data?.find((s) => s.id === id)?.name;
	const subtitle = routeSubtitle(route, nameOf, service.privateHost);
	const badge = routeBadge(route);
	const leds = routeLeds(
		status.phases,
		route?.protocol === "tcp" || route?.protocol === "udp"
			? route.protocol
			: "http",
	);
	const host = hostChip(service);
	const beam = status.inFlight && !reduced;
	const unreachable =
		service.server?.status === "offline" ||
		service.server?.status === "revoked";
	const SubtitleIcon = badge === "HTTP" ? GlobeIcon : NetworkIcon;

	return (
		<div
			data-service-id={service.id}
			data-route-protocol={badge.toLowerCase()}
			className="group/node relative transition-opacity duration-quick"
			style={{ width: NODE_WIDTH, opacity: dimmed ? DIMMED_OPACITY : 1 }}
		>
			{service.stagedKind ? (
				<Badge
					tone="staged"
					title={
						service.stagedKind === "new"
							? "New route: not deployed yet"
							: "Staged changes waiting to deploy"
					}
					className="surface-float absolute -top-2.5 right-3 z-10 h-5 rounded-md px-1.5 font-sans font-semibold text-[11px]"
				>
					Staged
				</Badge>
			) : null}
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
					className="surface-raised relative flex flex-col rounded-[12px] text-left transition-[border-color] duration-quick hover:border-line-strong"
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
								<div
									title={subtitle}
									className="mt-[3px] flex items-center gap-1 font-mono text-[11px] text-ink-3 leading-none"
								>
									<SubtitleIcon aria-hidden className="size-[11px] shrink-0" />
									<span className="min-w-0 truncate">{subtitle}</span>
								</div>
							</div>
							<StatusPill kind={status.kind}>{status.pill}</StatusPill>
						</div>

						<div className="mt-[11px] flex items-end justify-between gap-2">
							<div
								role="img"
								aria-label={leds
									.map((led) => `${led.label} ${LED_NAME[led.state]}`)
									.join(", ")}
								className="flex gap-[5px]"
							>
								{leds.map((led) => (
									<div
										key={led.label}
										className="flex min-w-[47px] flex-col items-stretch gap-1"
									>
										<span className="phase-strip">
											<i
												data-s={led.state}
												className="h-[6px]! w-full! rounded-[2px]!"
											/>
										</span>
										<span className="font-mono text-[10px] text-ink-4 uppercase leading-none tracking-[0.04em]">
											{led.label}
										</span>
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

						<div className="mt-[11px] flex items-center gap-1.5">
							<Badge shape="tag" className="font-mono text-[10px]">
								{badge}
							</Badge>
							{host ? (
								<span
									title={`Answers for ${host}`}
									className="inline-flex h-5 min-w-0 max-w-[130px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] text-ink-3 leading-none"
								>
									<GlobeIcon aria-hidden className="size-3 shrink-0" />
									<span className="truncate">{host}</span>
								</span>
							) : null}
							<span
								title={
									service.server
										? `Published by ${service.server.name}`
										: "Not placed on a server"
								}
								className={cn(
									"ml-auto inline-flex h-5 min-w-0 max-w-[110px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] leading-none",
									unreachable ? "text-status-degraded-on" : "text-ink-3",
								)}
							>
								<ServerIcon aria-hidden className="size-3 shrink-0" />
								<span className="truncate">
									{service.server?.name ?? "no server"}
								</span>
							</span>
							{service.stale ? (
								<span
									title="No recent report from the server: this state may be out of date"
									className="flex items-center gap-0.5 font-mono text-[10px] text-warning-text"
								>
									<TimerIcon aria-hidden className="size-3" />
									Stale
								</span>
							) : null}
						</div>
					</div>
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
