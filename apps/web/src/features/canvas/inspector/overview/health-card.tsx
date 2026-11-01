import type { Service } from "#/api/models";
import { KpiNumber } from "#/components/common/kpi-number";
import { StatusIcon } from "#/components/ui/status-icon";
import { orderedReplicas, replicaLabel } from "#/features/processes/replicas";
import {
	formatUptime,
	healthVerdict,
	replicaCounts,
	replicaKind,
	restartTotal,
	uptimeMs,
} from "./model";
import { useNow } from "./use-now";

function Figure({
	label,
	value,
	kpi = false,
}: {
	label: string;
	value: string;
	/** Resolve the digits from dither the first time and pop later changes. */
	kpi?: boolean;
}) {
	return (
		<div className="flex min-w-0 flex-col gap-1.5">
			<dt className="type-micro">{label}</dt>
			<dd className="type-metric truncate text-ink-1">
				{kpi ? <KpiNumber text={value} /> : value}
			</dd>
		</div>
	);
}

/**
 * Health: the verdict with the probe it rests on, then replicas ready of
 * desired, uptime from the container's start and the restarts replicas have
 * taken. A per-replica row appears once there is more than one.
 */
export function HealthCard({ service }: { service: Service }) {
	const now = useNow();
	const verdict = healthVerdict(service);
	const { ready, desired } = replicaCounts(service);
	const uptime = uptimeMs(service, now);
	const replicas = orderedReplicas(service);
	const running = service.observed !== null;

	return (
		<div className="rule-rows">
			<div className="flex min-w-0 items-center gap-2.5 py-2">
				<StatusIcon kind={verdict.kind} label="" className="size-4" />
				<span className="font-semibold text-ink-1 text-sm leading-none">
					{verdict.label}
				</span>
				<span
					title={verdict.detail}
					className="min-w-0 truncate font-mono text-2xs text-ink-3"
				>
					{verdict.detail}
				</span>
			</div>
			<dl className="grid grid-cols-3 gap-3 py-3">
				<Figure
					kpi
					label="Replicas"
					value={running || desired > 0 ? `${ready} / ${desired}` : "—"}
				/>
				<Figure
					label="Uptime"
					value={uptime === null ? "—" : formatUptime(uptime)}
				/>
				<Figure
					kpi
					label="Restarts"
					value={running ? String(restartTotal(service)) : "—"}
				/>
			</dl>
			{replicas.length > 1 ? (
				<ul aria-label="Replicas" className="flex flex-wrap gap-1.5 py-3">
					{replicas.map((replica) => (
						<li
							key={`${replica.process}:${replica.index}`}
							className="surface-well flex items-center gap-1.5 rounded-full py-1 pr-2.5 pl-2 font-mono text-2xs text-ink-2 leading-none"
						>
							<StatusIcon kind={replicaKind(replica)} label="" />
							{replicaLabel(replica)} · {replica.state}
							{replica.state === "running" && replica.health
								? `, ${replica.health}`
								: ""}
						</li>
					))}
				</ul>
			) : null}
			{service.stale ? (
				<p className="py-2 text-2xs text-ink-3">
					The agent's last report is more than 30 seconds old.
				</p>
			) : null}
		</div>
	);
}
