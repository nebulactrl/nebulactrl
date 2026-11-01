import { queryOptions } from "@tanstack/react-query";
import {
	getCatalogTemplateOptions,
	listCatalogTemplatesOptions,
	listTemplateInstancesOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { planTemplate } from "#/api/gen/sdk.gen";
import type { TemplateInstance } from "#/api/gen/types.gen";

// Generated keys, so the SSE feed can reach the instance list by operation
// id (`listTemplateInstances`, see src/api/sse.ts).

/** The whole catalog: filtering, search and chip counts happen in the client. */
export const catalogQuery = () =>
	queryOptions({
		...listCatalogTemplatesOptions(),
		// The catalog is embedded in the server binary: it changes with a release.
		staleTime: 10 * 60_000,
		select: (list) => ({
			items: list.items ?? [],
			categories: list.categories ?? [],
		}),
	});

export const templateQuery = (templateId: string) =>
	queryOptions({
		...getCatalogTemplateOptions({ path: { templateId } }),
		staleTime: 10 * 60_000,
		retry: false,
	});

const NO_INSTANCES: TemplateInstance[] = [];

export const instancesQuery = (environmentId: string) =>
	queryOptions({
		...listTemplateInstancesOptions({ path: { environmentId } }),
		select: (list) => list ?? NO_INSTANCES,
	});

export interface PlanArgs {
	environmentId: string;
	templateId: string;
	serverId: string;
	inputs: Record<string, string>;
}

/** The preflight is a POST; it reads nothing it writes, so it is cached like a query. */
export const planQuery = (args: PlanArgs) =>
	queryOptions({
		queryKey: [
			"templatePlan",
			args.environmentId,
			args.templateId,
			args.serverId,
			args.inputs,
		],
		queryFn: async ({ signal }) => {
			const { data } = await planTemplate({
				path: {
					environmentId: args.environmentId,
					templateId: args.templateId,
				},
				body: {
					...(args.serverId ? { serverId: args.serverId } : {}),
					inputs: args.inputs,
				},
				signal,
			});
			return data;
		},
		retry: false,
		staleTime: 0,
		gcTime: 30_000,
		refetchOnWindowFocus: false,
	});
