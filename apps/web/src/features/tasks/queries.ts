import type { QueryClient } from "@tanstack/react-query";
import { errorCode, errorMessage } from "#/api/client-config";
import { invalidateOperations } from "#/api/invalidate";

/** Everything the tasks UI shows; refetched after any task mutation. */
export const TASK_OPERATIONS = [
	"listServiceTasks",
	"listServiceTaskRuns",
	"getTaskRun",
	"getTaskRunLogs",
] as const;

export function invalidateTasks(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, TASK_OPERATIONS);
}

/** Why a run could not start, in words: the two 409s the user can act on get their own sentence. */
export function startErrorMessage(error: unknown): string {
	switch (errorCode(error)) {
		case "not_deployed":
			return "Deploy the service first: a command runs in the image of its current release.";
		case "server_offline":
			return "The service's server is offline, so nothing can run on it right now.";
		default:
			return errorMessage(error);
	}
}
