import type { Environment, Service } from "#/api/models";
import type { LampTone } from "#/components/ui/lamp";
import { nodeStatus, type StatusGroup } from "#/features/canvas/lib/status";

export interface EnvironmentSummary {
	id: string;
	slug: string;
	name: string;
	isProduction: boolean;
	serviceCount: number;
	/** The lamp of the environment's worst service; unlit with no services. */
	tone: LampTone;
	pulse: boolean;
	/** The words beside the lamp: "Live", "1 failed", "No services". */
	label: string;
}

// Worst first: a failed service decides the environment's lamp.
const GROUP_ORDER: readonly StatusGroup[] = [
	"failed",
	"waiting",
	"in_flight",
	"rolled_back",
	"deployed",
	"stopped",
	"not_deployed",
];

const GROUP_TONE: Record<StatusGroup, { tone: LampTone; pulse: boolean }> = {
	failed: { tone: "failed", pulse: false },
	waiting: { tone: "staged", pulse: true },
	in_flight: { tone: "deploying", pulse: true },
	rolled_back: { tone: "degraded", pulse: false },
	deployed: { tone: "live", pulse: false },
	stopped: { tone: "stopped", pulse: false },
	not_deployed: { tone: "off", pulse: false },
};

function labelFor(group: StatusGroup, count: number, total: number): string {
	switch (group) {
		case "failed":
			return `${count} failed`;
		case "waiting":
			return `${count} waiting`;
		case "in_flight":
			return "Deploying";
		case "rolled_back":
			return `${count} rolled back`;
		case "deployed":
			return total === count ? "Live" : `${count} of ${total} live`;
		case "stopped":
			return `${count} stopped`;
		case "not_deployed":
			return "Not deployed";
	}
}

/** One environment of a project, reduced to a lamp and a sentence. */
export function summarizeEnvironment(
	environment: Pick<Environment, "id" | "slug" | "name" | "isProduction">,
	services: readonly Service[],
): EnvironmentSummary {
	const base = {
		id: environment.id,
		slug: environment.slug,
		name: environment.name,
		isProduction: environment.isProduction,
		serviceCount: services.length,
	};
	if (services.length === 0) {
		return { ...base, tone: "off", pulse: false, label: "No services" };
	}
	const counts = new Map<StatusGroup, number>();
	for (const service of services) {
		const group = nodeStatus(service).group;
		counts.set(group, (counts.get(group) ?? 0) + 1);
	}
	const group = GROUP_ORDER.find((candidate) => counts.has(candidate));
	if (!group) {
		return { ...base, tone: "off", pulse: false, label: "No services" };
	}
	const { tone, pulse } = GROUP_TONE[group];
	return {
		...base,
		tone,
		pulse,
		label: labelFor(group, counts.get(group) ?? 0, services.length),
	};
}

/** The newest settle (or start) time of any service's latest deployment. */
export function lastDeployAt(services: readonly Service[]): string | null {
	let newest: string | null = null;
	for (const service of services) {
		const latest = service.latestDeployment;
		if (!latest) continue;
		const at = latest.finishedAt ?? latest.createdAt;
		if (newest === null || at > newest) newest = at;
	}
	return newest;
}

/** "service" or "services", for the label under a count. */
export function serviceNoun(count: number): string {
	return count === 1 ? "service" : "services";
}
