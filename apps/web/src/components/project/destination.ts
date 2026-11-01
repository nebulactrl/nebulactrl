/** Where a card or switcher entry leads: a project's canvas on one environment. */
export interface ProjectDestination {
	to: "/p/$projectId/$envSlug";
	params: { projectId: string; envSlug: string };
}

export function projectDestination(
	projectId: string,
	envSlug: string,
): ProjectDestination {
	return { to: "/p/$projectId/$envSlug", params: { projectId, envSlug } };
}
