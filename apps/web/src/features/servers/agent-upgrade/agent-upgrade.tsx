import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircleIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import {
	updateInstanceUpgrade,
	upgradeServerAgent,
	upgradeServerAgents,
} from "#/api/gen/sdk.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "#/components/ui/card";
import { KvRow } from "#/components/ui/kv-row";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import {
	behindSummary,
	rolloutSummary,
	SKEW_KIND,
	serverUpgrade,
	skewLabel,
	upgradePresentation,
	upgradeRunning,
} from "./model";
import { instanceUpgradeQuery, invalidateAgentUpgrade } from "./queries";

/** How one agent's version compares with the control plane's, and its upgrade if one runs. */
export function AgentSkew({ serverId }: { serverId: string }) {
	const { data } = useQuery({
		...instanceUpgradeQuery(),
		select: (v) => ({
			version: v.controlPlaneVersion,
			server: serverUpgrade(v, serverId),
		}),
	});
	const s = data?.server;
	if (!s || !data) return null;
	const running = upgradePresentation(s);
	if (upgradeRunning(s) && running) {
		return (
			<Badge variant="outline" shape="capsule" className="gap-1.5">
				<StatusIcon kind={running.kind} label="" />
				{running.label}
			</Badge>
		);
	}
	if (s.skew === "current") return null;
	return (
		<Badge variant="outline" shape="capsule" className="gap-1.5">
			<StatusIcon kind={SKEW_KIND[s.skew]} label="" />
			{skewLabel(s.skew, data.version)}
		</Badge>
	);
}

/**
 * The bar above the server list: which version the control plane runs, how many
 * agents are behind, the automatic upgrade switch and the staged "Upgrade agents"
 * action.
 */
export function AgentUpgradeBar() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const { data } = useQuery(instanceUpgradeQuery());
	const setAuto = useMutation({
		mutationFn: (agentAutoUpgrade: boolean) =>
			updateInstanceUpgrade({ body: { agentAutoUpgrade }, throwOnError: true }),
		onSuccess: () => invalidateAgentUpgrade(queryClient),
		onError: (e) =>
			toast.error("Could not change the setting", errorMessage(e)),
	});
	const rollout = useMutation({
		mutationFn: () => upgradeServerAgents({ body: {}, throwOnError: true }),
		onSuccess: async () => {
			toast.success(
				"Upgrading agents",
				"One server at a time; each one restarts its agent and reconnects.",
			);
			await invalidateAgentUpgrade(queryClient);
		},
		onError: (e) => toast.error("Could not start the upgrade", errorMessage(e)),
	});
	const servers = data?.servers ?? [];
	if (!data || servers.length === 0) return null;

	const summary = behindSummary(data);
	const failed = servers.filter((s) => s.state === "failed");
	const running = servers.some(upgradeRunning);
	const start = async () => {
		const ok = await confirm({
			title: `Upgrade ${data.upgradable === 1 ? "1 agent" : `${data.upgradable} agents`} to ${data.controlPlaneVersion}?`,
			body: rolloutSummary(data),
			cta: "Upgrade agents",
		});
		if (ok) rollout.mutate();
	};

	return (
		<section
			aria-label="Agent versions"
			className="surface-raised flex flex-wrap items-center justify-between gap-x-6 gap-y-3 rounded-card px-4 py-3"
		>
			<div className="flex min-w-0 flex-col gap-1">
				<span className="flex items-center gap-2 text-ink-1 text-sm">
					<StatusIcon
						kind={running ? "deploying" : summary ? "warning" : "healthy"}
						label=""
					/>
					{running
						? "Upgrading agents"
						: (summary ?? `All agents are on ${data.controlPlaneVersion}`)}
				</span>
				<span className="text-ink-3 text-xs">
					Control plane {data.controlPlaneVersion}.{" "}
					{failed.length > 0
						? `${failed.length} ${failed.length === 1 ? "upgrade" : "upgrades"} failed: open the server for the reason.`
						: "An agent replaces its own binary, checks it first and rolls back if the new one does not start."}
				</span>
			</div>
			<div className="flex items-center gap-4">
				<label
					htmlFor="agent-auto-upgrade"
					className="flex cursor-pointer items-center gap-2 text-ink-2 text-xs"
				>
					<Switch
						id="agent-auto-upgrade"
						size="sm"
						checked={data.agentAutoUpgrade}
						disabled={setAuto.isPending}
						onCheckedChange={(on) => setAuto.mutate(on)}
					/>
					Upgrade automatically
				</label>
				<Button
					variant="primary"
					disabled={data.upgradable === 0 || running || rollout.isPending}
					onClick={() => void start()}
				>
					<ArrowUpCircleIcon />
					Upgrade agents{data.upgradable > 0 ? ` (${data.upgradable})` : ""}
				</Button>
			</div>
		</section>
	);
}

/** The agent's version, skew and latest upgrade on the server's page, with its own upgrade button. */
export function AgentUpgradeCard({ serverId }: { serverId: string }) {
	const queryClient = useQueryClient();
	const { data } = useQuery(instanceUpgradeQuery());
	const s = serverUpgrade(data, serverId);
	const upgrade = useMutation({
		mutationFn: (force: boolean) =>
			upgradeServerAgent({
				path: { serverId },
				body: { force },
				throwOnError: true,
			}),
		onSuccess: async () => {
			toast.success("Upgrade queued", "The agent restarts when it is done.");
			await invalidateAgentUpgrade(queryClient);
		},
		onError: (e) => toast.error("Could not start the upgrade", errorMessage(e)),
	});
	if (!data || !s) return null;
	const last = upgradePresentation(s);
	const running = upgradeRunning(s);
	return (
		<Card>
			<CardHeader>
				<CardTitle>Agent version</CardTitle>
			</CardHeader>
			<CardContent className="flex flex-col gap-3">
				<div className="flex flex-col">
					<KvRow label="Agent">{s.agentVersion || "not connected"}</KvRow>
					<KvRow label="Control plane">{data.controlPlaneVersion}</KvRow>
					<KvRow label="Status">
						<span className="flex items-center gap-2">
							<StatusIcon kind={SKEW_KIND[s.skew]} label="" />
							{skewLabel(s.skew, data.controlPlaneVersion)}
						</span>
					</KvRow>
					{last ? (
						<KvRow label="Last upgrade">
							<span className="flex flex-col gap-0.5">
								<span className="flex items-center gap-2">
									<StatusIcon kind={last.kind} label="" />
									{last.label}
									{s.finishedAt ? (
										<span className="text-ink-4">
											· <Relative iso={s.finishedAt} />
										</span>
									) : null}
								</span>
								{s.message ? (
									<span
										className={
											s.state === "failed"
												? "break-words text-destructive-text text-xs"
												: "text-ink-3 text-xs"
										}
									>
										{s.message}
									</span>
								) : null}
							</span>
						</KvRow>
					) : null}
				</div>
				{s.upgradable && !running ? (
					<div className="flex items-center gap-3">
						<Button
							variant="primary"
							disabled={upgrade.isPending}
							onClick={() => upgrade.mutate(false)}
						>
							<ArrowUpCircleIcon />
							Upgrade agent to {data.controlPlaneVersion}
						</Button>
						{s.state === "failed" ? (
							<Button
								variant="default"
								disabled={upgrade.isPending}
								title="Install the version again although the agent rolled it back before"
								onClick={() => upgrade.mutate(true)}
							>
								Retry anyway
							</Button>
						) : null}
					</div>
				) : s.reason && s.skew !== "current" ? (
					<p className="text-ink-3 text-xs">{s.reason}</p>
				) : null}
			</CardContent>
		</Card>
	);
}
