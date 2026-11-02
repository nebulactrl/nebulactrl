import { queryOptions } from "@tanstack/react-query";
import {
	getDeploymentReleaseOptions,
	listDeploymentCommandsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type * as Gen from "#/api/gen/types.gen";

/** The release of a deployment as the debug panel shows it: never a variable value. */
export interface DebugRelease extends Omit<Gen.ReleaseDetailView, "variables"> {
	variables: Gen.ReleaseVariableView[];
}

export type DebugCommand = Gen.CommandView;

/** The wire release with Go's nil slice (the schema allows null) spelled out as empty. */
export function toDebugRelease(raw: Gen.ReleaseDetailView): DebugRelease {
	return { ...raw, variables: raw.variables ?? [] };
}

export function toDebugCommands(list: Gen.CommandsOutputBody): DebugCommand[] {
	return list.items ?? [];
}

/** Fast while the deployment runs (the events feed also invalidates), slow once it ended. */
function pollMs(running: boolean): number | false {
	return running ? 3_000 : false;
}

export function deploymentReleaseQuery(deploymentId: string, running: boolean) {
	return queryOptions({
		...getDeploymentReleaseOptions({ path: { deploymentId } }),
		refetchInterval: pollMs(running),
		// A deployment that has no release yet answers 404: not worth retrying.
		retry: false,
	});
}

export function deploymentCommandsQuery(
	deploymentId: string,
	running: boolean,
) {
	return queryOptions({
		...listDeploymentCommandsOptions({ path: { deploymentId } }),
		refetchInterval: pollMs(running),
	});
}
