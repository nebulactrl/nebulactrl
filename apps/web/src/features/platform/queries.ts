import { type QueryClient, queryOptions } from "@tanstack/react-query";
import {
	getPlatformServiceOptions,
	listPlatformServicesOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations } from "#/api/invalidate";
import type { Server } from "#/api/models";
import { isSchedulable } from "#/features/fleet/model";
import { anyBusy, isBusy, pollInterval } from "./model";

/**
 * The catalog and what is set up. The event feed is the primary signal; the
 * list also polls (every 3 s while something provisions, else every 15 s) so a
 * missed event cannot leave a card stuck.
 */
export const platformServicesQuery = () =>
	queryOptions({
		...listPlatformServicesOptions(),
		refetchInterval: (query) => pollInterval(anyBusy(query.state.data)),
	});

/** One service, followed by the set-up progress view until it is ready or failed. */
export const platformServiceQuery = (id: string) =>
	queryOptions({
		...getPlatformServiceOptions({ path: { id } }),
		refetchInterval: (query) => pollInterval(isBusy(query.state.data)),
	});

/** What the platform UI shows; refetched after any platform mutation. */
export const PLATFORM_OPERATIONS = [
	"listPlatformServices",
	"getPlatformService",
] as const;

/**
 * Everything the team's Headscale feeds: its own views plus the Tailscale
 * integration it is wired into.
 */
export const PLATFORM_AFFECTED_OPERATIONS = [
	...PLATFORM_OPERATIONS,
	"getTailscaleIntegration",
] as const;

export function invalidatePlatform(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, PLATFORM_AFFECTED_OPERATIONS);
}

/** Servers that are online and take new services. */
export function usableServers(servers: readonly Server[]): Server[] {
	return servers.filter(
		(server) => server.status === "online" && isSchedulable(server),
	);
}
