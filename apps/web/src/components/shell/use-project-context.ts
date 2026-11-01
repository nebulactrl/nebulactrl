import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { environmentsQuery, projectsQuery } from "#/api/hooks";
import { type Environment, type Project, productionFirst } from "#/api/models";

export interface ProjectContext {
	projectId: string;
	project: Project | undefined;
	environments: Environment[];
	/** The environment named by the `$envSlug` route param. */
	environment: Environment | undefined;
	envSlug: string | undefined;
}

/**
 * The project and environment the reader is inside, if any. The top bar's
 * switchers and the command palette read it, so the location lives in one
 * place: the `/p/$projectId/$envSlug` route params.
 */
export function useProjectContext(): ProjectContext | undefined {
	const params = useParams({ strict: false }) as {
		projectId?: string;
		envSlug?: string;
	};
	const projectId = params.projectId;
	const projects = useQuery({
		...projectsQuery(),
		enabled: projectId !== undefined,
	});
	const environmentsResult = useQuery({
		...environmentsQuery(projectId ?? ""),
		enabled: projectId !== undefined,
	});
	if (projectId === undefined) return undefined;
	const environments = productionFirst(environmentsResult.data ?? []);
	return {
		projectId,
		project: projects.data?.find((project) => project.id === projectId),
		environments,
		environment: environments.find(
			(candidate) => candidate.slug === params.envSlug,
		),
		envSlug: params.envSlug,
	};
}
