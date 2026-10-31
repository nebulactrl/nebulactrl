import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { PlusIcon, ServerIcon, WaypointsIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { deleteNetwork } from "#/api/gen/sdk.gen";
import type { NetworkView } from "#/api/gen/types.gen";
import { meQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { CreateNetworkDialog } from "./create-network-dialog";
import { invalidateNetworks, networksQuery } from "./queries";

/** A network's services, one per line with the hostname they answer to. */
function AttachedServices({ network }: { network: NetworkView }) {
	const services = network.services ?? [];
	if (services.length === 0) {
		return <span className="type-secondary">No service attached yet</span>;
	}
	return (
		<ul className="flex flex-col gap-1">
			{services.map((service) => (
				<li
					key={service.serviceId}
					className="flex min-w-0 items-center gap-1.5 text-[13px]"
				>
					<Link
						to="/p/$projectId/$envSlug"
						params={{ projectId: service.projectId, envSlug: service.envSlug }}
						search={{ service: service.serviceId }}
						className="font-medium text-ink-1 hover:underline"
					>
						{service.name}
					</Link>
					<span className="type-secondary">
						{service.projectName} · {service.envName}
					</span>
					<code className="min-w-0 truncate font-mono text-[12px] text-ink-3">
						{service.alias}
					</code>
					<CopyButton
						value={service.alias}
						variant="ghost"
						size="icon-xs"
						label={`Copy ${service.alias}`}
					/>
				</li>
			))}
		</ul>
	);
}

/**
 * Team settings → Private networks: the team's shared networks. A shared network lives on
 * one server; services of any project of the team that run there attach to it
 * from their own settings (a staged change) and reach each other by hostname.
 */
export function NetworksSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const networks = useQuery(networksQuery());
	const me = useQuery(meQuery());
	const [creating, setCreating] = useState(false);
	const canManage =
		me.data?.principal.scopes?.some(
			(scope) => scope === "admin" || scope === "servers:write",
		) ?? false;

	const remove = useMutation({
		mutationFn: async (network: NetworkView) => {
			await deleteNetwork({ path: { networkId: network.id } });
			return network;
		},
		onSuccess: async (network) => {
			await invalidateNetworks(queryClient);
			toast.success(`Deleted ${network.name}`);
		},
		onError: (error) =>
			toast.error("Could not delete the network", errorMessage(error)),
	});

	if (networks.isPending) return <Skeleton className="h-40 rounded-card" />;
	if (networks.isError && !networks.data) {
		return (
			<ErrorState
				frame="raised"
				title="Networks did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => void networks.refetch()}
			/>
		);
	}
	const items = networks.data?.items ?? [];
	const add = canManage ? (
		<Button variant="primary" onClick={() => setCreating(true)}>
			<PlusIcon />
			New network
		</Button>
	) : undefined;

	return (
		<div className="flex flex-col gap-4">
			{items.length === 0 ? (
				<EmptyState
					illustration="network"
					title="No shared networks"
					body="Every environment already has a private network of its own. A shared network lets chosen services of different projects on the same server reach each other by hostname, and nothing else."
					action={add}
				/>
			) : (
				<>
					{add ? <div className="flex justify-end">{add}</div> : null}
					<ul className="flex flex-col gap-3">
						{items.map((network) => (
							<li
								key={network.id}
								className="surface-raised flex flex-col gap-3 rounded-card p-4"
							>
								<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
									<WaypointsIcon aria-hidden className="size-4 text-ink-3" />
									<span className="font-semibold text-ink-1 text-sm">
										{network.name}
									</span>
									<Link
										to="/servers/$serverId"
										params={{ serverId: network.serverId }}
										className="inline-flex items-center gap-1 font-mono text-[12px] text-ink-3 hover:text-ink-1"
									>
										<ServerIcon aria-hidden className="size-3" />
										{network.serverName}
									</Link>
									<span
										title="The network's name on the server"
										className="font-mono text-[11px] text-ink-4"
									>
										{network.dockerName}
									</span>
									{canManage ? (
										<Button
											variant="ghost"
											size="sm"
											className="ml-auto text-destructive-text"
											onClick={() =>
												void confirm({
													title: `Delete ${network.name}?`,
													body:
														(network.services ?? []).length > 0
															? "Services are still attached. Detach them from their settings first; the network can not be deleted while any is."
															: "The network is removed. Nothing else changes.",
													cta: "Delete network",
													danger: true,
													hold: false,
													onConfirm: async () => {
														await remove.mutateAsync(network);
													},
												})
											}
										>
											Delete
										</Button>
									) : null}
								</div>
								<AttachedServices network={network} />
							</li>
						))}
					</ul>
				</>
			)}
			<CreateNetworkDialog open={creating} onOpenChange={setCreating} />
		</div>
	);
}
