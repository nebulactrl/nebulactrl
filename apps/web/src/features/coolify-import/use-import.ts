import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { importCoolify, planCoolifyImport } from "#/api/gen/sdk.gen";
import type {
	CoolifyApplyRequest,
	CoolifyApplyResult,
	CoolifyPlan,
	CoolifyPlanRequest,
} from "#/api/gen/types.gen";
import { invalidateOperations, SERVICE_OPERATIONS } from "#/api/invalidate";
import { toast } from "#/components/ui/toast";
import { resultText } from "./lib";

// Both requests carry the Coolify token (and, later, values and registry
// passwords). They run as mutations, never as queries: a query's key holds its
// request, and a key is cached, listed by the devtools and persisted by
// anything that persists the cache. A mutation's variables are kept only in
// memory and `gcTime: 0` lets go of them as soon as nothing observes them.

/**
 * The plan request. Every call starts a new request and aborts the one before
 * it; the mutation follows the latest call, so an answer to an older request
 * never replaces a newer one (per-call callbacks of an older call never run).
 */
export function useCoolifyPlan() {
	const controller = useRef<AbortController | null>(null);
	useEffect(() => () => controller.current?.abort(), []);
	return useMutation<CoolifyPlan, unknown, CoolifyPlanRequest>({
		gcTime: 0,
		mutationFn: async (request) => {
			controller.current?.abort();
			const own = new AbortController();
			controller.current = own;
			const { data } = await planCoolifyImport({
				body: request,
				signal: own.signal,
			});
			return data;
		},
	});
}

/**
 * One key per distinct request: a retry after a lost response replays the
 * import, while a changed server, choice or option is a new request. The
 * fingerprint stays in this ref, in memory.
 */
function useIdempotencyKey() {
	const last = useRef({ fingerprint: "", key: "" });
	return (request: CoolifyApplyRequest): string => {
		const fingerprint = JSON.stringify(request);
		if (last.current.fingerprint !== fingerprint) {
			last.current = { fingerprint, key: crypto.randomUUID() };
		}
		return last.current.key;
	};
}

/**
 * Imports the reviewed projects. On success the project and service lists
 * refetch and a toast says what was staged (or deployed); warnings stay on
 * the result step. Nothing in a toast or a message carries the request.
 */
export function useCoolifyApply() {
	const queryClient = useQueryClient();
	const keyFor = useIdempotencyKey();
	return useMutation<CoolifyApplyResult, unknown, CoolifyApplyRequest>({
		gcTime: 0,
		mutationFn: async (request) => {
			const { data } = await importCoolify({
				body: request,
				headers: { "Idempotency-Key": keyFor(request) },
			});
			return data;
		},
		onSuccess: async (result, request) => {
			const text = resultText(result);
			if (request.deploy === true)
				toast.deployed("Imported from Coolify", text);
			else toast.success("Imported from Coolify", text);
			await invalidateOperations(queryClient, [
				...SERVICE_OPERATIONS,
				"listProjects",
				"listEnvironments",
			]);
		},
	});
}
