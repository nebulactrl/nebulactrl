import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLinkIcon } from "lucide-react";
import { type ReactNode, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	getServerHardeningOptions,
	getServerHardeningQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import {
	applyServerHardening,
	rollbackServerHardening,
	runServerHardeningAudit,
	updateServerHardening,
} from "#/api/gen/sdk.gen";
import type {
	HardeningFix,
	HardeningRemaining,
	HardeningRun,
	HardeningView,
} from "#/api/gen/types.gen";
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
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { useIsAdmin } from "#/features/proxy/use-admin";
import { count } from "#/lib/format";
import type { ServerView } from "../model";
import {
	actionHint,
	fixActions,
	fixStatus,
	groupRemaining,
	outcomeText,
	runActionLabel,
	runSummary,
	safeFixesNote,
	safeFixIds,
	sinceFirst,
	triggerText,
	weeklyAuditLocked,
	weeklyAuditNote,
} from "./security-model";

/** While a run is in flight the tab polls; the SSE feed covers the rest. */
const RUNNING_POLL_MS = 3_000;

/**
 * The server's Security tab: the Lynis hardening index, the curated safe
 * fixes with an "Apply safe fixes" button, and the findings that remain, each
 * with an explanation and the commands to deal with it. Owners and Admins
 * change the host; everyone with the servers scope reads. NebulaCtrl never
 * changes SSH, the firewall or accounts, and says so next to those findings.
 */
export function SecurityTab({ server }: { server: ServerView }) {
	const query = useQuery({
		...getServerHardeningOptions({ path: { serverId: server.id } }),
		refetchInterval: (q) => (q.state.data?.running ? RUNNING_POLL_MS : false),
	});
	return (
		<SkeletonReveal
			loading={query.isPending}
			skeleton={
				<div
					className="flex flex-col gap-4"
					role="status"
					aria-label="Loading security"
				>
					<Skeleton className="h-40 rounded-card" />
					<Skeleton className="h-72 rounded-card" />
				</div>
			}
		>
			{() =>
				query.data ? (
					<SecurityBody server={server} view={query.data} />
				) : (
					<ErrorState
						frame="raised"
						title="Security did not load"
						body={`The control plane did not answer: ${errorMessage(query.error)}. Nothing was changed.`}
						retry={() => void query.refetch()}
					/>
				)
			}
		</SkeletonReveal>
	);
}

function SecurityBody({
	server,
	view,
}: {
	server: ServerView;
	view: HardeningView;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const isAdmin = useIsAdmin();
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: getServerHardeningQueryKey({ path: { serverId: server.id } }),
		});
	const path = { serverId: server.id };

	const audit = useMutation({
		mutationFn: async (installLynis: boolean) => {
			await runServerHardeningAudit({
				path,
				body: { installLynis },
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			toast.success(
				"Audit started",
				"The result appears here in a minute or so.",
			);
			await refresh();
		},
	});
	const apply = useMutation({
		mutationFn: async (fixIds: string[]) => {
			await applyServerHardening({
				path,
				body: { fixIds },
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			toast.success(
				"Applying fixes",
				"Each fix is checked, and undone by itself if it does not take effect.",
			);
			await refresh();
		},
	});
	const undo = useMutation({
		mutationFn: async (fixIds: string[]) => {
			await rollbackServerHardening({
				path,
				body: { fixIds },
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			toast.success(
				"Undoing the fix",
				"The previous settings are being restored.",
			);
			await refresh();
		},
	});
	const weekly = useMutation({
		mutationFn: async (on: boolean) => {
			await updateServerHardening({
				path,
				body: { weekly: on },
				throwOnError: true,
			});
		},
		onSuccess: refresh,
	});

	const hint = actionHint(view, server.status, isAdmin);
	const busy =
		hint !== null || audit.isPending || apply.isPending || undo.isPending;
	const safe = safeFixIds(view.fixes);
	const groups = groupRemaining(view.remaining);
	const error = audit.error ?? apply.error ?? undo.error ?? weekly.error;

	const confirmApply = (ids: string[], title: string) => {
		const picked = view.fixes.filter((f) => ids.includes(f.id));
		void confirm({
			title,
			body: (
				<div className="flex flex-col gap-3">
					<p>
						Each change is checked after it is made and undone by itself if it
						did not take effect. You can undo any fix from this page. SSH, the
						firewall and user accounts are never touched.
					</p>
					<ul className="flex flex-col gap-2">
						{picked.map((f) => (
							<li key={f.id}>
								<span className="font-medium text-ink-1">{f.title}</span>
								<span className="block text-ink-3 text-xs">{f.risk}</span>
								{f.rebootRequired ? (
									<span className="block text-ink-3 text-xs">
										Takes effect after the next reboot.
									</span>
								) : null}
							</li>
						))}
					</ul>
				</div>
			),
			cta: picked.length === 1 ? "Apply fix" : `Apply ${picked.length} fixes`,
			onConfirm: async () => {
				await apply.mutateAsync(ids);
			},
		});
	};

	return (
		<div className="flex flex-col gap-6">
			{view.rebootRequired ? (
				<Alert variant="info">
					<AlertTitle>A reboot is needed</AlertTitle>
					<AlertDescription>
						An applied fix takes effect after the next reboot of {server.name}.
						The next audit after the reboot clears this note.
					</AlertDescription>
				</Alert>
			) : null}

			<Card>
				<CardContent className="py-1">
					<SectionLabel className="pt-4 pb-1">
						Hardening index{" "}
						<DocsLink
							to="security/host-hardening"
							className="ml-2 normal-case tracking-normal"
						/>
					</SectionLabel>
					<div className="grid gap-4 py-4 sm:grid-cols-[minmax(0,220px)_minmax(0,1fr)]">
						<div className="flex flex-col gap-1">
							<span className="type-micro">Lynis score</span>
							<span className="type-metric">
								{view.hardeningIndex ?? "–"}
								{view.hardeningIndex !== null ? (
									<span className="ml-1 font-normal text-ink-3 text-sm">
										of 100
									</span>
								) : null}
							</span>
							{sinceFirst(view) ? (
								<span className="text-ink-3 text-xs">{sinceFirst(view)}</span>
							) : null}
						</div>
						<dl className="grid grid-cols-[minmax(96px,150px)_minmax(0,1fr)] items-baseline gap-x-4 gap-y-1.5 text-xs">
							<dt className="type-label">Last audit</dt>
							<dd className="text-ink-1">
								{view.auditedAt ? <Relative iso={view.auditedAt} /> : "never"}
							</dd>
							<dt className="type-label">Lynis</dt>
							<dd className="font-mono text-ink-1">
								{view.lynisInstalled
									? `version ${view.lynisVersion || "unknown"}`
									: "not installed"}
							</dd>
							<dt className="type-label">Findings</dt>
							<dd className="text-ink-1">
								{view.hardeningIndex === null
									? "–"
									: `${count(view.warnings, "warning")}, ${count(view.suggestions, "suggestion")}`}
								{view.testsDone > 0 ? (
									<span className="text-ink-3">
										{" "}
										from {view.testsDone} tests
									</span>
								) : null}
							</dd>
							<dt className="type-label">Reaches the host</dt>
							<dd className="text-ink-1">
								{view.mode === "helper"
									? "through a helper container"
									: view.mode === "host"
										? "directly (agent on the host)"
										: "–"}
							</dd>
						</dl>
					</div>
					{!view.lynisInstalled && view.agentSupported ? (
						<div className="pb-3.5">
							<Alert variant="info">
								<AlertTitle>Lynis is not installed on this server</AlertTitle>
								<AlertDescription>
									Lynis is an auditing tool from CISOfy. NebulaCtrl installs it
									from CISOfy's package repository (or your distribution's) and
									runs a read-only audit. You can apply the safe fixes without
									it, but the index needs it.
								</AlertDescription>
							</Alert>
						</div>
					) : null}
					<KvRow
						label="Audit now"
						description="Runs Lynis on the host. It only reads; nothing is changed."
						className="border-b-0 border-t"
						rule="top"
					>
						<span className="flex flex-col items-start gap-1.5">
							<Button
								type="button"
								variant="secondary"
								disabled={busy}
								onClick={() => audit.mutate(!view.lynisInstalled)}
							>
								{audit.isPending || view.running ? <Spinner /> : null}
								{view.running
									? "Working"
									: view.lynisInstalled
										? "Run audit"
										: "Install Lynis and run audit"}
							</Button>
							{hint ? <span className="text-ink-3 text-xs">{hint}</span> : null}
						</span>
					</KvRow>
					<KvRow
						label="Weekly audit"
						description="The control plane asks the agent for a fresh audit once a week. It never installs anything by itself."
						htmlFor="srv-hardening-weekly"
						className="border-b-0"
					>
						<span className="flex flex-col gap-1.5">
							<Switch
								id="srv-hardening-weekly"
								checked={view.weekly}
								disabled={
									!isAdmin || weekly.isPending || weeklyAuditLocked(view)
								}
								onCheckedChange={(on) => weekly.mutate(on)}
							/>
							<span className="max-w-[48ch] text-ink-3 text-xs">
								{weeklyAuditNote(view) ?? (
									<>
										Next audit <Relative iso={view.nextAuditAt ?? ""} />
									</>
								)}
							</span>
						</span>
					</KvRow>
				</CardContent>
			</Card>

			<Card>
				<CardContent className="py-1">
					<SectionLabel className="pt-4 pb-1">Safe fixes</SectionLabel>
					<div className="flex flex-wrap items-center justify-between gap-3 py-3">
						<div className="flex max-w-[62ch] flex-col gap-1.5 text-ink-3 text-sm">
							<p>
								A short, curated list that is reversible and does not touch SSH,
								the firewall or accounts. Each fix shows what it changes and its
								downside before you apply it.
							</p>
							<p className="text-xs">{safeFixesNote(view.fixes)}</p>
						</div>
						<Button
							type="button"
							disabled={busy || safe.length === 0}
							onClick={() =>
								confirmApply(
									safe,
									`Apply ${count(safe.length, "safe fix", "safe fixes")} on ${server.name}?`,
								)
							}
						>
							{apply.isPending ? <Spinner /> : null}
							{safe.length > 0
								? `Apply safe fixes (${safe.length})`
								: "Safe fixes applied"}
						</Button>
					</div>
					<ul className="rule-rows border-line border-t">
						{view.fixes.map((f) => (
							<FixRow
								key={f.id}
								fix={f}
								disabled={busy}
								onApply={() => confirmApply([f.id], `Apply “${f.title}”?`)}
								onUndo={() =>
									void confirm({
										title: `Undo “${f.title}”?`,
										body: (
											<>
												The settings NebulaCtrl changed for this fix go back to
												what they were. Anything you changed afterwards is kept.
											</>
										),
										cta: "Undo fix",
										onConfirm: async () => {
											await undo.mutateAsync([f.id]);
										},
									})
								}
							/>
						))}
					</ul>
				</CardContent>
			</Card>

			{view.hardeningIndex !== null ? (
				<Card>
					<CardContent className="py-1">
						<SectionLabel className="pt-4 pb-1">What is left</SectionLabel>
						{view.remaining.length === 0 ? (
							<p className="py-3.5 text-ink-3 text-sm">
								The latest audit reports nothing more.
							</p>
						) : (
							<div className="flex flex-col gap-4 pb-3">
								<Remaining
									title="A safe fix answers these"
									items={groups.fixable}
								/>
								<Remaining title="For you to decide" items={groups.manual} />
								<Remaining
									title="NebulaCtrl never changes these"
									items={groups.never}
								/>
							</div>
						)}
					</CardContent>
				</Card>
			) : null}

			<Card>
				<CardContent className="py-1">
					<SectionLabel className="pt-4 pb-1">Recent runs</SectionLabel>
					{view.runs.length === 0 ? (
						<p className="py-3.5 text-ink-3 text-sm">
							No audit has run on this server yet. The weekly audit starts one
							when the server is online, or start one above.
						</p>
					) : (
						<ul className="rule-rows">
							{view.runs.map((r) => (
								<RunRow key={r.id} run={r} />
							))}
						</ul>
					)}
				</CardContent>
			</Card>

			{error ? <FieldError>{errorMessage(error)}</FieldError> : null}
		</div>
	);
}

function FixRow({
	fix,
	disabled,
	onApply,
	onUndo,
}: {
	fix: HardeningFix;
	disabled: boolean;
	onApply: () => void;
	onUndo: () => void;
}) {
	const [open, setOpen] = useState(false);
	const status = fixStatus(fix);
	const act = fixActions(fix);
	return (
		<li className="py-3">
			<div className="flex min-w-0 items-start gap-3">
				<button
					type="button"
					className="flex min-w-0 flex-1 flex-col gap-1 text-left"
					aria-expanded={open}
					onClick={() => setOpen((o) => !o)}
				>
					<span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
						<span className="font-medium text-ink-1 text-sm">{fix.title}</span>
						{!fix.default ? (
							<Badge variant="outline" shape="tag">
								On request
							</Badge>
						) : null}
						{fix.rebootRequired ? (
							<Badge variant="outline" shape="tag">
								Needs a reboot
							</Badge>
						) : null}
					</span>
					<span className="flex items-center gap-1.5 text-ink-2 text-xs">
						<StatusIcon kind={status.kind} label="" />
						{status.label}
						{fix.message && fix.state !== "applied" ? (
							<span className="text-ink-3">· {fix.message}</span>
						) : null}
					</span>
				</button>
				{act.canApply ? (
					<Button
						type="button"
						size="sm"
						variant="secondary"
						disabled={disabled}
						onClick={onApply}
					>
						Apply
					</Button>
				) : null}
				{act.canUndo ? (
					<Button
						type="button"
						size="sm"
						variant="ghost"
						disabled={disabled}
						onClick={onUndo}
					>
						Undo
					</Button>
				) : null}
				<button
					type="button"
					className="mt-1 shrink-0 text-ink-3"
					aria-label={open ? "Hide details" : "Show details"}
					aria-expanded={open}
					onClick={() => setOpen((o) => !o)}
				>
					<DisclosureChevron open={open} className="size-4" />
				</button>
			</div>
			<DisclosurePanel open={open}>
				<div className="mt-3 flex flex-col gap-3 pr-8 text-sm">
					<Detail label="What it does">{fix.why}</Detail>
					<Detail label="The downside">{fix.risk}</Detail>
					<Detail label="Lynis tests">
						<span className="flex flex-wrap gap-x-3 gap-y-1">
							{fix.lynisTests.map((id) => (
								<a
									key={id}
									className="inline-flex items-center gap-1 font-mono text-accent-ink text-xs"
									href={`https://cisofy.com/lynis/controls/${id}/`}
									target="_blank"
									rel="noopener noreferrer"
								>
									{id}
									<ExternalLinkIcon aria-hidden className="size-3" />
								</a>
							))}
						</span>
					</Detail>
					<Detail label="By hand">
						<CodeBlock copyValue={fix.manual.join("\n")}>
							{fix.manual.join("\n")}
						</CodeBlock>
					</Detail>
				</div>
			</DisclosurePanel>
		</li>
	);
}

function Detail({ label, children }: { label: string; children: ReactNode }) {
	return (
		<div className="flex flex-col gap-1">
			<span className="type-label">{label}</span>
			<div className="text-ink-2">{children}</div>
		</div>
	);
}

function Remaining({
	title,
	items,
}: {
	title: string;
	items: HardeningRemaining[];
}) {
	if (items.length === 0) return null;
	return (
		<section className="flex flex-col gap-1">
			<h3 className="type-label pt-2">
				{title} <span className="text-ink-4">({items.length})</span>
			</h3>
			<ul className="rule-rows">
				{items.map((it) => (
					<RemainingRow key={`${it.testId}-${it.title}`} item={it} />
				))}
			</ul>
		</section>
	);
}

function RemainingRow({ item }: { item: HardeningRemaining }) {
	const [open, setOpen] = useState(false);
	const hasMore = Boolean(
		item.explanation || item.never || item.manual.length > 0 || item.detail,
	);
	return (
		<li className="py-2.5">
			<button
				type="button"
				className="flex w-full min-w-0 items-center gap-3 text-left"
				aria-expanded={open}
				onClick={() => setOpen((o) => !o)}
			>
				<StatusIcon
					kind={item.severity === "warning" ? "degraded" : "warning"}
					label={item.severity === "warning" ? "Warning" : "Suggestion"}
				/>
				<span className="w-[84px] shrink-0 font-mono text-ink-3 text-xs">
					{item.testId}
				</span>
				<span className="min-w-0 flex-1 truncate text-ink-1 text-sm">
					{item.title}
				</span>
				{item.fixId ? (
					<span className="shrink-0 text-ink-3 text-xs">
						{item.fixState === "pending"
							? "Fix available"
							: item.fixState === "unavailable"
								? "Fix not available here"
								: "Fix applied"}
					</span>
				) : null}
				<DisclosureChevron open={open} className="size-4 text-ink-3" />
			</button>
			<DisclosurePanel open={open}>
				<div className="mt-2 flex flex-col gap-2.5 pl-[108px] text-sm">
					{item.detail ? (
						<p className="text-ink-3 text-xs">{item.detail}</p>
					) : null}
					{item.explanation ? (
						<p className="text-ink-2">{item.explanation}</p>
					) : null}
					{item.fixMessage && item.fixState === "unavailable" ? (
						<p className="text-ink-3 text-xs">{item.fixMessage}</p>
					) : null}
					{item.never ? (
						<Alert variant="info">
							<AlertTitle>NebulaCtrl leaves this to you</AlertTitle>
							<AlertDescription>{item.never}</AlertDescription>
						</Alert>
					) : null}
					{item.manual.length > 0 ? (
						<CodeBlock copyValue={item.manual.join("\n")}>
							{item.manual.join("\n")}
						</CodeBlock>
					) : null}
					{!hasMore ? (
						<p className="text-ink-3 text-xs">Lynis gave no further detail.</p>
					) : null}
					<a
						className="inline-flex w-fit items-center gap-1 text-accent-ink text-xs"
						href={item.link}
						target="_blank"
						rel="noopener noreferrer"
					>
						About {item.testId} at CISOfy
						<ExternalLinkIcon aria-hidden className="size-3" />
					</a>
				</div>
			</DisclosurePanel>
		</li>
	);
}

function RunRow({ run }: { run: HardeningRun }) {
	const [open, setOpen] = useState(false);
	const expandable =
		run.outcomes.length > 0 || Boolean(run.error) || Boolean(run.message);
	return (
		<li className="py-2.5">
			<button
				type="button"
				className="flex w-full min-w-0 items-center gap-3 text-left disabled:cursor-default"
				aria-expanded={expandable ? open : undefined}
				disabled={!expandable}
				onClick={() => setOpen((o) => !o)}
			>
				<span className="w-[104px] shrink-0 text-ink-3 text-xs">
					<Relative iso={run.createdAt} />
				</span>
				<span className="w-[104px] shrink-0 text-ink-2 text-sm">
					{runActionLabel(run)}
				</span>
				<span className="w-[64px] shrink-0 text-ink-3 text-xs">
					{triggerText(run)}
				</span>
				<span
					className={
						run.status === "failed"
							? "flex min-w-0 flex-1 items-center gap-1.5 font-mono text-destructive-text text-xs"
							: "flex min-w-0 flex-1 items-center gap-1.5 font-mono text-ink-1 text-xs tabular-nums"
					}
				>
					{run.status === "pending" ? <Spinner className="shrink-0" /> : null}
					<span className="min-w-0 truncate">{runSummary(run)}</span>
				</span>
				{expandable ? (
					<DisclosureChevron open={open} className="size-4 text-ink-3" />
				) : null}
			</button>
			<DisclosurePanel open={open}>
				<div className="mt-2 flex flex-col gap-1.5 pl-[116px]">
					{run.error ? (
						<p className="text-destructive-text text-xs">{run.error}</p>
					) : null}
					{run.message ? (
						<p className="text-ink-3 text-xs">{run.message}</p>
					) : null}
					{run.outcomes.map((o) => (
						<div key={o.id} className="flex flex-col gap-0.5">
							<div className="flex items-baseline gap-3">
								<span className="w-[148px] shrink-0 font-mono text-ink-2 text-xs">
									{o.id}
								</span>
								<span
									className={
										o.outcome === "failed"
											? "text-destructive-text text-xs"
											: "text-ink-1 text-xs"
									}
								>
									{outcomeText(o.outcome)}
								</span>
							</div>
							{o.message ? (
								<span className="pl-[160px] text-ink-3 text-xs">
									{o.message}
								</span>
							) : null}
						</div>
					))}
				</div>
			</DisclosurePanel>
		</li>
	);
}

export type { HardeningView };
