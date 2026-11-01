import type { QueryClient } from "@tanstack/react-query";
import { invalidateOperations } from "#/api/invalidate";

/** Everything the git UI shows; refetched after any connection or link mutation. */
export const GIT_OPERATIONS = [
	"listGitConnections",
	"getGitConnection",
	"listGitDeliveries",
	"getServiceGit",
] as const;

export function invalidateGit(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, GIT_OPERATIONS);
}
