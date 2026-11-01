import type { QueryClient } from "@tanstack/react-query";

/**
 * Marks every generated query whose operation id is in `ids` stale and
 * refetches the ones on screen. Resolves once the refetch has finished, so a
 * mutation can wait for fresh data (a PATCH's next `If-Match` is the new
 * generation) before it reports done.
 */
export function invalidateOperations(
	queryClient: QueryClient,
	ids: readonly string[],
): Promise<void> {
	const wanted = new Set(ids);
	return queryClient.invalidateQueries({
		predicate: (query) => {
			const head = query.queryKey[0];
			return (
				typeof head === "object" &&
				head !== null &&
				"_id" in head &&
				wanted.has(String(head._id))
			);
		},
	});
}

/** Everything that shows a service: cards, inspector, staged changes. */
export const SERVICE_OPERATIONS = [
	"listServices",
	"getService",
	"listPendingChanges",
	// Editing a service in the dashboard is what turns an owned field into
	// "edited here", and applying nebula.toml is what clears it.
	"getServiceConfigSource",
	"getEnvironmentConfigSource",
	"listDeployments",
	"listDomains",
	"listVariables",
	"getVariableHistory",
	"listSuggestedVariables",
	"listProjectSharedVariables",
	"listEnvironmentSharedVariables",
	"listVolumes",
	"listServers",
	"getServiceSshKey",
	// A saved reference draws (or removes) a cable, so a variable edit that
	// refreshes the service refreshes the links with it.
	"listLinks",
	// Scaling one process and the combine saga change what a card, a scale
	// dialog and the services of the environment show.
	"setProcessScaling",
	"combineServices",
	"abortCombine",
	"retryCombine",
] as const;

export function invalidateServices(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, SERVICE_OPERATIONS);
}
