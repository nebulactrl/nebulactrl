import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { WaypointsIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import { attachServiceNetwork, detachServiceNetwork } from "#/api/gen/sdk.gen";
import type { NetworkRef } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { CopyButton } from "#/components/common/copy-button";
import { CheckLabel } from "#/components/ui/check-label";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { attachBlockedReason } from "./model";
import { invalidateNetworks, networksQuery } from "./queries";

/** One network's row: a checkbox, the name, and (while attached) the hostname to copy. */
function NetworkRow({
	name,
	attached,
	pending,
	onToggle,
}: {
	name: string;
	attached: NetworkRef | undefined;
	pending: boolean;
	onToggle(on: boolean): void;
}) {
	return (
		<li className="flex min-h-9 items-center gap-3 py-1">
			<CheckLabel
				checked={attached !== undefined}
				disabled={pending}
				onCheckedChange={onToggle}
				className="min-w-0 flex-1"
				labelClassName="flex min-w-0 items-center gap-2 text-ink-1 text-sm"
			>
				<WaypointsIcon aria-hidden className="size-3.5 shrink-0 text-ink-3" />
				<span className="truncate font-medium">{name}</span>
			</CheckLabel>
			{attached ? (
				<span className="flex min-w-0 items-center gap-1">
					<code
						title={`Other services on ${name} reach this service as ${attached.alias}`}
						className="min-w-0 truncate font-mono text-[12px] text-ink-2"
					>
						{attached.alias}
					</code>
					<CopyButton
						value={attached.alias}
						variant="ghost"
						size="icon-xs"
						label={`Copy ${attached.alias}`}
					/>
				</span>
			) : null}
		</li>
	);
}

/**
 * The shared networks a service can join: every network on its server as a
 * checkbox. Attaching is staged like volumes and domains (it takes effect on
 * the next deploy); the hostname other services use is shown for copying.
 */
export function ServiceNetworks({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const blocked = attachBlockedReason(service);
	const networks = useQuery({
		...networksQuery(service.serverId ?? undefined),
		enabled: service.serverId !== null && service.kind !== "route",
	});
	const toggle = useMutation({
		mutationFn: async (vars: {
			networkId: string;
			name: string;
			on: boolean;
		}) => {
			const path = { serviceId: service.id, networkId: vars.networkId };
			if (vars.on) await attachServiceNetwork({ path });
			else await detachServiceNetwork({ path });
			return vars;
		},
		onSuccess: async (vars) => {
			await invalidateNetworks(queryClient);
			toast.success(
				vars.on ? `Attached to ${vars.name}` : `Detached from ${vars.name}`,
				"Staged until the next deploy.",
			);
		},
		onError: (error) =>
			toast.error("Could not change the shared network", errorMessage(error)),
	});

	const attachedById = new Map(service.networks.map((n) => [n.id, n]));
	const onServer = networks.data?.items ?? [];
	const pendingId = toggle.isPending ? toggle.variables.networkId : null;
	const serverName = service.server?.name ?? "this server";

	return (
		<div className="flex flex-col gap-2">
			<div className="flex flex-col gap-0.5">
				<span className="font-medium text-ink-1 text-sm">Shared networks</span>
				<p className="type-secondary">
					Reach services of other projects on {serverName} by hostname. A shared
					network spans one server and one team.{" "}
					<Link
						to="/settings/$section"
						params={{ section: "private-networks" }}
						className="text-accent-ink underline"
					>
						Manage private networks
					</Link>
				</p>
			</div>
			{blocked ? (
				<p className="type-secondary" role="note">
					{blocked}
				</p>
			) : networks.isPending ? (
				<Skeleton className="h-9 rounded-ctl" />
			) : onServer.length === 0 ? (
				<p className="type-secondary">
					No shared network on {serverName} yet. Create one in Team settings →
					Private networks.
				</p>
			) : (
				<ul className="flex flex-col">
					{onServer.map((network) => (
						<NetworkRow
							key={network.id}
							name={network.name}
							attached={attachedById.get(network.id)}
							pending={pendingId === network.id}
							onToggle={(on) =>
								toggle.mutate({ networkId: network.id, name: network.name, on })
							}
						/>
					))}
				</ul>
			)}
		</div>
	);
}
