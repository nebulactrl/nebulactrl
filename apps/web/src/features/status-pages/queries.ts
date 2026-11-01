import {
	type QueryClient,
	queryOptions,
	useQueries,
	useQuery,
} from "@tanstack/react-query";
import {
	checkStatusPageDnsOptions,
	getEnvironmentUptimeOptions,
	getStatusIncidentOptions,
	getStatusPageCloudflareOptions,
	getStatusPageOptions,
	listMaintenanceWindowsOptions,
	listStatusIncidentsOptions,
	listStatusPagesOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type {
	ListMaintenanceWindowsData,
	ListStatusIncidentsData,
} from "#/api/gen/types.gen";
import { environmentsQuery, projectsQuery } from "#/api/hooks";
import { invalidateOperations } from "#/api/invalidate";

/** The control plane pushes `status_page.updated`; the lists also re-read on a timer. */
const REFRESH_MS = 30_000;

/** Everything the status pages UI shows; refetched after any mutation. */
const STATUS_PAGE_OPERATIONS = [
	"listStatusPages",
	"getStatusPage",
	"checkStatusPageDns",
	"getStatusPageCloudflare",
	"listStatusIncidents",
	"getStatusIncident",
	"listMaintenanceWindows",
	"getMaintenanceWindow",
] as const;

export function invalidateStatusPages(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, STATUS_PAGE_OPERATIONS);
}

export const statusPagesQuery = () =>
	queryOptions({
		...listStatusPagesOptions(),
		refetchInterval: REFRESH_MS,
		select: (body) => body.items ?? [],
	});

export const statusPageQuery = (pageId: string) =>
	queryOptions({
		...getStatusPageOptions({ path: { pageId } }),
		refetchInterval: REFRESH_MS,
	});

type IncidentFilter = NonNullable<ListStatusIncidentsData["query"]>;

/** Incidents, newest first, for a filter: open ones, resolved ones, or one page's. */
export const statusIncidentsQuery = (filter: IncidentFilter = {}) =>
	queryOptions({
		...listStatusIncidentsOptions({ query: { limit: 100, ...filter } }),
		refetchInterval: REFRESH_MS,
		select: (page) => page.items ?? [],
	});

export const statusIncidentQuery = (incidentId: string) =>
	queryOptions({
		...getStatusIncidentOptions({ path: { incidentId } }),
		refetchInterval: REFRESH_MS,
	});

type WindowFilter = NonNullable<ListMaintenanceWindowsData["query"]>;

export const maintenanceWindowsQuery = (filter: WindowFilter = {}) =>
	queryOptions({
		...listMaintenanceWindowsOptions({ query: { limit: 100, ...filter } }),
		refetchInterval: REFRESH_MS,
		select: (page) => page.items ?? [],
	});

/**
 * What the page's hostname resolves to right now. It is a live DNS lookup, so
 * it is read when asked for and again on Recheck, never on a timer. `fresh`
 * (a Recheck) skips the server's one-minute cache of lookups.
 */
export const statusPageDnsQuery = (
	pageId: string,
	enabled: boolean,
	fresh = false,
) =>
	queryOptions({
		...checkStatusPageDnsOptions({
			path: { pageId },
			query: { fresh },
		}),
		enabled,
		staleTime: 0,
		refetchInterval: false,
		retry: false,
	});

/**
 * The Cloudflare state of the page's hostname: zone, records, proxy and
 * origin lock. It reads the zone live, so it is asked for when the panel is
 * open and after a change, never on a timer.
 */
export const statusPageCloudflareQuery = (pageId: string) =>
	queryOptions({
		...getStatusPageCloudflareOptions({ path: { pageId } }),
		staleTime: 0,
		refetchInterval: false,
		retry: false,
	});

/**
 * The monitored state of every service of the team: projects, then their
 * environments, then each environment's uptime. The page's overall state on
 * the list is built from it, the way the public page builds its own.
 */
export function useServiceStates(enabled = true): {
	states: Map<string, string>;
	pending: boolean;
} {
	const projects = useQuery({ ...projectsQuery(), enabled });
	const projectList = projects.data ?? [];
	const environments = useQueries({
		queries: projectList.map((project) => ({
			...environmentsQuery(project.id),
			enabled,
		})),
	});
	const environmentIds = environments.flatMap((result) =>
		(result.data ?? []).map((environment) => environment.id),
	);
	const uptime = useQueries({
		queries: environmentIds.map((environmentId) => ({
			...getEnvironmentUptimeOptions({ path: { environmentId } }),
			enabled,
			refetchInterval: REFRESH_MS,
		})),
	});
	const states = new Map<string, string>();
	for (const result of uptime) {
		for (const item of result.data?.items ?? []) {
			if (item.monitored) states.set(item.serviceId, item.state);
		}
	}
	const pending =
		enabled &&
		(projects.isPending ||
			environments.some((result) => result.isPending) ||
			uptime.some((result) => result.isPending));
	return { states, pending };
}
