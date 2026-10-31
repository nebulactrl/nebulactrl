import type { QueryClient } from "@tanstack/react-query";
import { listNetworksOptions } from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations } from "#/api/invalidate";

/** Everything a network change can touch: the lists, the services that attach to it, their staged changes. */
export const NETWORK_OPERATIONS = [
	"listNetworks",
	"getNetwork",
	"listServices",
	"getService",
	"listPendingChanges",
] as const;

export function invalidateNetworks(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, NETWORK_OPERATIONS);
}

/** The team's shared networks, optionally only those on one server. */
export const networksQuery = (serverId?: string) =>
	listNetworksOptions(serverId ? { query: { serverId } } : {});
