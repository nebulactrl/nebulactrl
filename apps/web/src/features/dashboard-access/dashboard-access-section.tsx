import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { PlusIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getDashboardAccessOptions } from "#/api/gen/@tanstack/react-query.gen";
import { updateDashboardAccess } from "#/api/gen/sdk.gen";
import type { DashboardAccessView } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import { ErrorState } from "#/components/common/error-state";
import { DocsLink } from "#/components/docs/docs-link";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { FieldDescription, FieldError } from "#/components/ui/field";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { CidrListEditor } from "#/features/access-rules/cidr-list";
import {
	allowSummary,
	certificateChoices,
	checkDraft,
	type Draft,
	draftFor,
	draftValid,
	isDirty,
	listAdmits,
	lockoutOf,
	TAILNET_ONLY,
	toBody,
	withCurrentIp,
} from "./model";

const dashboardAccessQuery = () => ({
	...getDashboardAccessOptions(),
	staleTime: 30_000,
	retry: false,
});

/**
 * Admin → Dashboard access: who may reach the dashboard's
 * hostname (an IP allow list on its route on the control-plane host's proxy)
 * and how its certificate is issued. Instance admin only. An allow list applies
 * the moment it is saved; a DNS-01 certificate needs the proxy deployed once.
 * Saving a list that leaves the caller out asks for a confirmation first.
 */
export function DashboardAccessSection() {
	const queryClient = useQueryClient();
	const query = useQuery(dashboardAccessQuery());
	const data = query.data;
	const [draft, setDraft] = useState<Draft>({ allow: [], resolver: "" });
	const [confirmed, setConfirmed] = useState(false);
	const [saveError, setSaveError] = useState<string | null>(null);
	const [saved, setSaved] = useState<DashboardAccessView | null>(null);

	useEffect(() => {
		if (!data) return;
		setDraft(draftFor(data));
		setConfirmed(false);
	}, [data]);

	const save = useMutation({
		mutationFn: async (variables: { draft: Draft; confirm: boolean }) => {
			const res = await updateDashboardAccess({
				body: toBody(variables.draft, variables.confirm),
			});
			return res.data;
		},
		onSuccess: async (view) => {
			setSaveError(null);
			setSaved(view ?? null);
			await invalidateOperations(queryClient, ["getDashboardAccess"]);
			toast.success(
				"Dashboard access saved",
				view?.proxyDeployRequired
					? "The allow list is live. The DNS-01 certificate waits for Deploy proxy."
					: "The new rules are live.",
			);
		},
		onError: (e) => setSaveError(errorMessage(e)),
	});

	if (query.isPending) return <Skeleton className="h-64 rounded-card" />;
	if (!data) {
		return (
			<ErrorState
				frame="raised"
				title="Dashboard access did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => void query.refetch()}
			/>
		);
	}

	if (!data.enabled) {
		return (
			<div className="flex flex-col gap-4" data-testid="dashboard-access">
				<div className="surface-well flex flex-col gap-3 rounded-card p-4">
					<p className="text-ink-2 text-xs leading-relaxed">
						The dashboard is served without a domain, so there is no hostname to
						restrict. Re-run the installer on the control-plane host with a
						domain whose A record points at it. Your data, secrets and servers
						are kept; the dashboard moves behind the proxy with a certificate.{" "}
						<DocsLink to="installing/installer#two-ways-to-serve-the-dashboard" />
					</p>
					<code className="type-mono block break-all rounded-ctl bg-surface-base px-3 py-2 text-ink-2">
						… --domain nebula.example.com --email you@example.com
					</code>
				</div>
			</div>
		);
	}

	const check = checkDraft(draft);
	const valid = draftValid(draft);
	const dirty = isDirty(draft, data);
	const lockout = lockoutOf(draft, data);
	const choices = certificateChoices(data);
	const selected = choices.find((c) => c.value === draft.resolver);
	const youAreIn =
		data.clientIp !== "" && listAdmits(draft.allow, data.clientIp);
	const canSave =
		dirty && valid && !save.isPending && (!lockout.locksOut || confirmed);

	const submit = (event: React.FormEvent) => {
		event.preventDefault();
		if (!canSave) return;
		save.mutate({ draft, confirm: lockout.locksOut && confirmed });
	};

	return (
		<form
			onSubmit={submit}
			className="flex flex-col gap-5"
			data-testid="dashboard-access"
		>
			<section className="flex flex-col gap-3" aria-labelledby="da-allow-title">
				<div className="flex flex-col gap-1">
					<h4 id="da-allow-title" className="font-medium text-ink-1 text-sm">
						Who may open{" "}
						<span className="break-all font-mono">{data.host}</span>
					</h4>
					<p className="text-ink-3 text-xs">{allowSummary(draft.allow)}</p>
				</div>
				<CidrListEditor
					label="Allowed network"
					values={draft.allow}
					errors={check.entries}
					onChange={(allow) => {
						setDraft({ ...draft, allow });
						setConfirmed(false);
					}}
					disabled={save.isPending}
				/>
				{check.allow ? <FieldError>{check.allow}</FieldError> : null}
				<div className="flex flex-wrap items-center gap-1.5">
					<Button
						type="button"
						variant="secondary"
						size="xs"
						disabled={
							save.isPending ||
							!data.viaDashboardRoute ||
							data.clientIp === "" ||
							(draft.allow.length > 0 && youAreIn)
						}
						onClick={() => {
							setDraft({
								...draft,
								allow: withCurrentIp(draft.allow, data.clientIp),
							});
							setConfirmed(false);
						}}
					>
						<PlusIcon aria-hidden />
						Add my current IP
					</Button>
					<Button
						type="button"
						variant="secondary"
						size="xs"
						disabled={save.isPending}
						onClick={() => {
							setDraft({ ...draft, allow: [...TAILNET_ONLY] });
							setConfirmed(false);
						}}
					>
						Tailnet only
					</Button>
					<Button
						type="button"
						variant="ghost"
						size="xs"
						disabled={save.isPending || draft.allow.length === 0}
						onClick={() => {
							setDraft({ ...draft, allow: [] });
							setConfirmed(false);
						}}
					>
						Allow everybody
					</Button>
				</div>
				<FieldDescription>
					{data.viaDashboardRoute ? (
						<>
							You are connecting from{" "}
							<span className="font-mono text-ink-2">{data.clientIp}</span>.
						</>
					) : (
						"You are connected directly to the control plane, not through the dashboard hostname, so the list does not apply to you and your address is not shown."
					)}{" "}
					Agents on other servers and webhooks reach the control plane through
					this hostname too: keep their addresses in the list.
				</FieldDescription>

				{lockout.locksOut ? (
					<Alert variant="warning" data-testid="dashboard-lockout">
						<AlertTitle>This list locks you out</AlertTitle>
						<AlertDescription>
							<p>
								Your address <span className="font-mono">{lockout.ip}</span> is
								not in it. After saving, this browser can no longer open the
								dashboard. Recover from the host with an SSH tunnel to the
								control plane, or with{" "}
								<code className="type-mono">
									nebulactrl instance dashboard-access set --clear-allow
								</code>
								.
							</p>
							<CheckLabel
								checked={confirmed}
								onCheckedChange={setConfirmed}
								className="mt-2"
							>
								I understand I will lose access from {lockout.ip}
							</CheckLabel>
						</AlertDescription>
					</Alert>
				) : null}
			</section>

			<section className="flex flex-col gap-3" aria-labelledby="da-cert-title">
				<div className="flex flex-col gap-1">
					<h4 id="da-cert-title" className="font-medium text-ink-1 text-sm">
						Certificate
					</h4>
					<p className="max-w-[64ch] text-ink-3 text-xs leading-relaxed">
						Automatic HTTP-01 needs the hostname to reach this host on port 80.
						DNS-01 works for a hostname that only resolves on your tailnet.
					</p>
				</div>
				<Segmented
					aria-label="Certificate method"
					font="sans"
					density="regular"
					value={draft.resolver}
					onValueChange={(resolver) => setDraft({ ...draft, resolver })}
					options={choices.map((c) => ({
						value: c.value,
						label: c.label,
						disabled: c.disabled === true || save.isPending,
					}))}
				/>
				{(data.resolverAvailable ?? []).length === 0 ? (
					<p className="text-ink-3 text-xs">
						No DNS-01 resolver on the control-plane server yet. Add a DNS
						provider under{" "}
						{data.proxyServerId ? (
							<Link
								to="/servers/$serverId/proxy"
								params={{ serverId: data.proxyServerId }}
								className="text-ink-1 underline"
							>
								Servers, Proxy
							</Link>
						) : (
							"Servers, Proxy"
						)}
						, deploy the proxy, then choose it here.
					</p>
				) : null}
				{selected &&
				draft.resolver !== "" &&
				draft.resolver !== data.dnsResolver ? (
					<p className="text-ink-3 text-xs">
						The dashboard keeps its HTTP-01 certificate until the proxy runs the
						resolver. If it is staged, deploy the proxy after saving.
					</p>
				) : null}
				<p className="text-ink-3 text-xs">{data.certificate.message}</p>
			</section>

			{saveError ? (
				<Alert variant="destructive" role="alert">
					<AlertTitle>Not saved</AlertTitle>
					<AlertDescription>{saveError}</AlertDescription>
				</Alert>
			) : null}

			{data.proxyDeployRequired || saved?.proxyDeployRequired ? (
				<Alert variant="info" data-testid="dashboard-deploy-proxy">
					<AlertTitle>Deploy the proxy to switch the certificate</AlertTitle>
					<AlertDescription>
						The {data.dnsResolver} resolver is staged, not running. The proxy
						restarts for about a second when you deploy it.{" "}
						{data.proxyServerId ? (
							<Link
								to="/servers/$serverId/proxy"
								params={{ serverId: data.proxyServerId }}
								className="text-ink-1 underline"
							>
								Open Servers, Proxy
							</Link>
						) : null}
					</AlertDescription>
				</Alert>
			) : null}

			<div className="flex flex-wrap items-center gap-3">
				<Button type="submit" variant="secondary" disabled={!canSave}>
					{save.isPending ? <Spinner /> : null}
					{lockout.locksOut ? "Save and lock myself out" : "Save access"}
				</Button>
				{dirty ? (
					<Button
						type="button"
						variant="ghost"
						disabled={save.isPending}
						onClick={() => {
							setDraft(draftFor(data));
							setConfirmed(false);
							setSaveError(null);
						}}
					>
						Discard changes
					</Button>
				) : null}
			</div>
		</form>
	);
}
