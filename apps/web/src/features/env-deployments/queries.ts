import {
	infiniteQueryOptions,
	type QueryClient,
	useInfiniteQuery,
	useQuery,
} from "@tanstack/react-query";
import {
	getEnvironmentDeploymentsDailyOptions,
	listEnvironmentDeploymentsInfiniteOptions,
	listEnvironmentDeploymentsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { environmentsQuery, servicesQuery } from "#/api/hooks";
import { OUTCOME_DAYS } from "./outcomes";
import { isStatusFilter, STATUS_FILTERS } from "./status";

/** Rows per page of the environment's deployment list. */
export const PAGE_SIZE = 50;

export interface DeploymentFilters {
	/** A deployment status, or undefined for all of them. */
	status?: string | undefined;
	serviceId?: string | undefined;
}

function envDeploymentsOptions(
	environmentId: string,
	filters: DeploymentFilters,
) {
	const query = {
		limit: PAGE_SIZE,
		...(filters.status ? { status: filters.status } : {}),
		...(filters.serviceId ? { serviceId: filters.serviceId } : {}),
	};
	return infiniteQueryOptions({
		...listEnvironmentDeploymentsInfiniteOptions({
			path: { environmentId },
			query,
		}),
		initialPageParam: { path: { environmentId }, query },
		getNextPageParam: (last) => last.nextCursor || undefined,
	});
}

/**
 * The environment's deployments, newest first, one cursor page at a time.
 * Same operation id as the sidebar's count, so the `/events` feed's
 * `deployment.updated` invalidation reaches both.
 */
export function useEnvDeployments(
	environmentId: string,
	filters: DeploymentFilters,
) {
	return useInfiniteQuery(envDeploymentsOptions(environmentId, filters));
}

/**
 * Starts the Deployments page's requests: the environment, then its first
 * page of deployments, the 30-day strip and the services the rows name. The
 * route calls this from its loader, so hovering the sidebar link (the
 * router's intent preload) overlaps the round trips with the pointer's
 * travel and the click renders from the cache. Failures stay in the cache;
 * the page shows them itself.
 */
export async function prefetchEnvDeployments(
	queryClient: QueryClient,
	projectId: string,
	envSlug: string,
	status: string | undefined,
	serviceId: string | undefined,
): Promise<void> {
	const environments = await queryClient
		.ensureQueryData(environmentsQuery(projectId))
		.catch(() => undefined);
	const environment = environments?.find((e) => e.slug === envSlug);
	if (!environment) return;
	await Promise.all([
		queryClient.prefetchInfiniteQuery(
			envDeploymentsOptions(environment.id, {
				status: isStatusFilter(status)
					? STATUS_FILTERS.find((filter) => filter.value === status)?.statuses
					: undefined,
				serviceId,
			}),
		),
		queryClient.prefetchQuery(
			getEnvironmentDeploymentsDailyOptions({
				path: { environmentId: environment.id },
				query: { days: OUTCOME_DAYS },
			}),
		),
		queryClient.prefetchQuery(servicesQuery(environment.id)),
	]);
}

/**
 * The sidebar's Deployments badge: how many deployments the environment has,
 * "50+" once the first page is full and more exist. Undefined until loaded,
 * and outside an environment.
 */
export function useEnvDeploymentCount(
	environmentId: string | undefined,
): string | undefined {
	const page = useQuery({
		...listEnvironmentDeploymentsOptions({
			path: { environmentId: environmentId ?? "" },
			query: { limit: PAGE_SIZE },
		}),
		enabled: environmentId !== undefined,
	});
	if (!page.data) return undefined;
	const loaded = page.data.items?.length ?? 0;
	return page.data.nextCursor ? `${loaded}+` : String(loaded);
}

/**
 * Deployments per UTC day over the last 30 days: the strip above the
 * Deployments list. The aggregate counts the whole environment, not the page
 * of rows loaded below it. The events feed refreshes it with the list; the
 * interval is the polling fallback.
 */
export function useEnvDeploymentOutcomes(environmentId: string) {
	return useQuery({
		...getEnvironmentDeploymentsDailyOptions({
			path: { environmentId },
			query: { days: OUTCOME_DAYS },
		}),
		refetchInterval: 15_000,
	});
}
