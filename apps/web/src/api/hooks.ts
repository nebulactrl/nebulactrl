// Query-option factories over the generated TanStack Query plugin output
// (src/api/gen/@tanstack/react-query.gen.ts). Each wraps one generated
// `*Options()` and adds only what the generator cannot know: the page size,
// and a `select` that turns the wire shape into the app's models
// (src/api/models.ts). Query keys stay the generated ones, so SSE-driven
// invalidation (src/api/sse.ts) can target them by operation id.

import {
	type QueryFunction,
	type QueryFunctionContext,
	queryOptions,
	type SkipToken,
} from "@tanstack/react-query";
import {
	getCanvasOptions,
	getDeploymentOptions,
	getEnvironmentConfigSourceOptions,
	getMeOptions,
	getProjectDeletionPlanOptions,
	getServiceConfigSourceOptions,
	getServiceOptions,
	getSetupStatusOptions,
	getSystemOptions,
	getVariableHistoryOptions,
	listDeploymentEventsOptions,
	listDeploymentsOptions,
	listEnvironmentSharedVariablesOptions,
	listEnvironmentsOptions,
	listLinksOptions,
	listPendingChangesOptions,
	listProjectSharedVariablesOptions,
	listProjectsOptions,
	listServersOptions,
	listServicesOptions,
	listSuggestedVariablesOptions,
	listTeamSharedVariablesOptions,
	listVariablesOptions,
	listVolumesOptions,
} from "./gen/@tanstack/react-query.gen";
import {
	toCanvasDocument,
	toDeployment,
	toServer,
	toService,
	toVariable,
} from "./models";

const PAGE = 200;

/**
 * Wraps a generated query so its cached data is already the app's model
 * (not the wire shape). Mapping in `queryFn` rather than `select` keeps
 * `ensureQueryData`, `getQueryData` and `setQueryData` on the same type as
 * `useQuery`. The key is the generated one, minus its data-type brand.
 */
function mapped<TRaw, TOut, TKey extends readonly [object]>(
	base: {
		queryKey: TKey;
		queryFn?: QueryFunction<TRaw, TKey> | SkipToken | undefined;
	},
	map: (raw: TRaw) => TOut,
) {
	const { queryFn } = base;
	return queryOptions({
		queryKey: [...base.queryKey] as unknown as [TKey[0]],
		queryFn: async (context: QueryFunctionContext<[TKey[0]]>) => {
			if (typeof queryFn !== "function") {
				throw new Error("generated query has no queryFn");
			}
			return map(
				await queryFn(context as unknown as QueryFunctionContext<TKey>),
			);
		},
	});
}

export const setupQuery = () =>
	queryOptions({
		...getSetupStatusOptions(),
		staleTime: 30_000,
		// Session bootstrap state; it only changes through the setup form.
		refetchInterval: false,
	});

export const meQuery = () =>
	queryOptions({
		...getMeOptions(),
		staleTime: 60_000,
		retry: false,
		refetchInterval: false,
	});

export const projectsQuery = () =>
	queryOptions({
		...mapped(
			listProjectsOptions({ query: { limit: PAGE } }),
			(page) => page.items ?? [],
		),
	});

/**
 * What deleting a project removes, counted over the whole project. Read when
 * the delete dialog opens and never reused: the dialog lists exactly what
 * the server would delete now.
 */
export const projectDeletionPlanQuery = (projectId: string) =>
	queryOptions({
		...getProjectDeletionPlanOptions({ path: { projectId } }),
		staleTime: 0,
		gcTime: 0,
		refetchOnMount: "always",
		refetchInterval: false,
	});

export const environmentsQuery = (projectId: string) =>
	mapped(
		listEnvironmentsOptions({ path: { projectId } }),
		(page) => page.items ?? [],
	);

export const servicesQuery = (environmentId: string) =>
	mapped(
		listServicesOptions({ path: { environmentId }, query: { limit: PAGE } }),
		(page) => (page.items ?? []).map(toService),
	);

export const serviceQuery = (serviceId: string) =>
	mapped(getServiceOptions({ path: { serviceId } }), toService);

export const linksQuery = (environmentId: string) =>
	mapped(
		listLinksOptions({ path: { environmentId } }),
		(list) => list.items ?? [],
	);

export const serversQuery = () =>
	mapped(listServersOptions({ query: { limit: PAGE } }), (page) =>
		(page.items ?? []).map(toServer),
	);

/**
 * A canvas layout is owned by whoever is editing it: once loaded, the local
 * copy leads and is written back, so neither polling nor refetch-on-focus
 * applies. The server answers with an empty layout (version 0) when none has
 * been saved yet.
 */
export const canvasQuery = (projectId: string, environmentId: string) =>
	queryOptions({
		...mapped(
			getCanvasOptions({ path: { projectId, environmentId } }),
			toCanvasDocument,
		),
		staleTime: Number.POSITIVE_INFINITY,
		refetchInterval: false,
		refetchOnWindowFocus: false,
	});

export const deploymentsQuery = (serviceId: string) =>
	mapped(
		listDeploymentsOptions({ path: { serviceId }, query: { limit: 50 } }),
		(page) => (page.items ?? []).map(toDeployment),
	);

export const deploymentQuery = (deploymentId: string) =>
	mapped(getDeploymentOptions({ path: { deploymentId } }), toDeployment);

export const deploymentEventsQuery = (deploymentId: string) =>
	mapped(
		listDeploymentEventsOptions({ path: { deploymentId } }),
		(list) => list.items ?? [],
	);

export const variablesQuery = (serviceId: string) =>
	mapped(listVariablesOptions({ path: { serviceId } }), (list) => ({
		items: (list.items ?? []).map(toVariable),
		platform: list.platform ?? [],
		/** Virtual variables other services can reference (a database's DATABASE_URL). */
		exports: list.exports ?? [],
		warnings: list.warnings ?? [],
		restaged: list.restaged ?? [],
	}));

/** Shared variables of a project (its own layer). */
export const projectSharedVariablesQuery = (projectId: string) =>
	mapped(
		listProjectSharedVariablesOptions({ path: { projectId } }),
		(list) => list.items ?? [],
	);

/** Shared variables of an environment: its own plus the project values it inherits. */
export const environmentSharedVariablesQuery = (environmentId: string) =>
	mapped(
		listEnvironmentSharedVariablesOptions({ path: { environmentId } }),
		(list) => list.items ?? [],
	);

/** The team's shared variables, referenced as `${{team.KEY}}`. */
export const teamSharedVariablesQuery = () =>
	mapped(listTeamSharedVariablesOptions(), (list) => list.items ?? []);

/** Proposed variables: databases nothing references yet and `.env.example` keys. */
export const suggestedVariablesQuery = (serviceId: string) =>
	mapped(listSuggestedVariablesOptions({ path: { serviceId } }), (list) => ({
		items: list.items ?? [],
		commit: list.commit,
		branch: list.branch,
	}));

/** Versions of one variable, newest first. */
export const variableHistoryQuery = (serviceId: string, key: string) =>
	mapped(
		getVariableHistoryOptions({ path: { serviceId, key } }),
		(list) => list.items ?? [],
	);

export const volumesQuery = (serviceId: string) =>
	mapped(
		listVolumesOptions({ path: { serviceId } }),
		(list) => list.items ?? [],
	);

export const pendingChangesQuery = (serviceId: string) =>
	mapped(
		listPendingChangesOptions({ path: { serviceId } }),
		(list) => list.items ?? [],
	);

/** Version, public URL and MCP endpoint of this control plane. */
export const systemQuery = () =>
	queryOptions({
		...getSystemOptions(),
		staleTime: 300_000,
		refetchInterval: false,
	});

/**
 * Whether nebula.toml owns a service, and field by field whether it is in
 * sync, edited in the dashboard or changed in the file. Cheap: the control
 * plane answers from stored fingerprints, with no file read.
 */
export const serviceConfigSourceQuery = (serviceId: string) =>
	getServiceConfigSourceOptions({ path: { serviceId } });

/**
 * The environment's nebula.toml binding and the result of the last check:
 * status, commit, and per service whether it differs.
 */
export const environmentConfigSourceQuery = (environmentId: string) =>
	getEnvironmentConfigSourceOptions({ path: { environmentId } });
