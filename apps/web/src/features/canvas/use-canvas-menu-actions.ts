import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { errorMessage } from "#/api/client-config";
import { deleteLink } from "#/api/gen/sdk.gen";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import type { ServiceLink } from "#/api/models";
import { toast } from "#/components/ui/toast";
import { openStagedReview } from "#/features/deploy-button";
import {
	startDeploy,
	startRestart,
} from "#/features/deployments/quick-actions";

export interface CanvasMenuActions {
	/**
	 * Deploys each service. Production has one confirmation, the guarded key in
	 * the review sheet, so there it opens the sheet instead of deploying.
	 */
	deploySelected(serviceIds: readonly string[]): Promise<void>;
	/** Asks each service's server to restart its container. */
	restartSelected(serviceIds: readonly string[]): Promise<void>;
	/** Removes a manual link; presentation only, no deployment is touched. */
	removeLink(link: Pick<ServiceLink, "id">): Promise<void>;
}

function failures(
	results: readonly PromiseSettledResult<unknown>[],
): unknown[] {
	return results.flatMap((result) =>
		result.status === "rejected" ? [result.reason] : [],
	);
}

/**
 * What the canvas menus do that no single card's actions cover: the bulk
 * deploy and restart of a selection, and removing a cable. The requests are
 * the ones the dock and the palette send, one per service; each answer is the
 * control plane's (202), never a pretend success.
 */
export function useCanvasMenuActions(isProduction: boolean): CanvasMenuActions {
	const queryClient = useQueryClient();

	const refresh = useCallback(
		() =>
			Promise.all([
				invalidateServices(queryClient),
				invalidateOperations(queryClient, ["listEnvironmentDeployments"]),
			]).then(() => undefined),
		[queryClient],
	);

	const deploySelected = useCallback(
		async (serviceIds: readonly string[]) => {
			if (serviceIds.length === 0) return;
			if (isProduction) {
				openStagedReview();
				return;
			}
			const results = await Promise.allSettled(
				serviceIds.map((id) => startDeploy(id)),
			);
			const failed = failures(results);
			const started = results.length - failed.length;
			if (started > 0) {
				toast.deployed(
					started === 1
						? "Deploying 1 service"
						: `Deploying ${started} services`,
					"Watch each card for progress.",
				);
			}
			if (failed.length > 0) {
				toast.error(
					failed.length === 1
						? "Could not deploy 1 service"
						: `Could not deploy ${failed.length} services`,
					errorMessage(failed[0]),
				);
			}
			await refresh();
		},
		[isProduction, refresh],
	);

	const restartSelected = useCallback(
		async (serviceIds: readonly string[]) => {
			if (serviceIds.length === 0) return;
			const results = await Promise.allSettled(
				serviceIds.map((id) => startRestart(id)),
			);
			const failed = failures(results);
			const sent = results.length - failed.length;
			if (sent > 0) {
				toast.success(
					sent === 1 ? "Restarting 1 service" : `Restarting ${sent} services`,
					"The commands were sent to their servers.",
				);
			}
			if (failed.length > 0) {
				toast.error(
					failed.length === 1
						? "Could not restart 1 service"
						: `Could not restart ${failed.length} services`,
					errorMessage(failed[0]),
				);
			}
			await refresh();
		},
		[refresh],
	);

	const removeLink = useCallback(
		async (link: Pick<ServiceLink, "id">) => {
			try {
				await deleteLink({ path: { linkId: link.id } });
				await invalidateOperations(queryClient, ["listLinks"]);
				toast.success(
					"Link removed",
					"The services and their variables are unchanged.",
				);
			} catch (failure) {
				toast.error("Could not remove the link", errorMessage(failure));
			}
		},
		[queryClient],
	);

	return useMemo(
		() => ({ deploySelected, restartSelected, removeLink }),
		[deploySelected, restartSelected, removeLink],
	);
}
