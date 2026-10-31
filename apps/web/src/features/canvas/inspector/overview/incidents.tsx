import { useQuery } from "@tanstack/react-query";
import { listServiceIncidentsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { IncidentView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { Relative } from "#/components/common/relative";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { OverviewSection } from "./section";

/** What an incident kind is called. */
const KIND_LABEL: Record<string, string> = {
	crashed: "Crashed",
	unhealthy: "Unhealthy",
	restart_loop: "Restart loop",
};

/** Open incidents are red or amber; a closed one is quiet. */
export function incidentKind(
	i: Pick<IncidentView, "kind" | "resolvedAt">,
): StatusKind {
	if (i.resolvedAt) return "stopped";
	return i.kind === "unhealthy" ? "warning" : "failed";
}

/** "Crashed", "Crashed · recovered", "Restart loop · paused (flapping)". */
export function incidentLine(i: IncidentView): string {
	const kind = KIND_LABEL[i.kind] ?? i.kind;
	if (!i.resolvedAt) return i.flapping ? `${kind} · alerts paused` : kind;
	return i.resolution === "stopped"
		? `${kind} · service stopped`
		: `${kind} · recovered`;
}

/**
 * The service's recent health incidents: a crash, a failing health check or a
 * restart loop, newest first, with when it started and how it ended. Shown only
 * once the service has had one, so a healthy service's Overview stays as it was.
 */
export function Incidents({ service }: { service: Service }) {
	const incidents = useQuery(
		listServiceIncidentsOptions({
			path: { serviceId: service.id },
			query: { limit: 5 },
		}),
	);
	const items = incidents.data?.items ?? [];
	if (items.length === 0) return null;
	return (
		<OverviewSection id="overview-incidents" title="Health incidents">
			<ol className="rule-rows">
				{items.map((incident) => (
					<li
						key={incident.id}
						className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3"
					>
						<StatusIcon kind={incidentKind(incident)} label="" />
						<div className="flex min-w-0 flex-col gap-1.5">
							<span className="truncate font-medium text-ink-1 text-xs leading-none">
								{incidentLine(incident)}
							</span>
							<span
								title={incident.detail}
								className="truncate text-[10.5px] text-ink-3 leading-none"
							>
								{incident.detail}
							</span>
						</div>
						<div className="text-[10.5px] text-ink-3 leading-none">
							<Relative iso={incident.openedAt} />
						</div>
					</li>
				))}
			</ol>
		</OverviewSection>
	);
}
