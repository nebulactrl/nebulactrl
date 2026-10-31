/**
 * What deleting a project removes, in the words the delete dialog shows.
 * Counts come from `GET /projects/{id}/deletion-plan`; this file only turns
 * them into lines. A count of zero has no line.
 */

export interface DeletionPlan {
	/** The server sends null for none. */
	environments: readonly { name: string; production: boolean }[] | null;
	services: number;
	databases: number;
	volumes: number;
	domains: number;
	previews: number;
	statusPageComponents: number;
	managedDnsRecords: number;
}

/** "1 service", "2 services": the noun follows the count. */
export function count(n: number, one: string, many = `${one}s`): string {
	return `${n} ${n === 1 ? one : many}`;
}

function environmentLine(plan: DeletionPlan): string | undefined {
	const names = (plan.environments ?? []).map(
		(environment) => environment.name,
	);
	if (names.length === 0) return undefined;
	return `${count(names.length, "environment")} (${names.join(", ")})`;
}

function workloadLine(plan: DeletionPlan): string | undefined {
	const parts: string[] = [];
	if (plan.services > 0) parts.push(count(plan.services, "service"));
	if (plan.databases > 0) parts.push(count(plan.databases, "database"));
	return parts.length > 0 ? parts.join(", ") : undefined;
}

function domainLine(plan: DeletionPlan): string | undefined {
	if (plan.domains > 0 && plan.managedDnsRecords > 0) {
		return `${count(plan.domains, "domain")} and their Cloudflare records`;
	}
	if (plan.domains > 0) return count(plan.domains, "domain");
	if (plan.managedDnsRecords > 0) {
		return count(plan.managedDnsRecords, "Cloudflare record");
	}
	return undefined;
}

/**
 * The lines of the delete dialog's list, in the order a reader thinks of a
 * project: environments, what runs in them, their data, their addresses,
 * their previews, and the status page entries that point at them.
 */
export function describeDeletionPlan(plan: DeletionPlan): string[] {
	const lines: (string | undefined)[] = [
		environmentLine(plan),
		workloadLine(plan),
		plan.volumes > 0
			? `${count(plan.volumes, "volume")} and ${plan.volumes === 1 ? "its" : "their"} data`
			: undefined,
		domainLine(plan),
		plan.previews > 0 ? count(plan.previews, "preview") : undefined,
		plan.statusPageComponents > 0
			? `${count(plan.statusPageComponents, "status page component")}, removed from ${plan.statusPageComponents === 1 ? "its page" : "their pages"}`
			: undefined,
	];
	return lines.filter((line): line is string => line !== undefined);
}
