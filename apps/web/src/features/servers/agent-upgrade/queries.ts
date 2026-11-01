import { type QueryClient, queryOptions } from "@tanstack/react-query";
import { getInstanceUpgradeOptions } from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations } from "#/api/invalidate";
import { anyRunning } from "./model";

/** Refetch cadence while an agent upgrade runs (the event feed is the primary signal). */
const RUNNING_POLL_MS = 4000;

/** The control plane's version, the agent skew of every server and the auto-upgrade setting. */
export const instanceUpgradeQuery = () =>
	queryOptions({
		...getInstanceUpgradeOptions(),
		staleTime: 15_000,
		refetchInterval: (query) =>
			anyRunning(query.state.data) ? RUNNING_POLL_MS : false,
	});

const AGENT_UPGRADE_OPERATIONS = ["getInstanceUpgrade"] as const;

export function invalidateAgentUpgrade(
	queryClient: QueryClient,
): Promise<void> {
	return invalidateOperations(queryClient, AGENT_UPGRADE_OPERATIONS);
}
