import {
	keepPreviousData,
	queryOptions,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useRef } from "react";
import {
	abortCombine,
	combineServices,
	getCombine,
	planCombineServices,
	retryCombine,
} from "#/api/gen/sdk.gen";
import type {
	CombinePlan,
	CombineRequest,
	CombineView,
} from "#/api/gen/types.gen";
import { invalidateServices } from "#/api/invalidate";
import { COMBINE_POLL_MS, isSettled } from "./model";

/**
 * The plan is a POST that writes nothing, so it is cached like a query: the
 * members and the target are the key, an older request is aborted through
 * `signal`, and nothing keeps it: a plan is only as good as the moment it was
 * made (its fingerprint says which state of the services it describes).
 */
export const combinePlanQuery = (
	environmentId: string,
	serviceIds: readonly string[],
	targetServiceId: string,
) =>
	queryOptions({
		queryKey: [
			"combinePlan",
			environmentId,
			[...serviceIds].sort(),
			targetServiceId,
		],
		queryFn: async ({ signal }): Promise<CombinePlan> => {
			const { data } = await planCombineServices({
				path: { environmentId },
				body: {
					serviceIds: [...serviceIds],
					...(targetServiceId === "" ? {} : { targetServiceId }),
				},
				signal,
			});
			return data;
		},
		retry: false,
		staleTime: 0,
		gcTime: 0,
		refetchInterval: false,
		refetchOnWindowFocus: false,
		// The last plan stays on screen while the next one (another target) loads.
		placeholderData: keepPreviousData,
	});

/** The plan for the selected services; `targetServiceId` "" lets the control plane pick. */
export function useCombinePlan({
	environmentId,
	serviceIds,
	targetServiceId,
	enabled = true,
}: {
	environmentId: string;
	serviceIds: readonly string[];
	targetServiceId: string;
	enabled?: boolean;
}) {
	return useQuery({
		...combinePlanQuery(environmentId, serviceIds, targetServiceId),
		enabled,
	});
}

export const combineQuery = (combineId: string) =>
	queryOptions({
		queryKey: ["combine", combineId],
		queryFn: async ({ signal }): Promise<CombineView> => {
			const { data } = await getCombine({ path: { combineId }, signal });
			return data;
		},
		retry: false,
		staleTime: 0,
	});

/** A combine that is moving, asked again every `COMBINE_POLL_MS` until it settles. */
export function useCombineProgress(combineId: string | undefined) {
	return useQuery({
		...combineQuery(combineId ?? ""),
		enabled: combineId !== undefined,
		refetchInterval: (query) => {
			const view = query.state.data;
			return view && isSettled(view) ? false : COMBINE_POLL_MS;
		},
	});
}

/**
 * Start, retry and abort. Each answer is written to the progress query, and
 * the services refresh, since a combine changes the target's staged config
 * and (once done) deletes the others.
 */
export function useCombineActions(environmentId: string) {
	const queryClient = useQueryClient();
	// One key per distinct request: a retry after a lost response replays it,
	// a changed plan or choice is a new request.
	const attempt = useRef({ signature: "", key: "" });
	const keyFor = (body: CombineRequest) => {
		const signature = JSON.stringify(body);
		if (attempt.current.signature !== signature) {
			attempt.current = { signature, key: crypto.randomUUID() };
		}
		return attempt.current.key;
	};
	const settle = async (view: CombineView) => {
		queryClient.setQueryData(combineQuery(view.id).queryKey, view);
		await invalidateServices(queryClient);
	};

	const start = useMutation({
		mutationFn: async (body: CombineRequest): Promise<CombineView> => {
			const { data } = await combineServices({
				path: { environmentId },
				body,
				headers: { "Idempotency-Key": keyFor(body) },
			});
			return data;
		},
		onSuccess: settle,
	});
	const retry = useMutation({
		mutationFn: async (combineId: string): Promise<CombineView> => {
			const { data } = await retryCombine({
				path: { combineId },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return data;
		},
		onSuccess: settle,
	});
	const abort = useMutation({
		mutationFn: async (combineId: string): Promise<CombineView> => {
			const { data } = await abortCombine({
				path: { combineId },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return data;
		},
		onSuccess: settle,
	});
	return { start, retry, abort };
}
