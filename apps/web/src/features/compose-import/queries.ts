import { queryOptions } from "@tanstack/react-query";
import { planComposeImport } from "#/api/gen/sdk.gen";
import type { ComposePlanRequest } from "#/api/gen/types.gen";

/**
 * The plan is a POST that writes nothing, so it is cached like a query. The
 * request body is the key: a changed choice is a new key, the previous
 * request is aborted through `signal`, and TanStack never lets a stale answer
 * replace a newer one.
 */
export const composePlanQuery = (
	environmentId: string,
	request: ComposePlanRequest,
) =>
	queryOptions({
		queryKey: ["composePlan", environmentId, request],
		queryFn: async ({ signal }) => {
			const { data } = await planComposeImport({
				path: { environmentId },
				body: request,
				signal,
			});
			return data;
		},
		retry: false,
		staleTime: 0,
		gcTime: 30_000,
		refetchOnWindowFocus: false,
	});
