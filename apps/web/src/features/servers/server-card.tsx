import { DitherCapacityStat } from "@nebulactrl/dither/charts/meter";
import { Link } from "@tanstack/react-router";
import { cn } from "cn";
import { ServerIcon } from "lucide-react";
import { memo } from "react";
import { renderKpi } from "#/components/common/kpi-number";
import { Relative } from "#/components/common/relative";
import { ToneIcon } from "#/components/common/tone-icon";
import { Badge } from "#/components/ui/badge";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { roleBadge } from "#/features/build-placement/model";
import { DrainChip } from "#/features/fleet/drain-chip";
import { percentOf } from "#/features/fleet/model";
import { vt } from "#/lib/transitions";
import { AgentSkew } from "./agent-upgrade/agent-upgrade";
import { diskPercent, isOnline, type ServerView, usageText } from "./model";
import { updatesStatus, updatesText } from "./patching/patching-model";
import { ServerCardMenu } from "./server-card-menu";
import { ServerStatus } from "./ui/server-status";

const PROXY: Record<string, { kind: StatusKind; label: string }> = {
	ok: { kind: "healthy", label: "Proxy ok" },
	starting: { kind: "building", label: "Proxy starting" },
};

/** The proxy line: an icon and "Proxy ok", or the reason it is not. */
export function ProxyLine({ proxy }: { proxy: ServerView["proxy"] }) {
	if (proxy.status === "error") {
		return (
			<span className="flex min-w-0 items-center gap-1.5 text-destructive-text text-xs leading-snug">
				<StatusIcon kind="failed" label="" />
				<span className="truncate" title={proxy.error}>
					Proxy error{proxy.error ? `: ${proxy.error}` : ""}
				</span>
			</span>
		);
	}
	const { kind, label } = PROXY[proxy.status] ?? {
		kind: "unknown" as const,
		label: "Proxy not reported",
	};
	return (
		<span className="flex min-w-0 items-center gap-1.5 text-ink-3 text-xs leading-snug">
			<StatusIcon kind={kind} label="" />
			<span className="truncate">{label}</span>
		</span>
	);
}

/** "agent v0.1.0 · docker 27.3 · arm64", or that the agent has not connected. */
function Versions({ server }: { server: ServerView }) {
	return (
		<span className="type-mono text-ink-4 leading-snug">
			{server.agentVersion
				? `agent ${server.agentVersion}`
				: "agent not connected"}
			{server.facts?.dockerVersion
				? ` · docker ${server.facts.dockerVersion}`
				: ""}
			{server.facts ? ` · ${server.facts.arch}` : ""}
		</span>
	);
}

/**
 * One server in the list: status icon and word, versions, CPU / memory /
 * disk meters from the latest heartbeat, last seen and proxy state. A disk
 * over its alert threshold adds a warning line in words; a server that
 * stopped reporting is dimmed and says so.
 */
const ServerCardLink = memo(function ServerCardLink({
	server,
}: {
	server: ServerView;
}) {
	const { latest, settings } = server;
	const stale = !isOnline(server);
	const buildBadge = roleBadge(server);
	const disk = diskPercent(server);
	return (
		<Link
			to="/servers/$serverId"
			params={{ serverId: server.id }}
			{...vt("server", server.id)}
			className="surface-raised flex min-h-[220px] flex-1 flex-col gap-4 rounded-card p-[18px] text-ink-1 outline-none transition-[border-color] duration-quick ease-out hover:border-line-strong hover:text-ink-1 focus-visible:focus-ring"
		>
			<span className="flex items-start justify-between gap-3">
				<span className="flex min-w-0 items-start gap-3">
					<span
						aria-hidden
						className="surface-well grid size-9 shrink-0 place-items-center rounded-ctl text-ink-3"
					>
						<ServerIcon className="size-4" />
					</span>
					<span className="flex min-w-0 flex-col gap-1.5">
						<span
							title={server.name}
							className="truncate font-display font-semibold text-base text-ink-1 leading-[1.15] tracking-[-0.01em]"
						>
							{server.name}
						</span>
						<span className="type-mono truncate text-ink-3 leading-none">
							{server.publicIp ?? "no public IP"}
							{server.baseDomain ? ` · ${server.baseDomain}` : ""}
							{server.controlPlane ? " · control plane" : ""}
							{server.acceptWorkloads ? "" : " · no workloads"}
						</span>
					</span>
				</span>
				<span className="flex shrink-0 flex-col items-end gap-2 pr-7">
					<ServerStatus status={server.status} />
					<DrainChip state={server.drainState} message={server.drainMessage} />
					{buildBadge ? (
						<Badge
							variant="outline"
							title="Runs builds for projects that use build servers"
						>
							{buildBadge}
						</Badge>
					) : null}
				</span>
			</span>

			<span className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
				<Versions server={server} />
				<AgentSkew serverId={server.id} />
			</span>

			{latest ? (
				<span className="grid grid-cols-3 gap-4">
					<DitherCapacityStat
						label="cpu"
						renderValue={renderKpi}
						percent={latest.cpuPercent}
						warnAt={80}
						badAt={95}
						stale={stale}
					/>
					<DitherCapacityStat
						label="memory"
						renderValue={renderKpi}
						percent={percentOf(latest.memUsed, latest.memTotal)}
						detail={
							<span className="whitespace-nowrap">
								{usageText(latest.memUsed, latest.memTotal)}
							</span>
						}
						warnAt={85}
						stale={stale}
					/>
					<DitherCapacityStat
						label="disk"
						renderValue={renderKpi}
						percent={disk}
						detail={
							<span className="whitespace-nowrap">
								{usageText(latest.diskUsed, latest.diskTotal)}
							</span>
						}
						threshold={settings.diskAlertThresholdPercent}
						warnAt={settings.diskAlertThresholdPercent}
						stale={stale}
					/>
				</span>
			) : (
				<span className="text-ink-3 text-xs leading-snug">
					{server.status === "pending"
						? "Waiting for the agent. Open the server for its install command; usage appears once the agent connects."
						: "No heartbeat has been recorded yet."}
				</span>
			)}

			{stale && latest ? (
				<span className="text-ink-3 text-xs leading-snug">
					{server.status === "revoked"
						? "Revoked: these are the last figures it reported."
						: "Not reporting: these are the last figures it sent."}
				</span>
			) : null}

			{server.diskAlert ? (
				<span
					role="alert"
					data-tone="warning"
					className="tone-callout relative flex items-start gap-2 rounded-ctl px-2.5 py-2 text-warning-text text-xs leading-snug"
				>
					<ToneIcon tone="warning" className="mt-px" />
					<span className="min-w-0">
						<span className="font-medium">Disk alert.</span> Disk is over its{" "}
						{settings.diskAlertThresholdPercent}% threshold. Free space or prune
						images.
					</span>
				</span>
			) : null}

			{server.patches.known && server.patches.supported ? (
				<span className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs leading-snug">
					<span className="flex items-center gap-1.5 text-ink-2">
						<StatusIcon
							kind={updatesStatus(server.patches)}
							label={updatesText(server.patches)}
						/>
						{updatesText(server.patches)}
					</span>
					{server.patches.rebootRequired ? (
						<Badge
							tone="warn"
							lamp
							title="Installed updates only take effect after a reboot."
						>
							Reboot required
						</Badge>
					) : null}
				</span>
			) : null}

			{server.unrestricted ? (
				<span
					role="note"
					data-tone="warning"
					className="tone-callout relative flex items-start gap-2 rounded-ctl px-2.5 py-2 text-warning-text text-xs leading-snug"
				>
					<ToneIcon tone="warning" className="mt-px" />
					<span className="min-w-0">
						<span className="font-medium">Unrestricted.</span> Stacks and
						services here can use privileged mode, host networking, devices and
						host mounts.
					</span>
				</span>
			) : null}

			<span
				className={cn(
					"mt-auto flex items-center justify-between gap-3 border-line border-t pt-3",
					stale && "text-ink-3",
				)}
			>
				<ProxyLine proxy={server.proxy} />
				<span className="type-mono flex shrink-0 items-center gap-3 text-ink-4 leading-none">
					<span>
						{server.serviceCount}{" "}
						{server.serviceCount === 1 ? "service" : "services"}
					</span>
					<span>
						{server.lastSeenAt ? (
							<>
								seen <Relative iso={server.lastSeenAt} />
							</>
						) : (
							"never seen"
						)}
					</span>
				</span>
			</span>
		</Link>
	);
});

/**
 * One server in the list: the card that opens it, and the `⋯` menu laid over
 * its corner. The menu is a sibling of the link, not inside it, so no button
 * nests in an anchor.
 */
export const ServerCard = memo(function ServerCard({
	server,
}: {
	server: ServerView;
}) {
	return (
		<div className="group/card relative flex min-w-0 flex-col">
			<ServerCardLink server={server} />
			<ServerCardMenu server={server} />
		</div>
	);
});
