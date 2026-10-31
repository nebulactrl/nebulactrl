import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef } from "react";
import {
	deploymentQuery,
	deploymentsQuery,
	pendingChangesQuery,
	variablesQuery,
} from "#/api/hooks";
import type { Service } from "#/api/models";
import { serviceMetricsQuery } from "#/features/metrics/queries";
import { preloadOverview } from "./inspector/tab-bodies";

/** How long a pointer rests on a card before it counts as intent. */
const INTENT_MS = 80;
/** A prefetch younger than this is not repeated by the next hover. */
const FRESH_MS = 10_000;

/**
 * Starts what the inspector's Overview reads on open: its chunk and the
 * service's deployments, variables, latest deployment, staged changes and
 * resource series. They are the five requests the panel fires when it
 * mounts, so a click on a card that has been hovered finds them cached.
 */
export function prefetchInspector(
	queryClient: QueryClient,
	service: Service,
): void {
	preloadOverview(service);
	const fresh = { staleTime: FRESH_MS };
	void queryClient.prefetchQuery({ ...deploymentsQuery(service.id), ...fresh });
	void queryClient.prefetchQuery({ ...variablesQuery(service.id), ...fresh });
	if (service.latestDeployment) {
		void queryClient.prefetchQuery({
			...deploymentQuery(service.latestDeployment.id),
			...fresh,
		});
	}
	if (service.hasStagedChanges) {
		void queryClient.prefetchQuery({
			...pendingChangesQuery(service.id),
			...fresh,
		});
	}
	if (service.currentDeploymentId !== null) {
		void queryClient.prefetchQuery({
			...serviceMetricsQuery(service.id, "1h"),
			...fresh,
		});
	}
}

/**
 * Pointer and focus handlers for a card: the pointer resting on it for a
 * moment, or the keyboard arriving, prefetches its inspector. Passing over a
 * card on the way elsewhere starts nothing.
 */
export function useInspectorPrefetch(service: Service) {
	const queryClient = useQueryClient();
	const timer = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(timer.current), []);
	return useMemo(
		() => ({
			onPointerEnter: () => {
				window.clearTimeout(timer.current);
				timer.current = window.setTimeout(
					() => prefetchInspector(queryClient, service),
					INTENT_MS,
				);
			},
			onPointerLeave: () => window.clearTimeout(timer.current),
			onFocus: () => prefetchInspector(queryClient, service),
		}),
		[queryClient, service],
	);
}
