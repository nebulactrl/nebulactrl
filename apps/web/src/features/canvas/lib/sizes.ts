import type { Service } from "#/api/models";
import { hasProcesses, processCardSize } from "#/features/processes/model";
import { nodeSummary, stackNodeSize } from "#/features/stacks/model";
import { internetSummary } from "./internet";
import {
	defaultSize,
	type LayoutTraits,
	NODE_HEIGHT,
	type Size,
	type SizeOf,
} from "./layout";

/** What each volume past the first adds to a card: a 12px row and its 4px gap (the first fits `NODE_HEIGHT`'s slack). */
const VOLUME_ROW = 16;

/**
 * The real size of every node on the canvas: a service card is the fixed
 * card size plus a row for every volume past the first, a Compose stack or a
 * service with processes is as wide and as tall as its rows make it. Layout,
 * arrange, group frames and cable sides all ask this, so a node never
 * overlaps what sits beside it.
 */
export function sizesOf(services: readonly Service[]): SizeOf {
	const sizes = new Map<string, Size>();
	for (const service of services) {
		if (hasProcesses(service)) {
			sizes.set(service.id, processCardSize(service));
		} else if (service.kind === "stack") {
			sizes.set(service.id, stackNodeSize(nodeSummary(service)));
		} else if (service.volumes.length > 1) {
			sizes.set(service.id, {
				...defaultSize(service.id),
				h: NODE_HEIGHT + (service.volumes.length - 1) * VOLUME_ROW,
			});
		}
	}
	if (sizes.size === 0) return defaultSize;
	return (serviceId) => sizes.get(serviceId) ?? defaultSize(serviceId);
}

/**
 * What Arrange reads off the services: their list order, the ones the
 * Internet reaches (in domain order) and the databases.
 */
export function layoutTraits(
	services: readonly Pick<Service, "id" | "kind" | "domains">[],
): LayoutTraits {
	return {
		order: services.map((service) => service.id),
		entryIds: internetSummary(services)?.serviceIds ?? [],
		dataIds: new Set(
			services.filter((s) => s.kind === "database").map((s) => s.id),
		),
	};
}
