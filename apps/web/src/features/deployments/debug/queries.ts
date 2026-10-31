import { queryOptions } from "@tanstack/react-query";
import {
	getDeploymentReleaseOptions,
	listDeploymentCommandsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type * as Gen from "#/api/gen/types.gen";

/** A release's placement; the ids are null when nothing was built or placed. */
export interface DebugPlacement {
	strategy: string;
	targetServerId: string | null;
	targetServerName: string;
	buildServerId: string | null;
	buildServerName: string;
	registryId: string | null;
	registryRef: string;
	platform: string;
}

/** The release of a deployment as the debug panel shows it: never a variable value. */
export interface DebugRelease
	extends Omit<Gen.ReleaseDetailView, "build" | "variables" | "placement"> {
	build: Gen.BuildInfo | null;
	variables: Gen.ReleaseVariableView[];
	placement: DebugPlacement;
}

export type DebugCommand = Gen.CommandView;

/**
 * The wire release with Go's nulls spelled out: a pointer or slice the schema
 * renders as non-null may arrive as null.
 */
export function toDebugRelease(raw: Gen.ReleaseDetailView): DebugRelease {
	const release = raw as Gen.ReleaseDetailView & {
		build: Gen.BuildInfo | null;
	};
	return {
		...release,
		build: release.build ?? null,
		variables: release.variables ?? [],
		placement: {
			...release.placement,
			targetServerId: release.placement.targetServerId || null,
			buildServerId: release.placement.buildServerId || null,
			registryId: release.placement.registryId || null,
		},
	};
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
