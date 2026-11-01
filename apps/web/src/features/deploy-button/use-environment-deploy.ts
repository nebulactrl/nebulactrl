import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import type { Environment, Service } from "#/api/models";
import { toast } from "#/components/ui/toast";
import { useServices } from "#/features/canvas/data";
import { startDeploy } from "#/features/deployments/quick-actions";
import {
	changesOf,
	hasInFlightDeployment,
	type ReviewGroup,
	reviewGroups,
	staleStaged,
} from "#/features/staged/staged";
import {
	useStagedActions,
	useStagedChanges,
} from "#/features/staged/use-staged";

/**
 * What the top bar's Deploy · N control and its review sheet share for one
 * environment.
 */
export interface EnvironmentDeploy {
	/** The environment's services (empty until loaded). */
	services: readonly Service[];
	/** Staged changes of one service. */
	stagedCountOf(serviceId: string): number;
	/** Staged lines per service; empty until the review sheet has asked. */
	groups: ReviewGroup[];
	/** Staged changes across the environment: the N of "Deploy · N". */
	count: number;
	/**
	 * Services flagged as staged whose change list is empty (the flag outlived
	 * the last change). They add nothing to `count` and have nothing to deploy.
	 */
	emptyStaged: readonly Service[];
	/** Some service of the environment has a deployment that has not settled. */
	inFlight: boolean;
	/** The key was pressed and the release has not settled. */
	sent: boolean;
	/** A request of this control is on its way. */
	busy: boolean;
	/** `serviceId:field` of the line being discarded, if one is. */
	discarding: string | undefined;
	/**
	 * Applies the staged changes: of every service, or only of `serviceIds`.
	 * Resolves true once the request is accepted.
	 */
	deployStaged(
		message?: string,
		serviceIds?: readonly string[],
	): Promise<boolean>;
	/** A new deployment of one service's current release. True once accepted. */
	redeployService(serviceId: string, message?: string): Promise<boolean>;
	discardChange(serviceId: string, field: string): void;
	/** Resolves once every discardable staged change is reverted. */
	discardAll(): Promise<void>;
	redeployCurrent(): Promise<void>;
}

/**
 * `detailed` loads each staged service's change lines (the review sheet is
 * open); the count itself comes from the services' `pendingChangeCounts`, so
 * the top bar costs no request of its own.
 */
export function useEnvironmentDeploy(
	environment: Pick<Environment, "id">,
	detailed: boolean,
): EnvironmentDeploy {
	const services = useServices(environment.id).data;
	const queryClient = useQueryClient();
	const { entries } = useStagedChanges(detailed ? services : undefined);
	const { deploy, discardChange, discardAll, redeployCurrent } =
		useStagedActions(environment.id, services);
	const groups = useMemo(() => reviewGroups(entries), [entries]);

	const staged = useMemo(
		() => (services ?? []).filter((service) => service.hasStagedChanges),
		[services],
	);
	// A flagged service whose changes are all gone counts for nothing: the
	// number on the Deploy key is the number of lines the review lists.
	const count = staged.reduce(
		(total, service) => total + changesOf(service),
		0,
	);
	const emptyStaged = useMemo(() => staleStaged(services ?? []), [services]);
	const inFlight = (services ?? []).some((service) =>
		hasInFlightDeployment(service),
	);

	// "Sent": held from the press until the request is answered and no
	// deployment of the environment is running.
	const [pressed, setPressed] = useState(false);
	const settled = !deploy.isPending && !inFlight;
	useEffect(() => {
		if (pressed && settled) setPressed(false);
	}, [pressed, settled]);

	const deployStaged = useCallback(
		async (message?: string, serviceIds?: readonly string[]) => {
			setPressed(true);
			try {
				await deploy.mutateAsync({
					...(message ? { message } : {}),
					...(serviceIds ? { serviceIds } : {}),
				});
				return true;
			} catch {
				setPressed(false);
				return false;
			}
		},
		[deploy],
	);

	const redeployService = useCallback(
		async (serviceId: string, message?: string) => {
			setPressed(true);
			try {
				await startDeploy(serviceId, undefined, message);
				toast.deployed(
					"Redeploying",
					"The current release is being deployed again.",
				);
				await Promise.all([
					invalidateServices(queryClient),
					invalidateOperations(queryClient, ["listEnvironmentDeployments"]),
				]);
				return true;
			} catch (error) {
				toast.error("Could not redeploy", errorMessage(error));
				setPressed(false);
				return false;
			}
		},
		[queryClient],
	);

	return {
		services: services ?? [],
		stagedCountOf: (serviceId) => {
			const service = (services ?? []).find((s) => s.id === serviceId);
			return service?.hasStagedChanges ? changesOf(service) : 0;
		},
		groups,
		count,
		emptyStaged,
		inFlight,
		sent: pressed && !settled,
		busy:
			deploy.isPending ||
			discardChange.isPending ||
			discardAll.isPending ||
			redeployCurrent.isPending,
		discarding: discardChange.isPending
			? `${discardChange.variables.serviceId}:${discardChange.variables.field}`
			: undefined,
		deployStaged,
		redeployService,
		discardChange: (serviceId, field) =>
			discardChange.mutate({ serviceId, field }),
		// The mutations toast their own failures; callers only need to know
		// when they are done.
		discardAll: () =>
			discardAll.mutateAsync(staged).then(
				() => undefined,
				() => undefined,
			),
		redeployCurrent: () =>
			redeployCurrent.mutateAsync().then(
				() => undefined,
				() => undefined,
			),
	};
}
