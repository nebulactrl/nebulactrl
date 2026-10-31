import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { ApiError, errorCode, errorMessage } from "#/api/client-config";
import { rollbackDeployment, stopService } from "#/api/gen/sdk.gen";
import { invalidateOperations, SERVICE_OPERATIONS } from "#/api/invalidate";
import { type Deployment, type Service, toDeployment } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { useProjectContext } from "#/components/shell/use-project-context";
import { toast } from "#/components/ui/toast";
import { relativeTime } from "#/lib/format";
import {
	cancelDeploymentRequest,
	startDeploy,
	startRestart,
} from "./quick-actions";
import { rollbackPrompt } from "./rollback-prompt";

export const ROLLBACK_DISCLAIMER =
	"Application image and configuration only. Databases, volumes and external services are not rolled back.";

/** The commit a pruned git release was built from, from the 409's details. */
function prunedCommit(error: unknown): string | undefined {
	if (!(error instanceof ApiError) || error.code !== "image_pruned")
		return undefined;
	const sha = error.details?.commitSha;
	// Without the commit there is nothing to rebuild: report the 409 as is.
	return typeof sha === "string" && sha !== "" ? sha : undefined;
}

/** Everything a deployment's status change reaches: the lists and its detail. */
const DEPLOYMENT_OPERATIONS = [
	...SERVICE_OPERATIONS,
	"listEnvironmentDeployments",
	"getDeployment",
	"listDeploymentEvents",
] as const;

/**
 * Deploy, redeploy, cancel, roll back, restart and stop for one service. Every call
 * returns what the server accepted (202), never a pretend success: the
 * deployment's real status arrives through the events feed.
 */
export function useDeploymentActions(service: Service) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const context = useProjectContext();
	// The service's own environment decides whether a confirm is guarded; an
	// unknown one (environments still loading) is treated as production.
	const environment = context?.environments.find(
		(candidate) => candidate.id === service.environmentId,
	);
	const refresh = () =>
		invalidateOperations(queryClient, DEPLOYMENT_OPERATIONS);

	const deploy = useMutation({
		mutationFn: (commitSha?: string) => startDeploy(service.id, commitSha),
		onSuccess: () => {
			toast.deployed(
				`Deploying ${service.name}`,
				"Follow it under Deployments.",
			);
			return refresh();
		},
		onError: (error) => {
			toast.error(`Could not deploy ${service.name}`, errorMessage(error));
		},
	});

	const deployNoCache = useMutation({
		mutationFn: () => startDeploy(service.id, undefined, undefined, true),
		onSuccess: () => {
			toast.deployed(
				`Deploying ${service.name} without cache`,
				"The image is built from scratch. Follow it under Deployments.",
			);
			return refresh();
		},
		onError: (error) => {
			toast.error(
				`Could not deploy ${service.name} without cache`,
				errorMessage(error),
			);
		},
	});

	/** Asks first (a clean build is slower), then deploys without cache. */
	const confirmDeployNoCache = useCallback(async (): Promise<void> => {
		await confirm({
			title: `Redeploy ${service.name} without cache?`,
			body: "Builds this one deployment from scratch: every layer is rebuilt and base images are pulled again (docker build --no-cache --pull), and an image a build server already built for this commit is not reused. It takes longer than a normal deploy. Later deployments use the cache again.",
			cta: "Redeploy without cache",
			hold: false,
			onConfirm: async () => {
				await deployNoCache.mutateAsync().catch(() => undefined);
			},
		});
	}, [confirm, deployNoCache, service.name]);

	const restart = useMutation({
		mutationFn: () => startRestart(service.id),
		onSuccess: () => {
			toast.success(
				`Restarting ${service.name}`,
				"The command was sent to its server.",
			);
			return refresh();
		},
		onError: (error) => {
			toast.error(`Could not restart ${service.name}`, errorMessage(error));
		},
	});

	const stop = useMutation({
		mutationFn: async () => {
			await stopService({ path: { serviceId: service.id } });
		},
		onSuccess: () => {
			toast.success(
				`Stopping ${service.name}`,
				"The container stops; its data stays.",
			);
			return refresh();
		},
		onError: (error) => {
			toast.error(`Could not stop ${service.name}`, errorMessage(error));
		},
	});

	const cancel = useMutation({
		mutationFn: (deploymentId: string) => cancelDeploymentRequest(deploymentId),
		onSuccess: () => {
			toast.success(
				`Canceling the deployment of ${service.name}`,
				"The agent is stopping it. The previous release keeps serving.",
			);
			return refresh();
		},
		onError: (error) => {
			// 409 cannot_cancel: the deployment already finished. Its message says
			// so; there is nothing to retry.
			toast.error(
				errorCode(error) === "cannot_cancel"
					? "Too late to cancel"
					: `Could not cancel the deployment of ${service.name}`,
				errorMessage(error),
			);
			// The deployment moved on without us: show where it is now.
			return refresh();
		},
	});

	/** Asks first, then cancels. Failures are toasted by the mutation. */
	const confirmCancel = useCallback(
		async (target: Deployment): Promise<void> => {
			await confirm({
				title: `Cancel this deployment of ${service.name}?`,
				body: "The deployment stops at the step it has reached: a build is killed, the new containers are removed and the previous routes are put back. The current release keeps serving. If the new release is already live it is too late.",
				cta: "Cancel deployment",
				dismiss: "Keep running",
				onConfirm: async () => {
					await cancel.mutateAsync(target.id).catch(() => undefined);
				},
			});
		},
		[cancel, confirm, service.name],
	);

	const rollbackMutation = useMutation({
		mutationFn: async (deploymentId: string): Promise<Deployment> => {
			const { data } = await rollbackDeployment({
				path: { deploymentId },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return toDeployment(data);
		},
		onSuccess: refresh,
	});

	/** Asks first, with the data disclaimer, then rolls back. */
	const rollback = useCallback(
		async (target: Deployment, label: string): Promise<void> => {
			// A pruned image is an answer, not a failure: it closes this dialog
			// and offers the rebuild in the next one.
			let pruned: string | undefined;
			try {
				const prompt = rollbackPrompt({
					serviceName: service.name,
					environmentName: environment?.name,
					production: environment?.isProduction ?? true,
					label,
					when: relativeTime(target.finishedAt ?? target.createdAt),
					message: target.message,
					disclaimer: ROLLBACK_DISCLAIMER,
				});
				const ok = await confirm({
					title: prompt.title,
					body: prompt.body,
					cta: "Roll back",
					guarded: prompt.guarded,
					onConfirm: async () => {
						try {
							await rollbackMutation.mutateAsync(target.id);
						} catch (error) {
							const sha = prunedCommit(error);
							if (sha === undefined) throw error;
							pruned = sha;
						}
					},
				});
				if (ok && pruned === undefined) {
					toast.success(
						`Rolling back ${service.name}`,
						`Deploying ${label} again.`,
					);
				}
			} catch (error) {
				toast.error("Rollback failed", errorMessage(error));
				return;
			}
			if (pruned === undefined) return;
			const commit = pruned;
			const short = commit.slice(0, 7);
			const source = service.config.gitUrl || "its repository";
			await confirm({
				title: `The image for ${short} was pruned`,
				body: `Old images are removed to save disk, so this release can no longer start as it was. Rebuilds commit ${short} from ${source}, then deploys it.`,
				cta: `Rebuild commit ${short}`,
				onConfirm: async () => {
					await deploy.mutateAsync(commit);
				},
			});
		},
		[
			confirm,
			deploy,
			environment,
			rollbackMutation,
			service.config.gitUrl,
			service.name,
		],
	);

	return {
		deploy,
		deployNoCache,
		confirmDeployNoCache,
		cancel,
		confirmCancel,
		restart,
		stop,
		rollback,
	};
}
