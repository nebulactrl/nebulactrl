import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { adoptServer, planServerAdoption } from "#/api/gen/sdk.gen";
import type {
	AdoptionApplyRequest,
	AdoptionPlan,
	AdoptionPlanRequest,
	AdoptionResult,
} from "#/api/gen/types.gen";
import { invalidateOperations, SERVICE_OPERATIONS } from "#/api/invalidate";
import { toast } from "#/components/ui/toast";
import { resultText } from "./lib";

// Both requests run as mutations, never as queries: a query's key holds its
// request and is cached and listed by the devtools, while a mutation's
// variables stay in memory, and `gcTime: 0` lets go of them as soon as nothing
// observes them. The plan reads the host's variables, so nothing about it is
// kept.

/**
 * The plan request. Every call starts a new request and aborts the one before
 * it; the mutation follows the latest call, so an answer to an older request
 * never replaces a newer one (per-call callbacks of an older call never run).
 */
export function useAdoptionPlan(serverId: string) {
	const controller = useRef<AbortController | null>(null);
	useEffect(() => () => controller.current?.abort(), []);
	return useMutation<AdoptionPlan, unknown, AdoptionPlanRequest>({
		gcTime: 0,
		mutationFn: async (request) => {
			controller.current?.abort();
			const own = new AbortController();
			controller.current = own;
			const { data } = await planServerAdoption({
				path: { serverId },
				body: request,
				signal: own.signal,
			});
			return data;
		},
	});
}

/**
 * One key per distinct request: a retry after a lost response replays the
 * adoption, while a changed choice is a new request. The fingerprint stays in
 * this ref, in memory.
 */
function useIdempotencyKey() {
	const last = useRef({ fingerprint: "", key: "" });
	return (request: AdoptionApplyRequest): string => {
		const fingerprint = JSON.stringify(request);
		if (last.current.fingerprint !== fingerprint) {
			last.current = { fingerprint, key: crypto.randomUUID() };
		}
		return last.current.key;
	};
}

/**
 * Adopts the reviewed workloads. On success the server, project and service
 * lists refetch and a toast says what was done, in the action's own words;
 * the warnings stay on the result step.
 */
export function useAdoptionApply(serverId: string) {
	const queryClient = useQueryClient();
	const keyFor = useIdempotencyKey();
	return useMutation<AdoptionResult, unknown, AdoptionApplyRequest>({
		gcTime: 0,
		mutationFn: async (request) => {
			const { data } = await adoptServer({
				path: { serverId },
				body: request,
				headers: { "Idempotency-Key": keyFor(request) },
			});
			return data;
		},
		onSuccess: async (result) => {
			toast.success(resultText(result));
			await invalidateOperations(queryClient, [
				...SERVICE_OPERATIONS,
				"getServer",
				"listProjects",
				"listEnvironments",
			]);
		},
	});
}
