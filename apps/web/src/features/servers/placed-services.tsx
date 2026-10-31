import { Link } from "@tanstack/react-router";
import { EmptyState } from "#/components/common/empty-state";
import { Skeleton } from "#/components/ui/skeleton";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { cardStatus, sourceLabel } from "#/features/canvas/lib/status";
import { useServicesOnServer } from "./use-services-on-server";

/** Services running on this host; each row opens its inspector on the canvas. */
export function PlacedServices({ serverId }: { serverId: string }) {
	const { placed, pending } = useServicesOnServer(serverId);

	if (pending && placed.length === 0) {
		return <Skeleton className="h-24 rounded-card" />;
	}
	if (placed.length === 0) {
		return (
			<EmptyState
				illustration="box"
				title="No services on this server"
				body="Pick this server when you create a service, or move an existing one in its Placement settings."
				frame="raised"
				className="py-10"
			/>
		);
	}
	return (
		<div className="surface-raised overflow-hidden rounded-card">
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>Service</TableHead>
						<TableHead>Where</TableHead>
						<TableHead>Source</TableHead>
						<TableHead>Domains</TableHead>
						<TableHead>Status</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{placed.map(({ service, project, environment }) => {
						const status = cardStatus(service);
						return (
							<TableRow key={service.id}>
								<TableCell pin="start">
									<Link
										to="/p/$projectId/$envSlug"
										params={{
											projectId: project.id,
											envSlug: environment.slug,
										}}
										search={{ service: service.id }}
										className="font-medium text-ink-1 hover:text-accent-ink"
									>
										{service.name}
									</Link>
								</TableCell>
								<TableCell label="Where" className="type-mono text-ink-3">
									{project.name} / {environment.slug}
								</TableCell>
								<TableCell label="Source" className="type-mono text-ink-3">
									{sourceLabel(service)}
								</TableCell>
								<TableCell label="Domains" className="type-mono text-ink-3">
									{service.domains.length > 0
										? service.domains.map((d) => d.hostname).join(", ")
										: "none"}
								</TableCell>
								<TableCell label="Status" pin="end">
									<span
										title={status.detail}
										className="inline-flex items-center gap-1.5 text-xs"
									>
										<StatusIcon kind={status.kind} label="" />
										<span className="truncate text-ink-1">{status.pill}</span>
									</span>
								</TableCell>
							</TableRow>
						);
					})}
				</TableBody>
			</Table>
		</div>
	);
}
