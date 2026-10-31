import type { DeploymentStatus } from "#/api/models";
import type { StatusKind } from "#/components/ui/status-icon";

/** The status icon of a deployment's status; the label always accompanies it. */
export const STATUS_KIND: Record<DeploymentStatus, StatusKind> = {
	created: "pending",
	queued: "pending",
	preparing: "building",
	building: "building",
	releasing: "deploying",
	verifying: "verifying",
	succeeded: "live",
	failed: "failed",
	canceled: "stopped",
	rolled_back: "degraded",
	rollback_failed: "failed",
};

/**
 * Who started a deployment, with what kind of principal when it is not a
 * person: "token · gate", "mcp · claude". A person is just their name.
 */
export function actorText(type: string, label: string): string {
	if (type === "user" || type === "") return label;
	return label === "" ? type : `${type} · ${label}`;
}

export type StatusFilter =
	| "all"
	| "succeeded"
	| "failed"
	| "in_progress"
	| "rolled_back"
	| "canceled";

/**
 * The status filter's choices. `statuses` is the comma-separated list the
 * control plane's `status` query takes; a group sends several.
 */
export const STATUS_FILTERS: ReadonlyArray<{
	value: StatusFilter;
	label: string;
	statuses: string | undefined;
}> = [
	{ value: "all", label: "All statuses", statuses: undefined },
	{ value: "succeeded", label: "Succeeded", statuses: "succeeded" },
	{ value: "failed", label: "Failed", statuses: "failed,rollback_failed" },
	{
		value: "in_progress",
		label: "In progress",
		statuses: "created,queued,preparing,building,releasing,verifying",
	},
	{ value: "rolled_back", label: "Rolled back", statuses: "rolled_back" },
	{ value: "canceled", label: "Canceled", statuses: "canceled" },
];

export function isStatusFilter(value: unknown): value is StatusFilter {
	return STATUS_FILTERS.some((filter) => filter.value === value);
}
