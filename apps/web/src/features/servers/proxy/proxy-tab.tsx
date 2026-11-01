import { useQuery } from "@tanstack/react-query";
import {
	CloudIcon,
	KeyRoundIcon,
	LockIcon,
	NetworkIcon,
	PlugIcon,
	RouteIcon,
	ShieldCheckIcon,
	WaypointsIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import type { ServerProxy } from "#/api/gen/types.gen";
import { ErrorState } from "#/components/common/error-state";
import { Badge } from "#/components/ui/badge";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { proxyRoutesQuery, serverProxyQuery } from "#/features/proxy/queries";
import { AgentTooOldBanner } from "#/features/proxy/ui/agent-too-old";
import type { ServerView } from "../model";
import { CertificatesCard } from "./certificates-card";
import { DeployBar } from "./deploy-bar";
import { Dns01Card } from "./dns01-card";
import { PolicyCard } from "./policy-card";
import { PortsCard } from "./ports-card";
import { L4PortsTable, RoutesTable } from "./routes-table";
import { TailnetCard } from "./tailnet-card";
import { TunnelCard } from "./tunnel-card";

/**
 * One proxy section: a `section-head` carrying the icon and the title, then
 * the section's own rows. The card inside has no second title.
 */
function Block({
	id,
	title,
	icon,
	description,
	children,
}: {
	id: string;
	title: string;
	icon: ReactNode;
	description?: string;
	children: ReactNode;
}) {
	return (
		<section
			id={id}
			aria-labelledby={`${id}-title`}
			className="flex flex-col gap-3"
		>
			<h2 id={`${id}-title`} className="section-head">
				<span aria-hidden className="flex shrink-0 text-ink-3 [&>svg]:size-3.5">
					{icon}
				</span>
				{title}
				{description ? (
					<span className="font-sans text-xs normal-case tracking-normal">
						{description}
					</span>
				) : null}
			</h2>
			{children}
		</section>
	);
}

const STATUS_KIND: Record<string, { kind: StatusKind; label: string }> = {
	ok: { kind: "live", label: "Running" },
	starting: { kind: "deploying", label: "Starting" },
	error: { kind: "failed", label: "Error" },
};

/**
 * The server's proxy: its state and TCP/UDP ports, uploaded certificates, the
 * upstream policy, the tailnet and the routes it serves. Server-level changes
 * stage until the sticky bar deploys them.
 */
export function ProxyTab({
	server,
	onOpenSettings,
}: {
	server: ServerView;
	/** Switches to the overview and scrolls to the server's settings. */
	onOpenSettings?: () => void;
}) {
	const proxy = useQuery(serverProxyQuery(server.id));
	const routes = useQuery(proxyRoutesQuery(server.id));

	return (
		<SkeletonReveal
			loading={proxy.isPending}
			skeleton={
				<div
					className="flex flex-col gap-4"
					role="status"
					aria-label="Loading proxy"
				>
					<Skeleton className="h-28 rounded-card" />
					<Skeleton className="h-52 rounded-card" />
				</div>
			}
		>
			{() =>
				proxy.data ? (
					<ProxyBody
						server={server}
						data={proxy.data}
						routesBody={
							<SkeletonReveal
								loading={routes.isPending}
								skeleton={<Skeleton className="h-24 rounded-card" />}
							>
								{() =>
									routes.isError ? (
										<ErrorState
											title="Routes did not load"
											body="The control plane did not answer."
											retry={() => void routes.refetch()}
										/>
									) : (
										<RoutesTable routes={routes.data?.items ?? []} />
									)
								}
							</SkeletonReveal>
						}
						{...(onOpenSettings ? { onOpenSettings } : {})}
					/>
				) : (
					<ErrorState
						frame="raised"
						title="The proxy did not load"
						body="The control plane did not answer, so nothing was changed."
						retry={() => void proxy.refetch()}
					/>
				)
			}
		</SkeletonReveal>
	);
}

function ProxyBody({
	server,
	data,
	routesBody,
	onOpenSettings,
}: {
	server: ServerView;
	data: ServerProxy;
	/** The Routes block's content: its own query has its own loading state. */
	routesBody: ReactNode;
	onOpenSettings?: () => void;
}) {
	const status = STATUS_KIND[data.status] ?? {
		kind: "unknown" as const,
		label: "Not reported",
	};

	return (
		<div className="flex flex-col gap-6" data-tab="proxy">
			{data.agentTooOld ? <AgentTooOldBanner serverName={server.name} /> : null}

			<Block
				id="proxy-status"
				title="Proxy"
				icon={<WaypointsIcon />}
				description="Traefik on this server, and the TCP/UDP ports it publishes."
			>
				<div className="flex flex-wrap items-center gap-3">
					<span className="inline-flex items-center gap-1.5 font-medium text-ink-1 text-xs">
						<StatusIcon kind={status.kind} label="" />
						{status.label}
					</span>
					{data.error ? (
						<span className="text-destructive-text text-xs">{data.error}</span>
					) : null}
					{data.staged ? <Badge tone="staged">Staged changes</Badge> : null}
					<span className="type-mono text-ink-3">
						generation {data.appliedGeneration}
						{data.generation > data.appliedGeneration
							? ` → ${data.generation}`
							: ""}
					</span>
				</div>
				<L4PortsTable ports={data.l4Ports ?? []} />
			</Block>

			<Block id="proxy-ports" title="Proxy ports" icon={<PlugIcon />}>
				<PortsCard
					serverId={server.id}
					serverName={server.name}
					ports={data.ports}
					l4Ports={data.l4Ports ?? []}
					agentTooOld={data.agentTooOld}
				/>
			</Block>

			<Block
				id="proxy-certificates"
				title="Certificates"
				icon={<ShieldCheckIcon />}
			>
				<CertificatesCard
					serverId={server.id}
					serverName={server.name}
					certificates={data.certificates ?? []}
					agentTooOld={data.agentTooOld}
				/>
			</Block>

			<Block
				id="proxy-tunnel"
				title="Cloudflare Tunnel"
				icon={<CloudIcon />}
				description="Acts at once; it is not staged for Deploy proxy."
			>
				<TunnelCard serverId={server.id} serverName={server.name} />
			</Block>

			<Block
				id="proxy-dns01"
				title="DNS-01 certificates"
				icon={<KeyRoundIcon />}
			>
				<Dns01Card
					serverId={server.id}
					agentTooOld={data.agentTooOld}
					{...(onOpenSettings ? { onOpenSettings } : {})}
				/>
			</Block>

			<Block id="proxy-policy" title="Upstream policy" icon={<LockIcon />}>
				<PolicyCard
					serverId={server.id}
					desired={data.upstreamPolicy}
					applied={data.appliedUpstreamPolicy}
					agentTooOld={data.agentTooOld}
				/>
			</Block>

			<Block id="proxy-tailnet" title="Tailnet" icon={<NetworkIcon />}>
				<TailnetCard
					serverId={server.id}
					serverName={server.name}
					tailnet={data.tailnet}
					agentTooOld={data.agentTooOld}
				/>
			</Block>

			<Block
				id="proxy-routes"
				title="Routes"
				icon={<RouteIcon />}
				description="What the proxy serves right now, from the last deploy."
			>
				{routesBody}
			</Block>

			<DeployBar serverId={server.id} serverName={server.name} proxy={data} />
		</div>
	);
}
