import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CloudIcon, RefreshCwIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import {
	disableServerTunnel,
	enableServerTunnel,
	resyncServerTunnel,
} from "#/api/gen/sdk.gen";
import type { TunnelView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import { useIsAdmin } from "#/features/proxy/use-admin";
import { invalidateTunnel, serverTunnelQuery } from "../queries";
import {
	connectionsText,
	enableBlocker,
	hostnamesText,
	tunnelChip,
	tunnelNote,
} from "./tunnel-state";

/** The connector's state, its tunnel and the last ingress push, with copy buttons. */
function TunnelStatusRow({ tunnel }: { tunnel: TunnelView }) {
	const chip = tunnelChip(tunnel.state);
	return (
		<section
			aria-label="Tunnel status"
			className="surface-well flex flex-col gap-2 rounded-ctl px-3 py-2.5"
		>
			<div className="flex flex-wrap items-center gap-2">
				<span className="inline-flex items-center gap-1.5 font-medium text-ink-1 text-xs">
					<StatusIcon kind={chip.kind} label="" />
					{chip.label}
				</span>
				<span className="text-ink-2 text-xs">
					{connectionsText(tunnel.connections)}
				</span>
				{tunnel.observedAt ? (
					<span className="text-ink-3 text-xs">
						reported <Relative iso={tunnel.observedAt} />
					</span>
				) : null}
			</div>
			{tunnel.cname ? (
				<div className="flex items-center gap-2">
					<code className="min-w-0 flex-1 truncate font-mono text-ink-1 text-xs">
						{tunnel.cname}
					</code>
					<CopyButton
						value={tunnel.cname}
						label="Copy tunnel CNAME target"
						variant="ghost"
						size="icon-xs"
					/>
				</div>
			) : null}
			<p className="type-mono text-ink-3">
				{tunnel.tunnelName ? `${tunnel.tunnelName} · ` : ""}
				{hostnamesText(tunnel.ingressHosts)} → {tunnel.upstream}
				{tunnel.ingressSyncedAt ? (
					<>
						{" "}
						· synced <Relative iso={tunnel.ingressSyncedAt} />
					</>
				) : null}
			</p>
			{tunnel.ingressError ? (
				<Alert variant="destructive">
					<AlertDescription>{tunnel.ingressError}</AlertDescription>
				</Alert>
			) : null}
			{tunnel.message ? (
				tunnel.state === "needs_attention" || tunnel.state === "removing" ? (
					<Alert variant="destructive">
						<AlertDescription>{tunnel.message}</AlertDescription>
					</Alert>
				) : (
					<p className="text-ink-3 text-xs">{tunnel.message}</p>
				)
			) : null}
		</section>
	);
}

/**
 * The server's Cloudflare Tunnel: a cloudflared connector next to the proxy
 * that lets Cloudflare reach this server without ports 80 and 443. Enabling
 * and disabling act at once (they are not staged): the tunnel is created or
 * deleted at Cloudflare and the domains' DNS follows.
 */
export function TunnelCard({
	serverId,
	serverName,
}: {
	serverId: string;
	serverName: string;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const admin = useIsAdmin();
	const query = useQuery(serverTunnelQuery(serverId));

	const enable = useMutation({
		mutationFn: async () => {
			await enableServerTunnel({
				path: { serverId },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
		},
		onSuccess: async () => {
			await invalidateTunnel(queryClient);
			toast.success(
				"Creating the tunnel",
				"The connector starts on the server; its status updates here.",
			);
		},
	});
	const resync = useMutation({
		mutationFn: async () => {
			await resyncServerTunnel({
				path: { serverId },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
		},
		onSuccess: async () => {
			await invalidateTunnel(queryClient);
			toast.success(
				"Re-syncing",
				"The connector and its ingress rules are being refreshed.",
			);
		},
	});
	const disable = useMutation({
		mutationFn: async (force: boolean) => {
			await disableServerTunnel({ path: { serverId }, query: { force } });
		},
		onSuccess: async (_data, force) => {
			await invalidateTunnel(queryClient);
			toast.success(
				force ? "Tunnel removed" : "Removing the tunnel",
				force
					? "The record is gone. Delete the tunnel in the Cloudflare dashboard if it lingers."
					: "Domains point back at the server; the tunnel is deleted at Cloudflare shortly.",
			);
		},
	});

	const tunnel = query.data;
	const removing = tunnel?.state === "removing";

	const askEnable = () =>
		confirm({
			title: `Enable a Cloudflare Tunnel on ${serverName}?`,
			body: "NebulaCtrl creates a tunnel in your Cloudflare account and runs the cloudflared connector next to the proxy. Once it is connected, domains in your connected zones switch to a proxied CNAME to the tunnel. Cloudflare then terminates the public HTTPS, so no Let's Encrypt HTTP-01 certificate is requested for those domains. Ports 80 and 443 may stay closed.",
			cta: "Enable tunnel",
			hold: false,
			onConfirm: async () => {
				await enable.mutateAsync();
			},
		});
	const askDisable = () =>
		confirm({
			title: removing
				? "Retry removing the tunnel?"
				: `Disable the tunnel on ${serverName}?`,
			body: "Domains point back at this server's public IP and the connector is removed. Open ports 80 and 443 before the DNS change propagates, or the sites go dark. The tunnel is deleted at Cloudflare shortly after.",
			cta: removing ? "Retry" : "Disable tunnel",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await disable.mutateAsync(false);
			},
		});
	const askForce = () =>
		confirm({
			title: "Force-remove the tunnel?",
			body: "The tunnel record is dropped without asking Cloudflare. If the tunnel still exists in your Cloudflare account, delete it there.",
			cta: "Force remove",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await disable.mutateAsync(true);
			},
		});

	return (
		<SkeletonReveal
			loading={query.isPending}
			skeleton={<Skeleton className="h-36 rounded-card" />}
		>
			{() => {
				if (!tunnel) {
					return (
						<ErrorState
							title="The tunnel did not load"
							body="The control plane did not answer, so nothing was changed."
							retry={() => void query.refetch()}
						/>
					);
				}
				const blocker = enableBlocker(tunnel);
				const note = tunnelNote(tunnel);
				return (
					<div className="flex flex-col gap-3">
						<p className="type-secondary">
							Reach this server through Cloudflare. cloudflared connects out, so
							ports 80 and 443 can stay closed.
						</p>

						{tunnel.enabled ? <TunnelStatusRow tunnel={tunnel} /> : null}
						{note ? (
							<p className="text-ink-3 text-xs leading-[1.5]">{note}</p>
						) : null}

						{!tunnel.enabled ? (
							<div className="flex flex-col gap-2">
								{blocker ? (
									<p className="text-ink-3 text-xs">
										{blocker}{" "}
										{!tunnel.cloudflareConnected || !tunnel.accountReady ? (
											<Link
												to="/settings/$section"
												params={{ section: "integrations" }}
												className="text-accent-ink underline"
											>
												Open Team settings → Integrations
											</Link>
										) : null}
									</p>
								) : (
									<p className="text-ink-3 text-xs leading-[1.5]">
										The token of your Cloudflare connection needs the permission
										Account · Cloudflare Tunnel · Edit. Domains in zones you
										have connected move to the tunnel automatically.
									</p>
								)}
								{admin ? (
									<div>
										<Button
											disabled={blocker !== null || enable.isPending}
											onClick={() => void askEnable()}
										>
											{enable.isPending ? (
												<Spinner />
											) : (
												<CloudIcon aria-hidden />
											)}
											Enable tunnel
										</Button>
									</div>
								) : (
									<p className="text-ink-3 text-xs">
										Only an administrator can enable a tunnel.
									</p>
								)}
							</div>
						) : admin ? (
							<div className="flex flex-wrap items-center gap-2">
								{removing ? null : (
									<Button
										variant="secondary"
										disabled={resync.isPending}
										onClick={() => resync.mutate()}
									>
										{resync.isPending ? (
											<Spinner />
										) : (
											<RefreshCwIcon aria-hidden />
										)}
										Re-sync
									</Button>
								)}
								<Button
									variant="danger"
									disabled={disable.isPending}
									onClick={() => void askDisable()}
								>
									{disable.isPending ? <Spinner /> : null}
									{removing ? "Retry removal" : "Disable tunnel"}
								</Button>
								{removing && tunnel.message ? (
									<Button variant="link" onClick={() => void askForce()}>
										Force remove
									</Button>
								) : null}
							</div>
						) : null}

						{enable.isError ? (
							<FieldError>{errorMessage(enable.error)}</FieldError>
						) : null}
						{resync.isError ? (
							<FieldError>{errorMessage(resync.error)}</FieldError>
						) : null}
						{disable.isError ? (
							<FieldError>{errorMessage(disable.error)}</FieldError>
						) : null}
					</div>
				);
			}}
		</SkeletonReveal>
	);
}
