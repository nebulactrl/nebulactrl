import { useQueries, useQuery } from "@tanstack/react-query";
import { environmentsQuery, projectsQuery, servicesQuery } from "#/api/hooks";

export interface TeamService {
	id: string;
	name: string;
	projectId: string;
	projectName: string;
	environmentName: string;
}

/**
 * Every service of the team, with the project and environment it lives in.
 * The API lists services per environment, so this walks projects →
 * environments → services over the cached queries the canvas already uses.
 * Nothing is fetched while `enabled` is false.
 */
export function useTeamServices(enabled: boolean): {
	services: TeamService[];
	pending: boolean;
} {
	const projects = useQuery({ ...projectsQuery(), enabled });
	const projectList = projects.data ?? [];
	const environmentResults = useQueries({
		queries: projectList.map((project) => ({
			...environmentsQuery(project.id),
			enabled,
		})),
	});
	const pairs: {
		projectId: string;
		projectName: string;
		environmentId: string;
		environmentName: string;
	}[] = [];
	environmentResults.forEach((result, index) => {
		const project = projectList[index];
		if (!project) return;
		for (const environment of result.data ?? []) {
			pairs.push({
				projectId: project.id,
				projectName: project.name,
				environmentId: environment.id,
				environmentName: environment.name,
			});
		}
	});
	const serviceResults = useQueries({
		queries: pairs.map((pair) => ({
			...servicesQuery(pair.environmentId),
			enabled,
		})),
	});

	const services: TeamService[] = [];
	serviceResults.forEach((result, index) => {
		const pair = pairs[index];
		if (!pair) return;
		for (const service of result.data ?? []) {
			services.push({
				id: service.id,
				name: service.name,
				projectId: pair.projectId,
				projectName: pair.projectName,
				environmentName: pair.environmentName,
			});
		}
	});
	services.sort(
		(a, b) =>
			a.projectName.localeCompare(b.projectName) ||
			a.environmentName.localeCompare(b.environmentName) ||
			a.name.localeCompare(b.name),
	);

	const pending =
		enabled &&
		(projects.isPending ||
			environmentResults.some((r) => r.isPending) ||
			serviceResults.some((r) => r.isPending));
	return { services, pending };
}
