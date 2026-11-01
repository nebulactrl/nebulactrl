import type { Deployment } from "#/api/models";
import { shortSHA256 } from "#/lib/format";
import type { DeployMarker } from "./ui/time-chart";

/** "a1b2c3d" for a commit, "sha256:01234567" for an image, else the id's head. */
function releaseLabel(deployment: Deployment): string {
	const source = deployment.release?.source;
	if (source?.commitSha) return source.commitSha.slice(0, 7);
	if (source?.digest) return shortSHA256(source.digest);
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
		markers.push({ id: deployment.id, at, label: releaseLabel(deployment) });
	}
	return markers;
}
