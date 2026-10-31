import type { Service } from "#/api/models";

/**
 * The word for deploying a service, wherever the canvas offers it: the dock,
 * the card's menu, the palette. A route has no release to repeat and a
 * service that never ran has nothing to run again, so both "Deploy"; every
 * other service is "Redeploy", as the inspector's header calls it.
 */
export function deployLabel(
	service: Pick<Service, "kind" | "currentDeploymentId">,
): "Deploy" | "Redeploy" {
	return service.kind === "route" || service.currentDeploymentId === null
		? "Deploy"
		: "Redeploy";
}
