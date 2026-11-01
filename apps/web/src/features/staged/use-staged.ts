import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { errorMessage } from "#/api/client-config";
import { deployStaged, discardPendingChange } from "#/api/gen/sdk.gen";
import type { FieldChange } from "#/api/gen/types.gen";
import { pendingChangesQuery } from "#/api/hooks";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { toast } from "#/components/ui/toast";
import { startDeploy } from "#/features/deployments/quick-actions";
import { FIRST_DEPLOYMENT, hasInFlightDeployment } from "./staged";

/** A service with staged changes, and what they are. */
export interface StagedEntry {
	service: Service;
	changes: readonly FieldChange[];
}

/** Pending changes of every service that has staged changes in the environment. */
export function useStagedChanges(services: readonly Service[] | undefined): {
	entries: StagedEntry[];
	loading: boolean;
} {
	const staged = useMemo(
		() => (services ?? []).filter((service) => service.hasStagedChanges),
		[services],
	);
	const results = useQueries({
		queries: staged.map((service) => pendingChangesQuery(service.id)),
	});
	const loading = results.some((result) => result.isPending);
	const entries = useMemo(
		() =>
			staged.map((service, index) => ({
				service,
				changes: results[index]?.data ?? [],
			})),
		[staged, results],
	);
	return { entries, loading };
}

/** What the environment's Deploy action sends. */
export interface DeployRequest {
	/** Limit to these services; absent deploys every service with staged changes. */
	serviceIds?: readonly string[];
	/** The optional note recorded on each deployment (≤ 200 characters). */
	message?: string;
}

function refresh(queryClient: Parameters<typeof invalidateServices>[0]) {
	return Promise.all([
		invalidateServices(queryClient),
		invalidateOperations(queryClient, ["listEnvironmentDeployments"]),
	]);
}

/**
 * Deploy, discard and redeploy for one environment's staged changes. Discard
 * is the control plane's own: each `DELETE …/pending-changes/{field}` reverts
 * one field to the current release.
 */
export function useStagedActions(
	environmentId: string,
	services: readonly Service[] | undefined,
) {
	const queryClient = useQueryClient();

	const discardChange = useMutation({
		mutationFn: async (target: { serviceId: string; field: string }) => {
			await discardPendingChange({
				path: { serviceId: target.serviceId, field: target.field },
			});
		},
		onSuccess: () => refresh(queryClient),
		onError: (error) => {
			toast.error("Could not discard that change", errorMessage(error));
			void refresh(queryClient);
		},
	});

	const discardAll = useMutation({
		mutationFn: async (staged: readonly Service[]) => {
			// One call per field, a service at a time: each one bumps the
			// service's generation. A first deployment has nothing to go back
			// to, so it is left staged.
			let discarded = 0;
			for (const service of staged) {
				const changes = await queryClient.fetchQuery({
					...pendingChangesQuery(service.id),
					staleTime: 0,
				});
				for (const change of changes) {
					if (change.field === FIRST_DEPLOYMENT) continue;
					await discardPendingChange({
						path: { serviceId: service.id, field: change.field },
					});
					discarded++;
				}
			}
			return discarded;
		},
		onSuccess: async (discarded) => {
			await refresh(queryClient);
			if (discarded === 0) {
				toast.add({
					type: "info",
					title: "Nothing to discard",
					description:
						"What is staged is a first deployment, which has no earlier release to go back to.",
				});
				return;
			}
			toast.success(
				discarded === 1 ? "Change discarded" : "Changes discarded",
				"Services are back to their deployed configuration.",
			);
		},
		onError: (error) => {
			toast.error("Could not discard the changes", errorMessage(error));
			void refresh(queryClient);
		},
	});

	const deploy = useMutation({
		mutationFn: async (request: DeployRequest) => {
			const message = request.message?.trim();
			const { data } = await deployStaged({
				path: { environmentId },
				body: {
					...(request.serviceIds
						? { serviceIds: [...request.serviceIds] }
						: {}),
					...(message ? { message } : {}),
				},
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return data.items ?? [];
		},
		onSuccess: async (deployments) => {
			toast.deployed(
				deployments.length === 1
					? "Deploying 1 service"
					: `Deploying ${deployments.length} services`,
				"Watch each card for progress.",
			);
			await refresh(queryClient);
		},
		onError: (error) => {
			toast.error("Could not deploy the changes", errorMessage(error));
			void refresh(queryClient);
		},
	});

	// "Redeploy current": a new deployment of every deployed service's current
	// configuration, databases first so what they serve is up before consumers.
	const redeployCurrent = useMutation({
		mutationFn: async () => {
			const targets = (services ?? [])
				.filter(
					(service) =>
						service.currentDeploymentId !== null &&
						// A staged service would ship its staged changes; those go
						// through Deploy, which shows them first.
						!service.hasStagedChanges &&
						!hasInFlightDeployment(service),
				)
				.sort(
					(a, b) =>
						Number(b.kind === "database") - Number(a.kind === "database"),
				);
			const failed: string[] = [];
			let started = 0;
			for (const service of targets) {
				try {
					await startDeploy(service.id);
					started++;
				} catch (error) {
					failed.push(`${service.name}: ${errorMessage(error)}`);
				}
			}
			return { started, failed };
		},
		onSuccess: async ({ started, failed }) => {
			await refresh(queryClient);
			if (failed.length > 0) {
				toast.add({
					type: "warning",
					title: `Redeploying ${started}, ${failed.length} failed`,
					description: failed.join(" "),
					timeout: 10_000,
				});
			} else if (started === 0) {
				toast.add({
					type: "info",
					title: "Nothing to redeploy",
					description:
						"No service in this environment has a running release that is not already deploying.",
				});
			} else {
				toast.deployed(
					started === 1
						? "Redeploying 1 service"
						: `Redeploying ${started} services`,
					"Each service redeploys its current release.",
				);
			}
		},
		onError: (error) => {
			toast.error("Could not redeploy", errorMessage(error));
		},
	});

	return { deploy, discardChange, discardAll, redeployCurrent };
}
