import { useMutation, useQueryClient } from "@tanstack/react-query";
import { LockIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import { updateServerProxySettings } from "#/api/gen/sdk.gen";
import type {
	ProxyUpstreamPolicy,
	UpstreamPolicyStruct,
} from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { FieldError } from "#/components/ui/field";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { isAgentTooOld } from "#/features/proxy/lib/errors";
import { invalidateProxy } from "#/features/proxy/queries";
import { AgentTooOldBanner } from "#/features/proxy/ui/agent-too-old";
import { useIsAdmin } from "#/features/proxy/use-admin";

type PolicyKey = keyof ProxyUpstreamPolicy;

interface PolicyRow {
	key: PolicyKey;
	label: string;
	description: string;
	/** Red caution under the row; shown while the switch is on. */
	warning?: string;
	adminOnly: boolean;
}

const ROWS: readonly PolicyRow[] = [
	{
		key: "allowHostGateway",
		label: "Reach services on the host",
		description:
			"Routes may target host.docker.internal, the Docker host's own services (a database, a dev server).",
		adminOnly: false,
	},
	{
		key: "allowLoopback",
		label: "Allow loopback upstreams",
		description: "Routes may target 127.0.0.1 and localhost.",
		warning:
			"Loopback means the proxy container itself, including its admin API. Only turn this on for a trusted team.",
		adminOnly: true,
	},
	{
		key: "allowLinkLocal",
		label: "Allow link-local upstreams",
		description: "Routes may target 169.254.0.0/16 and metadata names.",
		warning:
			"This includes the cloud metadata service (169.254.169.254), which can hand out instance credentials. Anyone who can create a route could read them.",
		adminOnly: true,
	},
];

/**
 * What an external route may point at. Defaults deny loopback and link-local;
 * the two risky toggles are for administrators. Changes are staged until the
 * proxy is deployed.
 */
export function PolicyCard({
	serverId,
	desired,
	applied,
	agentTooOld,
}: {
	serverId: string;
	desired: ProxyUpstreamPolicy;
	applied: ProxyUpstreamPolicy;
	agentTooOld: boolean;
}) {
	const queryClient = useQueryClient();
	const admin = useIsAdmin();

	const update = useMutation({
		mutationFn: async (patch: UpstreamPolicyStruct) => {
			await updateServerProxySettings({
				path: { serverId },
				body: { upstreamPolicy: patch },
			});
		},
		onSuccess: async () => {
			await invalidateProxy(queryClient);
			toast.success("Policy staged", "It applies on the next Deploy proxy.");
		},
	});

	return (
		<div className="flex flex-col gap-1">
			<p className="type-secondary">
				Which addresses an external route on this server may forward to. Checked
				when a route is saved and again by the agent.
			</p>
			{isAgentTooOld(update.error) ? (
				<AgentTooOldBanner />
			) : update.isError ? (
				<FieldError>{errorMessage(update.error)}</FieldError>
			) : null}
			<ul aria-label="Upstream policy" className="rule-rows mt-1">
				{ROWS.map((row) => {
					const on = desired[row.key];
					const locked = row.adminOnly && !admin;
					const staged = desired[row.key] !== applied[row.key];
					const id = `policy-${row.key}`;
					return (
						<li key={row.key} className="flex items-start gap-3">
							<div className="min-w-0 flex-1">
								<label
									htmlFor={id}
									className="flex flex-wrap items-center gap-2 font-medium text-ink-1 text-sm"
								>
									{row.label}
									{row.adminOnly ? (
										<Badge shape="tag">
											<LockIcon aria-hidden className="size-3" />
											administrators
										</Badge>
									) : null}
									{staged ? <Badge tone="staged">Staged</Badge> : null}
								</label>
								<p className="mt-1 text-ink-3 text-xs">{row.description}</p>
								{on && row.warning ? (
									<p className="mt-1.5 text-destructive-text text-xs leading-[1.5]">
										{row.warning}
									</p>
								) : null}
								{locked ? (
									<p className="mt-1 text-ink-3 text-xs">
										Only an administrator can change this.
									</p>
								) : null}
							</div>
							<Switch
								id={id}
								checked={on}
								disabled={locked || agentTooOld || update.isPending}
								onCheckedChange={(next) =>
									update.mutate({ [row.key]: next } as UpstreamPolicyStruct)
								}
								aria-label={row.label}
							/>
						</li>
					);
				})}
			</ul>
		</div>
	);
}
