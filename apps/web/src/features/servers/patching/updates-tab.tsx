import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	getMySecurityOptions,
	getServerPatchesOptions,
	getServerPatchesQueryKey,
	getServerPatchRunOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import {
	applyServerPatches,
	checkServerPatches,
	planServerPatches,
	rebootServer,
} from "#/api/gen/sdk.gen";
import type {
	PatchPackage,
	PatchPlan,
	PatchRun,
	PatchView,
} from "#/api/gen/types.gen";
import { meQuery } from "#/api/hooks";
import { CodeBlock } from "#/components/common/code-block";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { DocsLink } from "#/components/docs/docs-link";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Card, CardContent } from "#/components/ui/card";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { FieldError } from "#/components/ui/field";
import { KvRow } from "#/components/ui/kv-row";
import { SectionLabel } from "#/components/ui/section-label";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import { count } from "#/lib/format";
import type { ServerView } from "../model";
import {
	busyText,
	needsAcknowledgement,
	patchHint,
	planCautions,
	productionList,
	rebootWarnings,
	runActionLabel,
	runStatus,
	runSummary,
	triggerText,
	updatesText,
} from "./patching-model";

/** While a run is in flight the tab polls; the SSE feed covers the rest. */
const BUSY_POLL_MS = 3_000;

/**
 * The server's Updates tab: pending operating-system updates (with the security
 * ones counted apart), whether the host asks for a reboot, and the two actions
 * that change the host, "Apply updates" and "Reboot". Both are reserved to the
 * instance admin, show a plan first, need the server's name typed, and are
 * audited; on a server that runs production the plan says so.
 */
export function UpdatesTab({ server }: { server: ServerView }) {
	const query = useQuery({
		...getServerPatchesOptions({ path: { serverId: server.id } }),
		refetchInterval: (q) => (q.state.data?.busy ? BUSY_POLL_MS : false),
	});
	return (
		<SkeletonReveal
			loading={query.isPending}
			skeleton={
				<div
					className="flex flex-col gap-4"
					role="status"
					aria-label="Loading updates"
				>
					<Skeleton className="h-40 rounded-card" />
					<Skeleton className="h-56 rounded-card" />
				</div>
			}
		>
			{() =>
				query.data ? (
					<UpdatesBody server={server} view={query.data} />
				) : (
					<ErrorState
						frame="raised"
						title="Updates did not load"
						body={`The control plane did not answer: ${errorMessage(query.error)}. Nothing was changed.`}
						retry={() => void query.refetch()}
					/>
				)
			}
		</SkeletonReveal>
	);
}

function useRoles() {
	const me = useQuery(meQuery());
	const security = useQuery(getMySecurityOptions());
	const role = me.data?.principal.role;
	return {
		canCheck: role === "owner" || role === "admin",
		instanceAdmin: security.data?.isInstanceAdmin ?? false,
	};
}

function UpdatesBody({
	server,
	view,
}: {
	server: ServerView;
	view: PatchView;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const roles = useRoles();
	const path = { serverId: server.id };
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: getServerPatchesQueryKey({ path }),
		});

	const check = useMutation({
		mutationFn: async () => {
			await checkServerPatches({ path, throwOnError: true });
		},
		onSuccess: async () => {
			toast.success(
				"Checking for updates",
				"The result appears here in a minute or so.",
			);
			await refresh();
		},
	});

	const checkHint = patchHint(view, server.status, {
		...roles,
		action: "check",
	});
	const applyHint = patchHint(view, server.status, {
		...roles,
		action: "apply",
	});
	const rebootHint = patchHint(view, server.status, {
		...roles,
		action: "reboot",
	});
	const busyLabel = busyText(view.busy);

	const startApply = async (securityOnly: boolean) => {
		let plan: PatchPlan;
		try {
			const res = await planServerPatches({
				path,
				query: { securityOnly },
				throwOnError: true,
			});
			plan = res.data;
		} catch (cause) {
			toast.error("The plan did not load", errorMessage(cause));
			return;
		}
		if (!plan.ready) {
			toast.warning("Nothing to upgrade", plan.notReadyReason);
			return;
		}
		void confirm({
			title: `Upgrade ${count(plan.updates, "package")} on ${server.name}?`,
			body: <PlanBody plan={plan} />,
			cta: "Upgrade packages",
			danger: plan.requiresAcknowledgement,
			typeToConfirm: server.name,
			onConfirm: async () => {
				await applyServerPatches({
					path,
					body: {
						confirm: server.name,
						securityOnly,
						acknowledgeProduction: plan.requiresAcknowledgement,
					},
					throwOnError: true,
				});
				toast.success(
					"Upgrading packages",
					"The upgrade runs on the host and finishes even if Docker restarts. Follow it in the history below.",
				);
				await refresh();
			},
		});
	};

	const startReboot = () => {
		void confirm({
			title: `Reboot ${server.name}?`,
			body: (
				<ul className="flex list-disc flex-col gap-1.5 pl-4">
					{rebootWarnings(view, server.serviceCount).map((w) => (
						<li key={w}>{w}</li>
					))}
				</ul>
			),
			cta: "Reboot server",
			danger: true,
			typeToConfirm: server.name,
			onConfirm: async () => {
				await rebootServer({
					path,
					body: {
						confirm: server.name,
						acknowledgeProduction: needsAcknowledgement(view),
					},
					throwOnError: true,
				});
				toast.warning(
					"Reboot scheduled",
					"The server goes down in a few seconds. NebulaCtrl waits for its agent to reconnect.",
				);
				await refresh();
			},
		});
	};

	const working = check.isPending || view.busy !== "";
	const securityOnly =
		view.security > 0 && view.security < view.updates && view.known;

	return (
		<div className="flex flex-col gap-6">
			{view.rebootRequired ? (
				<Alert variant="warning">
					<AlertTitle>{server.name} needs a reboot</AlertTitle>
					<AlertDescription>
						Installed updates only take effect after a reboot
						{view.rebootPackages.length > 0
							? ` (${view.rebootPackages.slice(0, 4).join(", ")}${view.rebootPackages.length > 4 ? " …" : ""})`
							: ""}
						. Services keep running until you reboot the server; pick a moment
						when a short outage is acceptable.
					</AlertDescription>
				</Alert>
			) : null}
			{busyLabel ? (
				<Alert variant="info">
					<AlertTitle className="flex items-center gap-1.5">
						<Spinner className="shrink-0" />
						{busyLabel}
					</AlertTitle>
					<AlertDescription>
						{view.busy === "reboot"
							? "NebulaCtrl is waiting for the agent to reconnect on a new boot. This page updates by itself."
							: "This page updates by itself when the host answers."}
					</AlertDescription>
				</Alert>
			) : null}
			{view.productionServices.length > 0 ? (
				<Alert variant="info">
					<AlertTitle>Production runs here</AlertTitle>
					<AlertDescription>
						{productionList(view.productionServices)}. Upgrading and rebooting
						need an explicit acknowledgement.
					</AlertDescription>
				</Alert>
			) : null}

			<Card>
				<CardContent className="py-1">
					<SectionLabel className="pt-4 pb-1">
						Operating system updates{" "}
						<DocsLink
							to="operations/server-patching"
							className="ml-2 normal-case tracking-normal"
						/>
					</SectionLabel>
					<div className="grid gap-4 py-4 sm:grid-cols-[minmax(0,220px)_minmax(0,1fr)]">
						<div className="flex flex-col gap-1">
							<span className="type-micro">Pending</span>
							<span className="type-metric">
								{view.known ? view.updates : "–"}
								{view.known && view.supported ? (
									<span className="ml-1 font-normal text-ink-3 text-sm">
										{view.updates === 1 ? "update" : "updates"}
									</span>
								) : null}
							</span>
							<span className="text-ink-3 text-xs">
								{updatesText({
									known: view.known,
									supported: view.supported,
									updates: view.updates,
									security: view.security,
								})}
							</span>
						</div>
						<dl className="grid grid-cols-[minmax(96px,150px)_minmax(0,1fr)] items-baseline gap-x-4 gap-y-1.5 text-xs">
							<dt className="type-label">Last check</dt>
							<dd className="text-ink-1">
								{view.checkedAt ? <Relative iso={view.checkedAt} /> : "never"}
								{view.stale ? (
									<span className="text-warning-text"> · out of date</span>
								) : null}
							</dd>
							<dt className="type-label">Package manager</dt>
							<dd className="font-mono text-ink-1">
								{view.manager || (view.known ? "none found" : "–")}
							</dd>
							<dt className="type-label">Reboot</dt>
							<dd className="text-ink-1">
								{!view.known
									? "–"
									: view.rebootRequired
										? "required"
										: "not required"}
							</dd>
						</dl>
					</div>
					{view.message ? (
						<p className="pb-3 text-ink-3 text-xs">{view.message}</p>
					) : null}
					<KvRow
						label="Check now"
						description="Refreshes the package lists on the host and reads what is pending. Nothing is installed. NebulaCtrl also checks about every 6 hours."
						className="border-b-0 border-t"
						rule="top"
					>
						<span className="flex flex-col items-start gap-1.5">
							<Button
								type="button"
								variant="secondary"
								disabled={checkHint !== null || working}
								onClick={() => check.mutate()}
							>
								{check.isPending || view.busy === "check" ? <Spinner /> : null}
								Check for updates
							</Button>
							{checkHint ? (
								<span className="text-ink-3 text-xs">{checkHint}</span>
							) : null}
						</span>
					</KvRow>
					<KvRow
						label="Apply updates"
						description="Upgrades the packages on the host as root, keeping the configuration files you changed. You see a plan first. It never reboots by itself."
						className="border-b-0"
					>
						<span className="flex flex-col items-start gap-1.5">
							<span className="flex flex-wrap gap-2">
								<Button
									type="button"
									disabled={applyHint !== null || working || view.updates === 0}
									onClick={() => void startApply(false)}
								>
									{view.busy === "apply" ? <Spinner /> : null}
									{view.updates > 0
										? `Apply ${count(view.updates, "update")}…`
										: "Apply updates…"}
								</Button>
								{securityOnly ? (
									<Button
										type="button"
										variant="secondary"
										disabled={applyHint !== null || working}
										onClick={() => void startApply(true)}
									>
										Security only ({view.security})…
									</Button>
								) : null}
							</span>
							{applyHint ? (
								<span className="text-ink-3 text-xs">{applyHint}</span>
							) : null}
						</span>
					</KvRow>
					<KvRow
						label="Reboot"
						description="Restarts the whole server. Every service on it goes down until it boots and Docker starts them again."
						className="border-b-0"
					>
						<span className="flex flex-col items-start gap-1.5">
							<Button
								type="button"
								variant={view.rebootRequired ? "primary" : "secondary"}
								disabled={rebootHint !== null || working}
								onClick={startReboot}
							>
								{view.busy === "reboot" ? <Spinner /> : null}
								Reboot server…
							</Button>
							{rebootHint ? (
								<span className="text-ink-3 text-xs">{rebootHint}</span>
							) : null}
						</span>
					</KvRow>
				</CardContent>
			</Card>

			{view.packages.length > 0 ? (
				<Card>
					<CardContent className="py-1">
						<SectionLabel className="pt-4 pb-1">Pending packages</SectionLabel>
						{view.updates > view.packages.length ? (
							<p className="pb-2 text-ink-3 text-xs">
								The first {view.packages.length} of {view.updates} are listed,
								security updates first.
							</p>
						) : null}
						<ul className="rule-rows">
							{view.packages.map((p) => (
								<PackageRow key={p.name} pkg={p} />
							))}
						</ul>
					</CardContent>
				</Card>
			) : null}

			<Card>
				<CardContent className="py-1">
					<SectionLabel className="pt-4 pb-1">History</SectionLabel>
					{view.runs.length === 0 ? (
						<p className="py-3.5 text-ink-3 text-sm">
							Nothing has run on this server yet. The first automatic check
							starts when the server is online.
						</p>
					) : (
						<ul className="rule-rows">
							{view.runs.map((r) => (
								<RunRow key={r.id} serverId={server.id} run={r} />
							))}
						</ul>
					)}
				</CardContent>
			</Card>

			{check.error ? (
				<FieldError>{errorMessage(check.error)}</FieldError>
			) : null}
		</div>
	);
}

/** What the confirmation shows: the plan in words, the risky packages and the warnings. */
function PlanBody({ plan }: { plan: PatchPlan }) {
	const cautions = planCautions(plan);
	return (
		<div className="flex flex-col gap-3">
			<p>
				{plan.securityOnly
					? `Upgrades the ${count(plan.updates, "package")} with a security update`
					: `Upgrades ${count(plan.updates, "package")} (${plan.security} security)`}{" "}
				with {plan.manager}, as root, on {plan.server}. Your changed
				configuration files are kept and no package is removed.
			</p>
			{plan.productionServices.length > 0 ? (
				<p className="font-medium text-warning-text">
					Production runs on this server:{" "}
					{productionList(plan.productionServices)}. Containers may restart if
					Docker is upgraded.
				</p>
			) : null}
			{plan.controlPlane ? (
				<p className="font-medium text-warning-text">
					This server runs NebulaCtrl itself.
				</p>
			) : null}
			{cautions.length > 0 ? (
				<ul className="flex list-disc flex-col gap-1 pl-4 text-ink-2 text-xs">
					{cautions.map((c) => (
						<li key={c}>{c}</li>
					))}
				</ul>
			) : null}
			<p className="text-ink-3 text-xs">
				Packages:{" "}
				{plan.packages
					.slice(0, 12)
					.map((p) => p.name)
					.join(", ")}
				{plan.packages.length > 12
					? `, and ${plan.packages.length - 12} more`
					: ""}
			</p>
		</div>
	);
}

function PackageRow({ pkg }: { pkg: PatchPackage }) {
	return (
		<li className="flex min-w-0 flex-col gap-0.5 py-2">
			<span className="flex min-w-0 items-center gap-3">
				<span className="min-w-0 flex-1 truncate font-mono text-ink-1 text-xs">
					{pkg.name}
				</span>
				<span className="hidden shrink-0 truncate font-mono text-ink-3 text-xs sm:inline">
					{pkg.from ? `${pkg.from} → ` : ""}
					{pkg.to}
				</span>
				{pkg.security ? (
					<Badge variant="outline" title="From a security repository">
						Security
					</Badge>
				) : null}
			</span>
			{pkg.risk ? <span className="text-ink-3 text-xs">{pkg.risk}</span> : null}
		</li>
	);
}

function RunRow({ serverId, run }: { serverId: string; run: PatchRun }) {
	const [open, setOpen] = useState(false);
	const status = runStatus(run);
	const expandable =
		run.action === "apply" || Boolean(run.error) || Boolean(run.message);
	return (
		<li className="py-2.5">
			<button
				type="button"
				className="flex w-full min-w-0 items-center gap-3 text-left disabled:cursor-default"
				aria-expanded={expandable ? open : undefined}
				disabled={!expandable}
				onClick={() => setOpen((o) => !o)}
			>
				<StatusIcon kind={status.kind} label={status.label} />
				<span className="w-[104px] shrink-0 text-ink-3 text-xs">
					<Relative iso={run.createdAt} />
				</span>
				<span className="w-[72px] shrink-0 text-ink-2 text-sm">
					{runActionLabel(run)}
				</span>
				<span className="w-[72px] shrink-0 text-ink-3 text-xs">
					{triggerText(run)}
				</span>
				<span
					className={
						run.status === "failed"
							? "min-w-0 flex-1 truncate font-mono text-destructive-text text-xs"
							: "min-w-0 flex-1 truncate font-mono text-ink-1 text-xs tabular-nums"
					}
				>
					{runSummary(run)}
				</span>
				{expandable ? (
					<DisclosureChevron open={open} className="size-4 text-ink-3" />
				) : null}
			</button>
			<DisclosurePanel open={open}>
				{open ? <RunDetail serverId={serverId} run={run} /> : null}
			</DisclosurePanel>
		</li>
	);
}

/** The run's message and, for an upgrade, the log the package manager printed. */
function RunDetail({ serverId, run }: { serverId: string; run: PatchRun }) {
	const detail = useQuery({
		...getServerPatchRunOptions({ path: { serverId, runId: run.id } }),
		enabled: run.action === "apply",
		refetchInterval: run.status === "pending" ? BUSY_POLL_MS : false,
	});
	const log = detail.data?.log ?? "";
	return (
		<div className="mt-2 flex flex-col gap-2 pl-[116px]">
			{run.error ? (
				<p className="text-destructive-text text-xs">{run.error}</p>
			) : null}
			{run.message ? <p className="text-ink-3 text-xs">{run.message}</p> : null}
			{run.action === "apply" ? (
				detail.isPending ? (
					<Skeleton className="h-24 rounded-ctl" />
				) : log ? (
					<CodeBlock copyValue={log}>{log}</CodeBlock>
				) : (
					<p className="text-ink-3 text-xs">
						{run.status === "pending"
							? "The upgrade is running; its log appears when it ends."
							: "The package manager printed nothing."}
					</p>
				)
			) : null}
		</div>
	);
}
