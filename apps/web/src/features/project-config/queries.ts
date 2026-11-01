import {
	type QueryClient,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import {
	applyProjectConfig,
	checkEnvironmentConfigSource,
	deleteEnvironmentConfigSource,
	exportProjectConfig,
	planProjectConfig,
	releaseServiceConfigFields,
	updateEnvironmentConfigSource,
} from "#/api/gen/sdk.gen";
import {
	environmentConfigSourceQuery,
	serviceConfigSourceQuery,
} from "#/api/hooks";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";

/** Everything the nebula.toml screens show; refetched after any apply, release or binding change. */
const CONFIG_OPERATIONS = [
	"getServiceConfigSource",
	"getEnvironmentConfigSource",
] as const;

async function refreshConfig(queryClient: QueryClient): Promise<void> {
	await Promise.all([
		invalidateOperations(queryClient, CONFIG_OPERATIONS),
		invalidateServices(queryClient),
	]);
}

/** Whether nebula.toml owns this service, and the state of each owned field. */
export function useServiceConfigSource(
	serviceId: string | undefined,
	enabled = true,
) {
	return useQuery({
		...serviceConfigSourceQuery(serviceId ?? ""),
		enabled: enabled && serviceId !== undefined,
		// Marks sit on many controls; one answer serves them all.
		staleTime: 15_000,
	});
}

/** The environment's binding and the result of its last check. */
export function useEnvironmentConfigSource(
	environmentId: string | undefined,
	enabled = true,
) {
	return useQuery({
		...environmentConfigSourceQuery(environmentId ?? ""),
		enabled: enabled && environmentId !== undefined,
		staleTime: 15_000,
	});
}

export interface EnvironmentTarget {
	projectId: string;
	/** The environment's slug: what the plan and apply calls name. */
	environmentSlug: string;
}

/**
 * Plans the file the environment's binding names, read at its newest commit
 * (or `ref`). Read-only: nothing changes until `useApplyFromGit`.
 */
export function usePlanFromGit(target: EnvironmentTarget) {
	return useMutation({
		mutationFn: async (input?: { only?: string[]; ref?: string }) => {
			const { data } = await planProjectConfig({
				path: { projectId: target.projectId },
				body: {
					environment: target.environmentSlug,
					source: { git: input?.ref ? { ref: input.ref } : {} },
					...(input?.only ? { only: input.only } : {}),
				},
			});
			return data;
		},
	});
}

export interface ApplyInput {
	/** The plan the reader reviewed: the apply fails with `plan_stale` if anything moved. */
	planId?: string;
	/** Deploy the touched services after staging. */
	deploy?: boolean;
	/** Limit the apply to these file key paths (Revert to file). */
	only?: string[];
	/** One key per intent: replaying the same one answers with the stored result. */
	idempotencyKey: string;
}

/** Stages the file the environment's binding names; applies the immediate settings. */
export function useApplyFromGit(target: EnvironmentTarget) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (input: ApplyInput) => {
			const { data } = await applyProjectConfig({
				path: { projectId: target.projectId },
				body: {
					environment: target.environmentSlug,
					source: { git: {} },
					...(input.planId ? { planId: input.planId } : {}),
					...(input.only ? { only: input.only } : {}),
					deploy: input.deploy ?? false,
				},
				headers: { "Idempotency-Key": input.idempotencyKey },
			});
			return data;
		},
		onSettled: () => refreshConfig(queryClient),
	});
}

/** "Stop managing": the listed fields stop being owned by the file; nothing else changes. */
export function useReleaseFields(serviceId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (paths: string[]) => {
			const { data } = await releaseServiceConfigFields({
				path: { serviceId },
				body: { paths },
			});
			return data;
		},
		onSuccess: () => refreshConfig(queryClient),
	});
}

export interface BindingInput {
	connectionId?: string;
	repo?: string;
	branch?: string;
	path?: string;
	autoApply?: boolean;
}

export function useUpdateBinding(environmentId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (body: BindingInput) => {
			const { data } = await updateEnvironmentConfigSource({
				path: { environmentId },
				body,
			});
			return data;
		},
		onSuccess: () => refreshConfig(queryClient),
	});
}

export function useClearBinding(environmentId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async () => {
			await deleteEnvironmentConfigSource({ path: { environmentId } });
		},
		onSuccess: () => refreshConfig(queryClient),
	});
}

/** Reads the file now and stores the result: the "Check now" button. */
export function useCheckBinding(environmentId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async () => {
			const { data } = await checkEnvironmentConfigSource({
				path: { environmentId },
			});
			return data;
		},
		onSuccess: () => refreshConfig(queryClient),
	});
}

export interface ExportedConfig {
	file: string;
	/** Not-representable fields, `secret_flag_differs`: diagnostics or sentences. */
	warnings: unknown[];
}

/** What to export: one environment, or all of them factored into base + overrides. */
export interface ExportTarget {
	projectId: string;
	/** An environment slug, or "all". */
	environment: string;
}

/**
 * The live project as a nebula.toml. Asks for the JSON form so the warnings
 * come with the file; the plain-text form carries the file alone.
 */
export async function exportConfig(
	target: ExportTarget,
): Promise<ExportedConfig> {
	const { data } = await exportProjectConfig({
		path: { projectId: target.projectId },
		query: { environment: target.environment },
		headers: { Accept: "application/json" },
	});
	const body: unknown = data;
	if (typeof body === "string") return { file: body, warnings: [] };
	const result: ExportedConfig = { file: "", warnings: [] };
	if (typeof body === "object" && body !== null) {
		if ("file" in body && typeof body.file === "string")
			result.file = body.file;
		if ("warnings" in body && Array.isArray(body.warnings)) {
			result.warnings = body.warnings;
		}
	}
	return result;
}
