import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
	deleteVariable,
	discardPendingChange,
	putVariables,
	restoreVariableVersion,
	rotateVariable,
} from "#/api/gen/sdk.gen";
import { invalidateServices } from "#/api/invalidate";
import type { VariableScope } from "#/api/models";
import type { EnvDiff } from "./raw-env";

export interface VariableWrite {
	key: string;
	/** Omitted to keep the stored value of an existing variable. */
	value?: string;
	/** Omitted to keep the stored flag; generators decide it on their own. */
	isSecret?: boolean;
	scope?: VariableScope;
	comment?: string;
	/** Reminder in days; 0 clears it. */
	rotateEveryDays?: number;
	/** "suggested" marks a key taken from the repository's `.env.example`. */
	source?: "suggested";
}

/**
 * Variable writes for one service. Every write bumps the service's
 * generation (a staged change), and may restage other services that
 * reference it, so each waits for the service queries to refetch before it
 * reports done.
 */
export function useVariableMutations(serviceId: string) {
	const queryClient = useQueryClient();
	const refresh = () => invalidateServices(queryClient);

	const save = useMutation({
		mutationFn: async (variable: VariableWrite) => {
			const { data } = await putVariables({
				path: { serviceId },
				body: { variables: [variable] },
			});
			return data;
		},
		onSuccess: refresh,
	});

	/** Stage several variables at once (the `.env.example` review). */
	const saveMany = useMutation({
		mutationFn: async (variables: VariableWrite[]) => {
			const { data } = await putVariables({
				path: { serviceId },
				body: { variables },
			});
			return data;
		},
		onSuccess: refresh,
	});

	const remove = useMutation({
		mutationFn: async (key: string) => {
			await deleteVariable({ path: { serviceId, key } });
		},
		onSuccess: refresh,
	});

	const applyDiff = useMutation({
		mutationFn: async (diff: EnvDiff) => {
			if (diff.upserts.length > 0) {
				await putVariables({
					path: { serviceId },
					body: { variables: diff.upserts },
				});
			}
			for (const key of diff.deletes) {
				await deleteVariable({ path: { serviceId, key } });
			}
		},
		onSuccess: refresh,
	});

	// Reverts one variable to what the current release has: an edit is undone,
	// an added variable is removed, a deleted one comes back.
	const discard = useMutation({
		mutationFn: async (key: string) => {
			await discardPendingChange({
				path: { serviceId, field: `variables.${key}` },
			});
		},
		onSuccess: refresh,
	});

	/** Stage a new value for a generated variable (`dryRun: false`). */
	const rotate = useMutation({
		mutationFn: async (key: string) => {
			const { data } = await rotateVariable({
				path: { serviceId, key },
				body: { dryRun: false },
			});
			return data;
		},
		onSuccess: refresh,
	});

	/** Write an old non-secret version again as a new one. */
	const restore = useMutation({
		mutationFn: async (input: { key: string; version: number }) => {
			const { data } = await restoreVariableVersion({
				path: { serviceId, key: input.key, version: input.version },
			});
			return data;
		},
		onSuccess: refresh,
	});

	return { save, saveMany, remove, applyDiff, discard, rotate, restore };
}
