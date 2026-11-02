import type { Deployment } from "#/api/models";
import { releaseLabel } from "#/features/deployments/labels";
import type { DeployMarker } from "./ui/time-chart";

/** The release's label ("a1b2c3d", "sha256:01234567"), or the id's head without a source. */
function markerLabel(deployment: Deployment): string {
	const source = deployment.release?.source;
	if (source?.commitSha || source?.digest) return releaseLabel(source);
	return deployment.id.slice(0, 7);
}

/**
 * Chart markers for the deployments that actually started: one dotted line
 * per `startedAt`. A deployment that never started (queued, undelivered)
 * changed nothing a chart could show.
 */
export function deploymentMarkers(
	deployments: readonly Deployment[] | undefined,
): DeployMarker[] {
	const markers: DeployMarker[] = [];
	for (const deployment of deployments ?? []) {
		if (!deployment.startedAt) continue;
		const at = Date.parse(deployment.startedAt);
		if (!Number.isFinite(at)) continue;
		markers.push({ id: deployment.id, at, label: markerLabel(deployment) });
	}
	return markers;
}
