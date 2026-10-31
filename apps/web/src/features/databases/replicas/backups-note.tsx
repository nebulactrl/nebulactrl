import { Link, useParams } from "@tanstack/react-router";
import type { ServiceRef } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";

/**
 * What the Backups tab of a read replica says instead of its panel: backups
 * run on the current primary only (a dump taken on a standby can be cancelled
 * by the replication itself), and a promote moves the schedules.
 */
export function ReplicaBackupsNote({
	primary,
}: {
	primary: ServiceRef | null | undefined;
}) {
	const params = useParams({ strict: false });
	return (
		<Alert variant="info">
			<AlertTitle>Backups run on the primary</AlertTitle>
			<AlertDescription>
				This is a read replica. Schedules and backups belong to{" "}
				{primary && params.projectId && params.envSlug ? (
					<Link
						to="/p/$projectId/$envSlug"
						params={{ projectId: params.projectId, envSlug: params.envSlug }}
						search={{ service: primary.id, tab: "backups" }}
						className="text-accent-ink underline"
					>
						{primary.slug}
					</Link>
				) : (
					(primary?.slug ?? "the primary")
				)}
				. If this replica is promoted, the schedules move with the primary role.
			</AlertDescription>
		</Alert>
	);
}
