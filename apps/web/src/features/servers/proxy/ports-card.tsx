import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LockIcon } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { planServerProxy, updateServerProxySettings } from "#/api/gen/sdk.gen";
import type { ProxyL4Port, ServerProxy } from "#/api/gen/types.gen";
import { meQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { toast } from "#/components/ui/toast";
import { isAgentTooOld } from "#/features/proxy/lib/errors";
import {
	planHighlights,
	planIsDisruptive,
} from "#/features/proxy/lib/proxy-plan";
import { invalidateProxy } from "#/features/proxy/queries";
import { AgentTooOldBanner } from "#/features/proxy/ui/agent-too-old";
import { PlanBody } from "./deploy-bar";
import {
	canReset,
	checkPorts,
	formOf,
	isDirty,
	type PortsForm,
	portsText,
	sourceLabel,
} from "./ports-model";

/**
 * The host ports of the proxy's web entrypoints. Shows where they come from
 * (the agent's service flags, or this server setting), and lets the instance
 * administrator move them later: the plan (what restarts, which port another
 * process holds, which certificates can no longer use HTTP-01) is shown first,
 * a confirm stages it, and the sticky Deploy proxy bar applies it.
 */
export function PortsCard({
	serverId,
	serverName,
	ports,
	l4Ports,
	agentTooOld,
}: {
	serverId: string;
	serverName: string;
	ports: ServerProxy["ports"];
	l4Ports: ProxyL4Port[];
	agentTooOld: boolean;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const baseId = useId();
	const me = useQuery(meQuery());
	const instanceAdmin = me.data?.instanceAdmin ?? false;
	const busyPorts = ports.busyOther ?? [];
	const [form, setForm] = useState<PortsForm>(() => formOf(ports));
	const { setHttp, setHttps } = ports;
	// A staged change from elsewhere (or this card) replaces the draft.
	useEffect(() => {
		setForm(formOf({ setHttp, setHttps }));
	}, [setHttp, setHttps]);

	const check = checkPorts(form, ports, l4Ports);
	const dirty = isDirty(form, ports);
	const unsupported = !ports.supported && !agentTooOld;

	const stage = useMutation({
		mutationFn: async (body: { http: number; https: number }) => {
			await updateServerProxySettings({
				path: { serverId },
				body: { ports: body },
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			await invalidateProxy(queryClient);
			toast.success("Ports staged", "They apply on the next Deploy proxy.");
		},
	});

	const review = useMutation({
		mutationFn: async (body: { http: number; https: number }) => {
			const { data } = await planServerProxy({
				path: { serverId },
				body: { ports: body },
				throwOnError: true,
			});
			return { plan: data, body };
		},
		onSuccess: async ({ plan, body }) => {
			const highlights = planHighlights(plan);
			const reset = body.http === 0 && body.https === 0;
			await confirm({
				title: reset
					? `Return ${serverName} to the agent's own ports?`
					: `Move the proxy on ${serverName} to another port?`,
				body: (
					<div className="flex flex-col gap-3">
						{highlights.changes.length === 0 ? (
							<p className="text-ink-2 text-xs">
								The proxy already listens on these ports; nothing changes when
								it is deployed.
							</p>
						) : null}
						<PlanBody highlights={highlights} />
						<p className="text-ink-3 text-xs">
							Confirming stages the change. The proxy is recreated only when you
							use Deploy proxy.
						</p>
					</div>
				),
				cta: "Stage ports",
				danger: planIsDisruptive(plan),
				hold: false,
				onConfirm: async () => {
					await stage.mutateAsync(body);
				},
			});
		},
	});

	const error = isAgentTooOld(review.error)
		? null
		: review.isError
			? errorMessage(review.error)
			: stage.isError
				? errorMessage(stage.error)
				: null;
	const busy = review.isPending || stage.isPending;

	return (
		<div className="flex flex-col gap-3" data-card="proxy-ports">
			<p className="type-secondary">
				The host ports Traefik publishes for HTTP and HTTPS. Let's Encrypt
				validates HTTP-01 on port 80 only, so a proxy on other ports (for
				example next to another proxy) needs DNS-01 certificates.
			</p>
			{isAgentTooOld(review.error) || isAgentTooOld(stage.error) ? (
				<AgentTooOldBanner serverName={serverName} />
			) : null}
			<ul aria-label="Proxy ports" className="rule-rows">
				<PortRow
					label="HTTP"
					applied={ports.appliedHttp}
					next={ports.http}
					staged={ports.staged}
				/>
				<PortRow
					label="HTTPS"
					applied={ports.appliedHttps}
					next={ports.https}
					staged={ports.staged}
					note="also UDP (HTTP/3)"
				/>
			</ul>
			<p className="text-ink-3 text-xs" data-testid="ports-source">
				{sourceLabel(ports.source)}
				{ports.source === "flag" && ports.reported
					? ` (--proxy-http-port ${ports.agentHttp}, --proxy-https-port ${ports.agentHttps})`
					: ""}
				.
			</p>
			{busyPorts.length > 0 ? (
				<p className="text-ink-3 text-xs">
					Another process on this server holds port {busyPorts.join(" and ")}{" "}
					right now.
				</p>
			) : null}
			{unsupported ? (
				<p className="text-warning-text text-xs">
					This agent cannot change its ports from the server. Update the agent,
					or restart it with --proxy-http-port and --proxy-https-port.
				</p>
			) : null}

			{instanceAdmin ? (
				<form
					className="flex flex-col gap-2"
					onSubmit={(event) => {
						event.preventDefault();
						if (check.body && dirty && !busy) review.mutate(check.body);
					}}
				>
					<div className="flex flex-wrap items-start gap-3">
						<label
							htmlFor={`${baseId}-http`}
							className="flex flex-col gap-1 text-ink-2 text-xs"
						>
							HTTP port
							<Input
								id={`${baseId}-http`}
								inputMode="numeric"
								autoComplete="off"
								className="w-28 font-mono"
								placeholder={String(ports.agentHttp)}
								value={form.http}
								aria-invalid={check.errors.http ? true : undefined}
								disabled={agentTooOld || unsupported}
								onChange={(event) =>
									setForm((f) => ({ ...f, http: event.target.value }))
								}
							/>
						</label>
						<label
							htmlFor={`${baseId}-https`}
							className="flex flex-col gap-1 text-ink-2 text-xs"
						>
							HTTPS port
							<Input
								id={`${baseId}-https`}
								inputMode="numeric"
								autoComplete="off"
								className="w-28 font-mono"
								placeholder={String(ports.agentHttps)}
								value={form.https}
								aria-invalid={check.errors.https ? true : undefined}
								disabled={agentTooOld || unsupported}
								onChange={(event) =>
									setForm((f) => ({ ...f, https: event.target.value }))
								}
							/>
						</label>
					</div>
					<p className="text-ink-3 text-xs">
						Leave a field empty to use the agent's own port (
						{portsText(ports.agentHttp, ports.agentHttps)}).
					</p>
					{check.errors.http ? (
						<FieldError>{check.errors.http}</FieldError>
					) : null}
					{check.errors.https ? (
						<FieldError>{check.errors.https}</FieldError>
					) : null}
					{check.errors.form ? (
						<FieldError>{check.errors.form}</FieldError>
					) : null}
					{error ? <FieldError>{error}</FieldError> : null}
					<div className="flex flex-wrap items-center gap-2">
						<Button
							type="submit"
							size="sm"
							disabled={
								busy ||
								!dirty ||
								check.body === null ||
								agentTooOld ||
								unsupported
							}
						>
							Review change
						</Button>
						{canReset(ports) ? (
							<Button
								type="button"
								size="sm"
								variant="ghost"
								disabled={busy || agentTooOld}
								onClick={() => review.mutate({ http: 0, https: 0 })}
							>
								Reset to agent defaults
							</Button>
						) : null}
					</div>
				</form>
			) : (
				<p className="flex items-center gap-1.5 text-ink-3 text-xs">
					<LockIcon aria-hidden className="size-3" />
					Only the instance administrator can change the proxy ports.
				</p>
			)}
		</div>
	);
}

function PortRow({
	label,
	applied,
	next,
	staged,
	note,
}: {
	label: string;
	applied: number;
	next: number;
	staged: boolean;
	note?: string;
}) {
	const moving = staged && applied !== next;
	return (
		<li className="flex flex-wrap items-center gap-2 py-1.5">
			<span className="w-14 font-medium text-ink-1 text-sm">{label}</span>
			<span className="font-mono text-ink-1 text-sm tabular-nums">
				{applied}
			</span>
			{moving ? (
				<>
					<span aria-hidden className="text-ink-3">
						→
					</span>
					<span className="font-mono text-ink-1 text-sm tabular-nums">
						{next}
					</span>
					<Badge tone="staged">Staged</Badge>
				</>
			) : null}
			{note ? <span className="text-ink-3 text-xs">{note}</span> : null}
		</li>
	);
}
