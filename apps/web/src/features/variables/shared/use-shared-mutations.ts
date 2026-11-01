import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
	deleteEnvironmentSharedVariable,
	deleteProjectSharedVariable,
	deleteTeamSharedVariable,
	putEnvironmentSharedVariables,
	putProjectSharedVariables,
	putTeamSharedVariables,
	rotateEnvironmentSharedVariable,
	rotateProjectSharedVariable,
	rotateTeamSharedVariable,
	shareEnvironmentSharedVariable,
	shareProjectSharedVariable,
	shareTeamSharedVariable,
} from "#/api/gen/sdk.gen";
import { invalidateServices } from "#/api/invalidate";

/**
 * Where a shared variable lives: the whole team, a whole project, or one of its
 * environments. The team has no id: it is the caller's current team.
 */
export type SharedOwner =
	| { layer: "team"; id?: undefined }
	| { layer: "project" | "environment"; id: string };

export interface SharedWrite {
	key: string;
	/** Omitted to keep the stored value. */
	value?: string;
	isSecret?: boolean;
	comment?: string;
}

/**
 * Writes to the shared variables of one project or environment. A shared
 * write can restage services in every environment it reaches, so each waits
 * for the service queries to refetch.
 */
export function useSharedMutations(owner: SharedOwner) {
	const queryClient = useQueryClient();
	const refresh = () => invalidateServices(queryClient);
	const save = useMutation({
		mutationFn: async (variable: SharedWrite) => {
			const body = { variables: [variable] };
			const { data } =
				owner.layer === "team"
					? await putTeamSharedVariables({ body })
					: owner.layer === "project"
						? await putProjectSharedVariables({
								path: { projectId: owner.id },
								body,
							})
						: await putEnvironmentSharedVariables({
								path: { environmentId: owner.id },
								body,
							});
			return data;
		},
		onSuccess: refresh,
	});

	const remove = useMutation({
		mutationFn: async (key: string) => {
			const { data } =
				owner.layer === "team"
					? await deleteTeamSharedVariable({ path: { key } })
					: owner.layer === "project"
						? await deleteProjectSharedVariable({
								path: { projectId: owner.id, key },
							})
						: await deleteEnvironmentSharedVariable({
								path: { environmentId: owner.id, key },
							});
			return data;
		},
		onSuccess: refresh,
	});

	const rotate = useMutation({
		mutationFn: async (input: { key: string; dryRun: boolean }) => {
			const body = { dryRun: input.dryRun };
			const { data } =
				owner.layer === "team"
					? await rotateTeamSharedVariable({
							path: { key: input.key },
							body,
						})
					: owner.layer === "project"
						? await rotateProjectSharedVariable({
								path: { projectId: owner.id, key: input.key },
								body,
							})
						: await rotateEnvironmentSharedVariable({
								path: { environmentId: owner.id, key: input.key },
								body,
							});
			return data;
		},
		onSuccess: (_data, input) => (input.dryRun ? undefined : refresh()),
	});

	const share = useMutation({
		mutationFn: async (input: { key: string; serviceIds: string[] }) => {
			const body = { serviceIds: input.serviceIds };
			const { data } =
				owner.layer === "team"
					? await shareTeamSharedVariable({
							path: { key: input.key },
							body,
						})
					: owner.layer === "project"
						? await shareProjectSharedVariable({
								path: { projectId: owner.id, key: input.key },
								body,
							})
						: await shareEnvironmentSharedVariable({
								path: { environmentId: owner.id, key: input.key },
								body,
							});
			return data;
		},
		onSuccess: refresh,
	});

	return { save, remove, rotate, share };
}
