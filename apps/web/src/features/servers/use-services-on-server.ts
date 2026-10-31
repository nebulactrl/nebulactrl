import { useQueries, useQuery } from "@tanstack/react-query";
import type { Environment, Project } from "#/api/gen/types.gen";
import { environmentsQuery, projectsQuery, servicesQuery } from "#/api/hooks";
import type { Service } from "#/api/models";

export interface PlacedService {
	service: Service;
	project: Project;
	environment: Environment;
}

/**
 * The services placed on one server. The API lists services per
 * environment, so this walks projects → environments → services over the
 * cached queries and keeps the ones whose `serverId` matches; each level is
 * a normal cached query, so the canvas and this page share the data.
 */
export function useServicesOnServer(serverId: string): {
	placed: PlacedService[];
	/** Every service of the team, wherever it runs: names for ids found on this server's disk. */
	everywhere: Service[];
	pending: boolean;
} {
	const projects = useQuery(projectsQuery());
	const projectList = projects.data ?? [];
	const environmentResults = useQueries({
		queries: projectList.map((project) => environmentsQuery(project.id)),
	});
	const pairs: { project: Project; environment: Environment }[] = [];
	environmentResults.forEach((result, index) => {
		const project = projectList[index];
		if (!project) return;
		for (const environment of result.data ?? []) {
			pairs.push({ project, environment });
		}
	});
	const serviceResults = useQueries({
		queries: pairs.map((pair) => servicesQuery(pair.environment.id)),
	});

	const placed: PlacedService[] = [];
	const everywhere: Service[] = [];
	serviceResults.forEach((result, index) => {
		const pair = pairs[index];
		if (!pair) return;
		for (const service of result.data ?? []) {
			everywhere.push(service);
			if (service.serverId === serverId) {
				placed.push({
					service,
					project: pair.project,
					environment: pair.environment,
				});
			}
		}
	});
	placed.sort((a, b) => a.service.name.localeCompare(b.service.name));

	const pending =
		projects.isPending ||
		environmentResults.some((r) => r.isPending) ||
		serviceResults.some((r) => r.isPending);
	return { placed, everywhere, pending };
}
