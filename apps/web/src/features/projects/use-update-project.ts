import { useMutation, useQueryClient } from "@tanstack/react-query";
import { updateProject } from "#/api/gen/sdk.gen";
import { projectsQuery } from "#/api/hooks";
import type { Project } from "#/api/models";

/** The fields of a project this feature edits. */
export interface ProjectDetails {
	name: string;
	description: string;
}

/**
 * Saves a project's name and description, then refreshes the project list
 * every card, switcher and page reads. The caller says what to tell the
 * reader; an error is the mutation's own, for the form to show.
 */
export function useUpdateProject(
	project: Pick<Project, "id">,
	onSaved?: (saved: Partial<ProjectDetails>) => void,
) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (next: Partial<ProjectDetails>) => {
			await updateProject({
				path: { projectId: project.id },
				body: next,
				throwOnError: true,
			});
			return next;
		},
		onSuccess: async (next) => {
			await queryClient.invalidateQueries({
				queryKey: projectsQuery().queryKey,
			});
			onSaved?.(next);
		},
	});
}
