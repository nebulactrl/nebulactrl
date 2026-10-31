import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useCallback } from "react";
import { environmentsQuery } from "#/api/hooks";
import { productionFirst } from "#/api/models";
import { type EnvSection, envSectionRoute } from "./section";

/**
 * Opens a project on its production environment (first by order), on
 * `section` when given (the page being read when switching projects), else
 * its canvas. The environment list is a cache hit for any card already
 * drawn; a project whose list cannot be read falls back to the `production`
 * slug every project is created with.
 */
export function useOpenProject(): (
	projectId: string,
	section?: EnvSection,
) => Promise<void> {
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	return useCallback(
		async (projectId: string, section?: EnvSection) => {
			const environments = await queryClient
				.ensureQueryData(environmentsQuery(projectId))
				.catch(() => undefined);
			const envSlug =
				productionFirst(environments ?? [])[0]?.slug ?? "production";
			await navigate({
				to: envSectionRoute(section),
				params: { projectId, envSlug },
			});
		},
		[navigate, queryClient],
	);
}
