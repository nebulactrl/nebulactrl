import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getDeployHookOptions } from "#/api/gen/@tanstack/react-query.gen";
import { rotateDeployHook, setDeployHook } from "#/api/gen/sdk.gen";
import { invalidateOperations } from "#/api/invalidate";
import { toast } from "#/components/ui/toast";

const HOOK_OPERATIONS = ["getDeployHook"] as const;

/**
 * A service's deploy hook and the two things that change it. The complete URL
 * only ever arrives in the response that creates its secret, so it is kept
 * here, in memory, until `dismiss` is called or the service changes: it is
 * never written to the query cache.
 */
export function useDeployHook(serviceId: string) {
	const queryClient = useQueryClient();
	// Calls to the URL come from outside the app, so "last call" is polled.
	const hook = useQuery({
		...getDeployHookOptions({ path: { serviceId } }),
		refetchInterval: 15_000,
	});
	const [revealed, setRevealed] = useState<string | null>(null);
	// biome-ignore lint/correctness/useExhaustiveDependencies: a URL belongs to one service.
	useEffect(() => setRevealed(null), [serviceId]);

	const refresh = () => invalidateOperations(queryClient, HOOK_OPERATIONS);

	const setEnabled = useMutation({
		mutationFn: async (enabled: boolean) => {
			const { data } = await setDeployHook({
				path: { serviceId },
				body: { enabled },
			});
			return data;
		},
		onSuccess: async (view) => {
			setRevealed(view.url ?? null);
			await refresh();
			toast.success(
				view.enabled ? "Deploy hook is on" : "Deploy hook is off",
				view.enabled
					? "Copy the URL now: it is shown once."
					: "The old URL no longer works.",
			);
		},
		onError: (error) =>
			toast.error("Could not change the deploy hook", errorMessage(error)),
	});

	const rotate = useMutation({
		mutationFn: async () => {
			const { data } = await rotateDeployHook({ path: { serviceId } });
			return data;
		},
		onSuccess: async (view) => {
			setRevealed(view.url ?? null);
			await refresh();
			toast.success(
				"Deploy hook rotated",
				"The previous URL stopped working. Copy the new one now: it is shown once.",
			);
		},
		onError: (error) =>
			toast.error("Could not rotate the deploy hook", errorMessage(error)),
	});

	return {
		hook,
		revealed,
		dismiss: () => setRevealed(null),
		setEnabled,
		rotate,
	};
}
