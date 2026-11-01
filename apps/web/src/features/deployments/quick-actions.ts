import {
	cancelDeployment,
	deployService,
	restartService,
} from "#/api/gen/sdk.gen";
import { type Deployment, toDeployment } from "#/api/models";

/**
 * POST a deployment of the service's current configuration (202). With
 * `commitSha` (git services only) it builds that exact commit instead of the
 * branch head: "Rebuild commit". With `noCache` (git services only) the build
 * of this one deployment ignores every cache: "Redeploy without cache".
 */
export async function startDeploy(
	serviceId: string,
	commitSha?: string,
	message?: string,
	noCache?: boolean,
): Promise<Deployment> {
	const note = message?.trim();
	const { data } = await deployService({
		path: { serviceId },
		...(commitSha || note || noCache
			? {
					body: {
						...(commitSha ? { commitSha } : {}),
						...(note ? { message: note } : {}),
						...(noCache ? { noCache: true } : {}),
					},
				}
			: {}),
		headers: { "Idempotency-Key": crypto.randomUUID() },
	});
	return toDeployment(data);
}

/** Ask the service's server to restart its container (no new deployment). */
export async function startRestart(serviceId: string): Promise<void> {
	await restartService({
		path: { serviceId },
		headers: { "Idempotency-Key": crypto.randomUUID() },
	});
}

/**
 * Cancel a deployment at any step. Before the agent started it the
 * deployment ends as canceled at once; after, the agent is asked to abort and
 * the deployment ends as canceled when it reports back. A 409 `cannot_cancel`
 * means it already finished: it surfaces as an ApiError.
 */
export async function cancelDeploymentRequest(
	deploymentId: string,
): Promise<void> {
	await cancelDeployment({
		path: { deploymentId },
		headers: { "Idempotency-Key": crypto.randomUUID() },
	});
}
