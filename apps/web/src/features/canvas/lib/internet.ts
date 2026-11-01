import type { CanvasLayout, Point, Service } from "#/api/models";
import { NODE_WIDTH } from "./layout";

/** The derived entry node's id: never stored, never a service. */
export const INTERNET_ID = "internet:public";
export const INTERNET_WIDTH = 320;
export const INTERNET_HEIGHT = 76;
/** Free space between the entry node and the cards under it. */
const INTERNET_GAP = 120;

export interface InternetSummary {
	/** Every public hostname of the environment, in service order. */
	domains: string[];
	/** The services that have at least one domain: the cables run to these. */
	serviceIds: string[];
}

/**
 * What the environment exposes to the internet, derived from the services'
 * domains. Null when nothing is exposed, so no entry node is drawn.
 */
export function internetSummary(
	services: readonly Pick<Service, "id" | "domains">[],
): InternetSummary | null {
	const domains: string[] = [];
	const serviceIds: string[] = [];
	for (const service of services) {
		if (service.domains.length === 0) continue;
		serviceIds.push(service.id);
		for (const domain of service.domains) domains.push(domain.hostname);
	}
	return serviceIds.length === 0 ? null : { domains, serviceIds };
}

/**
 * Where the entry node sits: centred above the services it feeds and clear
 * of the top of everything on the canvas (cards and group frames), so no
 * frame ever holds it and no cable to a service runs behind another card.
 */
export function internetPosition(
	layout: Pick<CanvasLayout, "nodes"> & Partial<Pick<CanvasLayout, "groups">>,
	serviceIds: readonly string[],
): Point {
	let minX = Number.POSITIVE_INFINITY;
	let maxX = Number.NEGATIVE_INFINITY;
	let minY = Number.POSITIVE_INFINITY;
	for (const id of serviceIds) {
		const at = layout.nodes[id];
		if (!at) continue;
		minX = Math.min(minX, at.x);
		maxX = Math.max(maxX, at.x + NODE_WIDTH);
	}
	if (!Number.isFinite(minX)) {
		return { x: 0, y: -(INTERNET_HEIGHT + INTERNET_GAP) };
	}
	for (const at of Object.values(layout.nodes)) minY = Math.min(minY, at.y);
	for (const group of layout.groups ?? []) minY = Math.min(minY, group.y);
	return {
		x: Math.round((minX + maxX) / 2 - INTERNET_WIDTH / 2),
		y: Math.round(minY - INTERNET_HEIGHT - INTERNET_GAP),
	};
}
