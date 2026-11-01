import { queryOptions } from "@tanstack/react-query";
import {
	getFleetOptions,
	getServerDrainOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type { DrainView, FleetServer, Overview } from "#/api/gen/types.gen";

export interface FleetData {
	servers: FleetServer[];
	totals: Overview["totals"];
}

// Same generated keys as the SDK's own options, so the SSE feed's
// `server.updated` / `deployment.updated` invalidation (operation ids
// `getFleet`, `getServerDrain`) reaches these.

export const fleetQuery = () =>
	queryOptions({
		...getFleetOptions(),
		refetchInterval: 15_000,
		select: (overview): FleetData => ({
			servers: overview.servers ?? [],
			totals: overview.totals,
		}),
	});

/** The running drain of a server, else its newest finished one; null if never drained. */
export const serverDrainQuery = (serverId: string, running: boolean) =>
	queryOptions({
		...getServerDrainOptions({ path: { serverId } }),
		// The SSE feed pushes changes; polling is the fallback, quicker while a drain runs.
		refetchInterval: running ? 3_000 : 15_000,
		select: (body): DrainView | null => body.drain ?? null,
	});
