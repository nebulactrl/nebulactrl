import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { RefreshCwIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	reprovisionServerTailnet,
	updateServerProxySettings,
} from "#/api/gen/sdk.gen";
import type { ProxyTailnet, TailnetStruct } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { statusKindOfTone } from "#/components/ui/lamp";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { isAgentTooOld } from "#/features/proxy/lib/errors";
import {
	MAGIC_DNS_CAVEAT,
	modeBlocker,
	TAILNET_MODES,
	type TailnetMode,
	tailnetChip,
	validateTailnetHostname,
} from "#/features/proxy/lib/tailnet";
import { managedEgressBlocker } from "#/features/proxy/lib/tailscale";
import { invalidateProxy, tailscaleQuery } from "#/features/proxy/queries";
import { AgentTooOldBanner } from "#/features/proxy/ui/agent-too-old";
import { useIsAdmin } from "#/features/proxy/use-admin";

/** Where the node's name, addresses and last report appear, with copy buttons. */
function TailnetStatusRow({ tailnet }: { tailnet: ProxyTailnet }) {
	const status: {
		state: string;
		fqdn?: string | undefined;
		ips?: string[] | null | undefined;
		message?: string | undefined;
	} = tailnet.status ?? { state: "off" };
	const chip = tailnetChip(status.state, tailnet.appliedMode);
	const ips = status.ips ?? [];
	return (
		<section
			aria-label="Tailnet status"
			className="surface-well flex flex-col gap-2 rounded-ctl px-3 py-2.5"
		>
			<div className="flex flex-wrap items-center gap-2">
				<span className="inline-flex items-center gap-1.5 font-medium text-ink-1 text-xs">
					<StatusIcon kind={statusKindOfTone(chip.tone)} label="" />
					{chip.label}
				</span>
				{tailnet.statusAt ? (
					<span className="text-ink-3 text-xs">
						reported <Relative iso={tailnet.statusAt} />
					</span>
				) : null}
			</div>
			{status.fqdn ? (
				<div className="flex items-center gap-2">
					<code className="min-w-0 flex-1 truncate font-mono text-ink-1 text-xs">
						{status.fqdn.replace(/\.$/, "")}
					</code>
					<CopyButton
						value={status.fqdn.replace(/\.$/, "")}
						label="Copy tailnet name"
						variant="ghost"
						size="icon-xs"
					/>
				</div>
			) : null}
			{ips.length > 0 ? (
				<div className="flex items-center gap-2">
					<code className="min-w-0 flex-1 truncate font-mono text-ink-2 text-xs">
						{ips.join("  ")}
					</code>
					<CopyButton
						value={ips.join(" ")}
						label="Copy tailnet addresses"
						variant="ghost"
						size="icon-xs"
					/>
				</div>
			) : null}
			{status.message ? (
				<p
					className={
						status.state === "needs_attention"
							? "text-destructive-text text-xs"
							: "text-ink-3 text-xs"
					}
				>
					{status.message}
				</p>
			) : null}
		</section>
	);
}

/**
 * The server's tailnet egress: off, a managed Tailscale node beside the proxy,
 * or the Tailscale already on the host. Changes are staged until the proxy is
 * deployed; re-provisioning deploys at once.
 */
export function TailnetCard({
	serverId,
	serverName,
	tailnet,
	agentTooOld,
}: {
	serverId: string;
	serverName: string;
	tailnet: ProxyTailnet;
	agentTooOld: boolean;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [hostname, setHostname] = useState(tailnet.hostname);
	useEffect(() => setHostname(tailnet.hostname), [tailnet.hostname]);
	const hostnameError = validateTailnetHostname(hostname);
	const hostnameDirty = hostname !== tailnet.hostname;

	const update = useMutation({
		mutationFn: async (patch: TailnetStruct) => {
			await updateServerProxySettings({
				path: { serverId },
				body: { tailnet: patch },
			});
		},
		onSuccess: async () => {
			await invalidateProxy(queryClient);
			toast.success(
				"Tailnet change staged",
				"It applies on the next Deploy proxy.",
			);
		},
	});

	const reprovision = useMutation({
		mutationFn: async () => {
			await reprovisionServerTailnet({
				path: { serverId },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
		},
		onSuccess: async () => {
			await invalidateProxy(queryClient);
			toast.success(
				"Re-provisioning",
				"A fresh node is being registered; status updates here.",
			);
		},
	});

	const askReprovision = () =>
		confirm({
			title: "Re-provision the tailnet node?",
			body: `The current node is deleted from your tailnet and ${serverName} registers again with a new key. Routes to tailnet devices drop for a few seconds.`,
			cta: "Re-provision",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await reprovision.mutateAsync();
			},
		});

	const isAdmin = useIsAdmin();
	const integration = useQuery({
		...tailscaleQuery(),
		enabled: isAdmin,
		retry: false,
	});
	const egressBlocker = managedEgressBlocker(
		integration.data?.configured ? integration.data.provider : undefined,
	);

	const blockers: Record<TailnetMode, string | null> = {
		off: null,
		managed: modeBlocker("managed", tailnet) ?? egressBlocker,
		host: modeBlocker("host", tailnet),
	};
	const stagedMode = tailnet.mode !== tailnet.appliedMode;
	const modeHint = TAILNET_MODES.find((m) => m.id === tailnet.mode)?.hint;
	const needsIntegration =
		tailnet.mode === "managed" && !tailnet.integrationConfigured;

	return (
		<div className="flex flex-col gap-3">
			<div className="flex items-center gap-3">
				<p className="type-secondary min-w-0 flex-1">
					Lets routes on this server forward to devices on your Tailscale
					network.
				</p>
				{stagedMode ? <Badge tone="staged">Staged</Badge> : null}
			</div>

			{agentTooOld ? <AgentTooOldBanner serverName={serverName} /> : null}

			<Segmented
				aria-label="Tailnet mode"
				font="sans"
				density="regular"
				value={tailnet.mode}
				onValueChange={(mode) => update.mutate({ mode })}
				options={TAILNET_MODES.map((mode) => ({
					value: mode.id,
					label: mode.label,
					disabled:
						agentTooOld ||
						update.isPending ||
						(blockers[mode.id] !== null && tailnet.mode !== mode.id),
				}))}
			/>
			{(["managed", "host"] as const).map((mode) =>
				blockers[mode] && tailnet.mode !== mode ? (
					<p key={mode} className="text-ink-2 text-xs">
						<span className="font-medium text-ink-1">
							{TAILNET_MODES.find((m) => m.id === mode)?.label} is unavailable:
						</span>{" "}
						{mode === "managed" && egressBlocker === null ? (
							<>
								Tailscale is not connected.{" "}
								<Link
									to="/settings/$section"
									params={{ section: "integrations" }}
									className="text-accent-ink underline"
								>
									Connect it in Team settings → Integrations
								</Link>
								.
							</>
						) : (
							blockers[mode]
						)}
					</p>
				) : null,
			)}
			{modeHint ? <p className="text-ink-3 text-xs">{modeHint}</p> : null}

			{needsIntegration ? (
				<Alert variant="destructive">
					<AlertDescription>
						Managed mode needs the Tailscale integration, which is not
						configured.{" "}
						<Link
							to="/settings/$section"
							params={{ section: "integrations" }}
							className="text-accent-ink underline"
						>
							Open Team settings → Integrations
						</Link>
					</AlertDescription>
				</Alert>
			) : null}

			{tailnet.mode === "managed" && egressBlocker ? (
				<Alert variant="destructive">
					<AlertDescription>{egressBlocker}</AlertDescription>
				</Alert>
			) : null}

			{tailnet.mode === "host" ? (
				<p className="text-xs">
					{tailnet.hostTailscale.detected ? (
						<span className="text-ok">
							Host Tailscale detected
							{tailnet.hostTailscale.ips?.length
								? ` · ${tailnet.hostTailscale.ips.join(", ")}`
								: ""}
							. Routes can target its 100.x addresses by IP.
						</span>
					) : (
						<span className="text-destructive-text">
							No Tailscale was detected on this server.
						</span>
					)}
				</p>
			) : null}

			{tailnet.mode === "managed" ? (
				<div className="flex flex-col gap-3 border-line border-t pt-3">
					<div className="flex items-start gap-2">
						<div className="min-w-0 flex-1">
							<label
								htmlFor="tailnet-hostname"
								className="font-medium text-ink-1 text-sm"
							>
								Machine name
							</label>
							<Input
								id="tailnet-hostname"
								autoComplete="off"
								spellCheck={false}
								className="mt-1.5 max-w-sm font-mono"
								aria-invalid={hostnameError !== null}
								value={hostname}
								onChange={(event) => setHostname(event.target.value)}
							/>
							<FieldError className="mt-1">{hostnameError}</FieldError>
						</div>
						<Button
							variant="secondary"
							className="mt-[22px]"
							disabled={
								!hostnameDirty ||
								hostnameError !== null ||
								update.isPending ||
								agentTooOld
							}
							onClick={() => update.mutate({ hostname: hostname.trim() })}
						>
							Save name
						</Button>
					</div>
					<div className="flex items-start gap-3">
						<div className="min-w-0 flex-1">
							<label
								htmlFor="tailnet-magicdns"
								className="font-medium text-ink-1 text-sm"
							>
								MagicDNS names
							</label>
							<p className="mt-1 text-ink-3 text-xs leading-[1.5]">
								{MAGIC_DNS_CAVEAT}
							</p>
						</div>
						<Switch
							id="tailnet-magicdns"
							checked={tailnet.magicDns}
							disabled={update.isPending || agentTooOld}
							onCheckedChange={(magicDns) => update.mutate({ magicDns })}
						/>
					</div>
				</div>
			) : null}

			{tailnet.appliedMode !== "off" || tailnet.status?.state ? (
				<TailnetStatusRow tailnet={tailnet} />
			) : null}

			{tailnet.appliedMode === "managed" ? (
				<div>
					<Button
						variant="secondary"
						disabled={reprovision.isPending || agentTooOld}
						onClick={() => void askReprovision()}
					>
						{reprovision.isPending ? (
							<Spinner />
						) : (
							<RefreshCwIcon aria-hidden />
						)}
						Re-provision
					</Button>
				</div>
			) : null}

			{isAgentTooOld(update.error) ? null : update.isError ? (
				<FieldError>{errorMessage(update.error)}</FieldError>
			) : null}
			{reprovision.isError ? (
				<FieldError>{errorMessage(reprovision.error)}</FieldError>
			) : null}
		</div>
	);
}
