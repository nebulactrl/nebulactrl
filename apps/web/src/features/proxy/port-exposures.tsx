import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PlusIcon, Trash2Icon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import { deleteRoute } from "#/api/gen/sdk.gen";
import { servicesQuery } from "#/api/hooks";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { toast } from "#/components/ui/toast";
import { routeCreate } from "./create-store";
import { exposuresOf } from "./lib/l4";
import { invalidateRoutes } from "./queries";

/**
 * "Expose a port (TCP/UDP)": the route services that publish a port of this
 * service, with a way to add one and to remove one. Removing an exposure
 * deletes its route service; the port closes on the next deploy.
 */
export function PortExposures({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const siblings = useQuery({
		...servicesQuery(service.environmentId),
		enabled: service.kind !== "route",
	});
	const exposures = exposuresOf(service.id, siblings.data ?? []);

	const remove = useMutation({
		mutationFn: async (routeId: string) => {
			await deleteRoute({ path: { routeId } });
		},
		onSuccess: async () => {
			await invalidateRoutes(queryClient);
			toast.success(
				"Port exposure removed",
				"The port closes on the next deploy.",
			);
		},
		onError: (error) => toast.error("Could not remove it", errorMessage(error)),
	});

	if (service.kind === "route") return null;
	const placed = service.serverId !== null;

	return (
		<div className="flex flex-col gap-3" data-section="port-exposures">
			<div className="flex items-center gap-2">
				<span className="font-medium text-ink-1 text-sm">
					Expose a port (TCP/UDP)
				</span>
				<Button
					variant="secondary"
					size="xs"
					className="ml-auto"
					disabled={!placed}
					title={placed ? undefined : "Place the service on a server first"}
					onClick={() => {
						const port = service.config.port;
						routeCreate.open({
							mode: "port",
							target: {
								serviceId: service.id,
								...(port !== undefined && port > 0 ? { port } : {}),
							},
						});
					}}
				>
					<PlusIcon aria-hidden />
					Expose a port
				</Button>
			</div>
			{exposures.length === 0 ? (
				<p className="type-secondary">
					Nothing exposes a port of this service. Use it for game servers, SMTP
					or a database that a trusted network needs to reach directly.
				</p>
			) : (
				<ul aria-label="Port exposures" className="flex flex-col">
					{exposures.map((exposure) => (
						<li
							key={exposure.route.id}
							className="flex items-center gap-3 border-line border-b py-2.5 last:border-0"
						>
							<Badge shape="tag">{exposure.protocol.toUpperCase()}</Badge>
							<span className="font-mono text-[13px] text-ink-1 tabular-nums">
								:{exposure.listenPort}
								<span className="text-ink-3"> → :{exposure.targetPort}</span>
							</span>
							<span className="min-w-0 flex-1 truncate text-ink-3 text-xs">
								{exposure.access}
							</span>
							{exposure.route.hasStagedChanges ? (
								<Badge tone="staged" lamp>
									Staged
								</Badge>
							) : null}
							<Button
								variant="ghost"
								size="icon-sm"
								aria-label={`Remove ${exposure.protocol} port ${exposure.listenPort}`}
								disabled={remove.isPending}
								onClick={() =>
									void confirm({
										title: `Stop exposing ${exposure.protocol.toUpperCase()} ${exposure.listenPort}?`,
										body: `${exposure.route.name} is deleted. The port closes on the next deploy.`,
										cta: "Remove exposure",
										glyph: "trash",
										danger: true,
										hold: false,
										onConfirm: () => remove.mutateAsync(exposure.route.id),
									})
								}
							>
								<Trash2Icon aria-hidden />
							</Button>
						</li>
					))}
				</ul>
			)}
		</div>
	);
}
