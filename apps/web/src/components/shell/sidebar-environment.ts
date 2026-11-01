import type { ProjectContext } from "./use-project-context";

export interface EnvironmentRef {
	projectId: string;
	envSlug: string;
}

/**
 * Where the sidebar's project items point. On a project's page, at the
 * environment on screen. On a page outside any project, at `remembered`, the
 * environment this team's reader last opened, and only while that project is
 * still one of the team's `projectIds`. Until the projects have loaded
 * nothing is known, so nothing is offered: a link to another team's project
 * is a "Project not found" page.
 */
export function resolveSidebarEnvironment({
	context,
	remembered,
	projectIds,
}: {
	context: ProjectContext | undefined;
	remembered: EnvironmentRef | undefined;
	projectIds: ReadonlySet<string> | undefined;
}): EnvironmentRef | undefined {
	const projectId = context?.projectId;
	const envSlug = context?.envSlug;
	if (projectId && envSlug) return { projectId, envSlug };
	// A page of the project outside any environment (its settings): the sidebar's
	// items stay on this project, on the last environment read here or the
	// first one it has.
	if (projectId) {
		if (remembered?.projectId === projectId) return remembered;
		const first = context?.environments[0];
		return first ? { projectId, envSlug: first.slug } : undefined;
	}
	if (!(remembered && projectIds?.has(remembered.projectId))) return undefined;
	return remembered;
}
