import { BackupsPanel } from "#/features/backups/backups-panel";
import { ReplicaBackupsNote } from "#/features/databases/replicas/backups-note";
import { useReplication } from "#/features/databases/replicas/queries";
import { VolumeBackupsPanel } from "#/features/volume-backups/volume-backups-panel";
import type { ServiceTabProps } from "./types";

/**
 * Backups of a service: database dumps for a managed database, file backups of
 * the volumes for any other service that has one (see `tabsFor`). The database
 * panel itself handles engines without backup support.
 */
export function BackupsTab({ service }: ServiceTabProps) {
	const engine = service.config.database?.engine;
	const volumes = service.kind !== "database" && service.volumes.length > 0;
	// A read replica takes no backups: they run on the primary.
	const replication = useReplication(service.id, engine === "postgres");
	if (!engine && !volumes) return null;
	return (
		<section
			data-tab="backups"
			data-service-id={service.id}
			aria-label="Backups"
			className="flex min-h-full flex-col p-4"
		>
			{replication.data?.role === "replica" ? (
				<ReplicaBackupsNote primary={replication.data.primary} />
			) : engine ? (
				<BackupsPanel
					serviceId={service.id}
					serviceSlug={service.slug}
					engine={engine}
					serverId={service.serverId}
				/>
			) : (
				<VolumeBackupsPanel
					serviceId={service.id}
					serviceSlug={service.slug}
					stagedCount={
						service.pendingChangeCounts.variables +
						service.pendingChangeCounts.settings
					}
				/>
			)}
		</section>
	);
}
