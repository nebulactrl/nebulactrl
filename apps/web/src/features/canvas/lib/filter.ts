import type { Service, ServiceKind } from "#/api/models";
import { nodeStatus, type StatusGroup } from "./status";

/** The sentinel server id for services not yet placed on a server. */
export const NO_SERVER = "none";

export interface CanvasFilter {
	statuses: ReadonlySet<StatusGroup>;
	serverIds: ReadonlySet<string>;
	kinds: ReadonlySet<ServiceKind>;
	/** Free text from the ⌘F search field. */
	query: string;
}

export const EMPTY_FILTER: CanvasFilter = {
	statuses: new Set(),
	serverIds: new Set(),
	kinds: new Set(),
	query: "",
};

export function isFilterEmpty(filter: CanvasFilter): boolean {
	return (
		filter.statuses.size === 0 &&
		filter.serverIds.size === 0 &&
		filter.kinds.size === 0 &&
		filter.query.trim() === ""
	);
}

function haystack(service: Service): string {
	return [
		service.name,
		service.slug,
		service.kind,
		service.server?.name ?? "",
		service.config.image ?? "",
		service.config.gitUrl ?? "",
		...service.domains.map((d) => d.hostname),
	]
		.join("\n")
		.toLowerCase();
}

/**
 * Whether a service passes the filter. Within one chip group the choices are
 * alternatives (any of them); across groups, and with the search text, all
 * must hold. Search terms are space-separated and all must appear.
 */
export function matchesFilter(service: Service, filter: CanvasFilter): boolean {
	if (filter.kinds.size > 0 && !filter.kinds.has(service.kind)) return false;
	if (
		filter.serverIds.size > 0 &&
		!filter.serverIds.has(service.serverId ?? NO_SERVER)
	) {
		return false;
	}
	if (
		filter.statuses.size > 0 &&
		!filter.statuses.has(nodeStatus(service).group)
	) {
		return false;
	}
	const terms = filter.query.toLowerCase().split(/\s+/).filter(Boolean);
	if (terms.length > 0) {
		const text = haystack(service);
		if (!terms.every((term) => text.includes(term))) return false;
	}
	return true;
}

/** The ids passing the filter, or null when nothing is filtered (all match). */
export function matchingIds(
	services: readonly Service[],
	filter: CanvasFilter,
): Set<string> | null {
	if (isFilterEmpty(filter)) return null;
	const ids = new Set<string>();
	for (const service of services) {
		if (matchesFilter(service, filter)) ids.add(service.id);
	}
	return ids;
}

/**
 * What dims the cards that do not match. A filter nothing passes dims
 * nothing: the canvas says "No matches" once, and the cards stay readable
 * instead of all going grey.
 */
export function dimmingIds(
	ids: ReadonlySet<string> | null,
): ReadonlySet<string> | null {
	return ids !== null && ids.size === 0 ? null : ids;
}

/** The one count the search field or the chip bar prints for a filter. */
export function matchSummary(matching: number, total: number): string {
	return matching === 0 ? "No matches" : `${matching} of ${total}`;
}
