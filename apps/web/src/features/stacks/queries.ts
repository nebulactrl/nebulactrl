import {
	keepPreviousData,
	queryOptions,
	useQuery,
} from "@tanstack/react-query";
import { useMemo } from "react";
import { errorMessage } from "#/api/client-config";
import { getServerUnrestrictedOptions } from "#/api/gen/@tanstack/react-query.gen";
import { checkStack } from "#/api/gen/sdk.gen";
import { useDebounced } from "#/features/templates/use-debounced";
import type { StackCheckResult, StackCheckState } from "./model";

/** How long the compose text rests before it is sent for a check. */
const CHECK_DEBOUNCE_MS = 400;

/**
 * The check is a POST that writes nothing, so it is cached like a query. The
 * text and the server are the key, an older request is aborted through
 * `signal`, and it never polls: the answer only changes with the text.
 */
const stackCheckQuery = (
	environmentId: string,
	compose: string,
	serverId: string,
) =>
	queryOptions({
		queryKey: ["stackCheck", environmentId, serverId, compose],
		queryFn: async ({ signal }): Promise<StackCheckResult> => {
			const { data } = await checkStack({
				path: { environmentId },
				body: { compose, ...(serverId === "" ? {} : { serverId }) },
				signal,
			});
			return {
				components: data.components ?? [],
				violations: data.violations ?? [],
				notes: data.notes ?? [],
				unrestricted: data.unrestricted,
				composeAvailable: data.composeAvailable,
			};
		},
		retry: false,
		staleTime: 30_000,
		gcTime: 60_000,
		refetchInterval: false,
		refetchOnWindowFocus: false,
		placeholderData: keepPreviousData,
	});

/**
 * The live check of a compose text: debounced, with the last answer kept on
 * screen while the next is on its way. `serverId` ("" for none) decides
 * whether the server's Unrestricted switch applies. Nothing is sent while the text is
 * empty or `enabled` is false.
 */
export function useStackCheck({
	environmentId,
	compose,
	serverId,
	enabled = true,
}: {
	environmentId: string;
	compose: string;
	serverId: string;
	enabled?: boolean;
}): StackCheckState {
	const settled = useDebounced(compose, CHECK_DEBOUNCE_MS);
	const empty = compose.trim() === "";
	const query = useQuery({
		...stackCheckQuery(environmentId, settled, serverId),
		enabled: enabled && settled.trim() !== "",
	});
	const current = settled === compose;
	return useMemo(
		() => ({
			empty,
			pending:
				enabled &&
				!empty &&
				(!current || query.isFetching || query.isPlaceholderData),
			result: empty ? null : (query.data ?? null),
			error: query.isError && current ? errorMessage(query.error) : null,
		}),
		[
			empty,
			enabled,
			current,
			query.isFetching,
			query.isPlaceholderData,
			query.data,
			query.isError,
			query.error,
		],
	);
}

/** `GET /servers/{id}/unrestricted`: the Unrestricted switch, the Compose version and availability, and what depends on the switch. */
export const serverUnrestrictedQuery = (serverId: string) =>
	getServerUnrestrictedOptions({ path: { serverId } });
