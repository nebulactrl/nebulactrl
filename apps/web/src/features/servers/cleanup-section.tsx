import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getServerCleanupOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	pruneServerVolumes,
	runServerCleanup,
	updateServerCleanup,
} from "#/api/gen/sdk.gen";
import type { CleanupRun, CleanupView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { DocsLink } from "#/components/docs/docs-link";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardFooter } from "#/components/ui/card";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { KvRow } from "#/components/ui/kv-row";
import { SectionLabel } from "#/components/ui/section-label";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import {
	CLEANUP_LIMITS,
	CLEANUP_SCHEDULE_PRESETS,
	type CleanupBody,
	type CleanupDraft,
	cleanupBody,
	cleanupDraftChanged,
	runSummary,
	STEP_LABELS,
	stepSummary,
	toCleanupDraft,
	triggerLabel,
	validateCleanupDraft,
} from "./cleanup-model";
import type { ServerView } from "./model";

/** While a run is in flight the card polls; the SSE feed covers the rest. */
const RUNNING_POLL_MS = 3_000;

/**
 * Docker cleanup of one server: the daily schedule, the switch for stopped
 * containers NebulaCtrl does not manage, "Run cleanup now", the history of
 * runs with what each step freed, and the separate, typed-confirm removal of
 * unused volumes. A cleanup never touches volumes, the current or a rollback
 * image of any service, or the images NebulaCtrl runs itself.
 */
export function CleanupSection({ server }: { server: ServerView }) {
	const query = useQuery({
		...getServerCleanupOptions({ path: { serverId: server.id } }),
		refetchInterval: (q) => (q.state.data?.running ? RUNNING_POLL_MS : false),
	});
	return (
		<SkeletonReveal
			loading={query.isPending}
			skeleton={<Skeleton className="h-56 rounded-card" />}
		>
			{() =>
				query.data ? (
					<CleanupCard server={server} view={query.data} />
				) : (
					<Card>
						<CardContent className="py-4 text-sm text-ink-3">
							Docker cleanup did not load: {errorMessage(query.error)}
						</CardContent>
					</Card>
				)
			}
		</SkeletonReveal>
	);
}

function CleanupCard({
	server,
	view,
}: {
	server: ServerView;
	view: CleanupView;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: getServerCleanupOptions({ path: { serverId: server.id } })
				.queryKey,
		});

	const save = useMutation({
		// The endpoint replaces every setting: always send them all.
		mutationFn: async (next: CleanupBody) => {
			await updateServerCleanup({
				path: { serverId: server.id },
				body: next,
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			toast.success(
				"Cleanup settings saved",
				"They apply from now; a new schedule does not catch up on earlier runs.",
			);
			await refresh();
		},
	});
	const run = useMutation({
		mutationFn: async () => {
			await runServerCleanup({
				path: { serverId: server.id },
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			toast.success(
				"Cleanup started",
				"The result appears under Recent cleanups.",
			);
			await refresh();
		},
	});
	const volumes = useMutation({
		mutationFn: async () => {
			await pruneServerVolumes({
				path: { serverId: server.id },
				body: { confirm: server.name },
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			toast.success(
				"Removing unused volumes",
				"The result appears under Recent cleanups.",
			);
			await refresh();
		},
	});

	// The editable schedule, switches and limits: a draft that follows the
	// saved values whenever they change (after a save, or an update from
	// another tab). Nothing saves until "Save changes".
	const savedFlags = {
		enabled: view.enabled,
		removeStoppedUnmanaged: view.removeStoppedUnmanaged,
	};
	const saved = toCleanupDraft(view);
	const savedKey = JSON.stringify([savedFlags, saved]);
	const [syncedKey, setSyncedKey] = useState("");
	const [draft, setDraft] = useState<CleanupDraft | null>(null);
	const [flags, setFlags] = useState(savedFlags);
	if (savedKey !== syncedKey) {
		setSyncedKey(savedKey);
		setDraft(saved);
		setFlags(savedFlags);
	}

	const form = draft ?? saved;
	const errors = validateCleanupDraft(form);
	const valid = Object.keys(errors).length === 0;
	const dirty =
		cleanupDraftChanged(form, saved) ||
		flags.enabled !== savedFlags.enabled ||
		flags.removeStoppedUnmanaged !== savedFlags.removeStoppedUnmanaged;
	const setField = (patch: Partial<CleanupDraft>) =>
		setDraft({ ...form, ...patch });
	const discard = () => {
		setDraft(saved);
		setFlags(savedFlags);
		save.reset();
	};
	const offline = server.status !== "online";
	const cannotRun = offline || !view.agentSupported || view.running;
	const runHint = !view.agentSupported
		? "This agent is too old to run a cleanup. Update it first."
		: offline
			? `${server.name} is offline. A cleanup runs when it is back online.`
			: view.running
				? "A cleanup is running."
				: dirty
					? "Runs with the saved settings, not the unsaved changes."
					: null;

	return (
		<Card>
			<CardContent className="py-1">
				<SectionLabel className="pt-4 pb-1">
					Docker cleanup{" "}
					<DocsLink
						to="operations/docker-cleanup"
						className="ml-2 normal-case tracking-normal"
					/>
				</SectionLabel>
				<KvRow
					label="Scheduled cleanup"
					description={`${view.schedule}, and when disk use reaches ${view.diskTriggerPercent ?? view.diskAlertThresholdPercent}%.`}
					htmlFor="srv-cleanup-on"
				>
					<span className="flex flex-col gap-1.5">
						<Switch
							id="srv-cleanup-on"
							checked={flags.enabled}
							onCheckedChange={(enabled) =>
								setFlags((f) => ({ ...f, enabled }))
							}
						/>
						<span className="text-ink-3 text-xs">
							{!flags.enabled ? (
								"Off. Nothing is removed unless you run a cleanup."
							) : view.enabled && view.nextRunAt ? (
								<>
									Next run <Relative iso={view.nextRunAt} />
								</>
							) : (
								"Starts once you save."
							)}
						</span>
					</span>
				</KvRow>
				<KvRow
					label="Stopped containers"
					description="Also remove stopped containers that NebulaCtrl does not manage, once they have been stopped for an hour. Their volumes stay. Off by default."
					htmlFor="srv-cleanup-containers"
				>
					<Switch
						id="srv-cleanup-containers"
						checked={flags.removeStoppedUnmanaged}
						onCheckedChange={(removeStoppedUnmanaged) =>
							setFlags((f) => ({ ...f, removeStoppedUnmanaged }))
						}
					/>
				</KvRow>
				<KvRow
					label="Schedule"
					description="A cron expression (minute hour day-of-month month day-of-week), evaluated in UTC. Runs must be at least 15 minutes apart."
					htmlFor="srv-cleanup-cron"
				>
					<span className="flex flex-col items-start gap-1.5">
						<Input
							id="srv-cleanup-cron"
							className="w-56 font-mono"
							value={form.cron}
							spellCheck={false}
							autoComplete="off"
							aria-invalid={errors.cron !== undefined}
							onChange={(e) => setField({ cron: e.target.value })}
						/>
						<span className="flex flex-wrap gap-1.5">
							{CLEANUP_SCHEDULE_PRESETS.map((preset) => (
								<Button
									key={preset.cron}
									type="button"
									variant="secondary"
									size="xs"
									onClick={() => setField({ cron: preset.cron })}
								>
									{preset.label}
								</Button>
							))}
						</span>
						<FieldError>{errors.cron}</FieldError>
					</span>
				</KvRow>
				<KvRow
					label="Unused images"
					description="Removed once unused for this many hours, unless a release's rollback image or an image NebulaCtrl runs."
					htmlFor="srv-cleanup-image-hours"
				>
					<span className="flex flex-col items-start gap-1.5">
						<Input
							id="srv-cleanup-image-hours"
							type="number"
							className="w-28"
							min={CLEANUP_LIMITS.unusedImageHours.min}
							max={CLEANUP_LIMITS.unusedImageHours.max}
							value={form.unusedImageMinAgeHours}
							aria-invalid={errors.unusedImageMinAgeHours !== undefined}
							onChange={(e) =>
								setField({ unusedImageMinAgeHours: e.target.value })
							}
						/>
						<FieldError>{errors.unusedImageMinAgeHours}</FieldError>
					</span>
				</KvRow>
				<KvRow
					label="Build cache"
					description="Pruned once it is older than this many days."
					htmlFor="srv-cleanup-cache-days"
				>
					<span className="flex flex-col items-start gap-1.5">
						<Input
							id="srv-cleanup-cache-days"
							type="number"
							className="w-28"
							min={CLEANUP_LIMITS.buildCacheDays.min}
							max={CLEANUP_LIMITS.buildCacheDays.max}
							value={form.buildCacheMinAgeDays}
							aria-invalid={errors.buildCacheMinAgeDays !== undefined}
							onChange={(e) =>
								setField({ buildCacheMinAgeDays: e.target.value })
							}
						/>
						<FieldError>{errors.buildCacheMinAgeDays}</FieldError>
					</span>
				</KvRow>
				<KvRow
					label="Disk trigger (%)"
					description={`Also start a cleanup when the root disk reaches this percent, at most one every 6 hours. Empty follows the disk alert threshold (${view.diskAlertThresholdPercent}%).`}
					htmlFor="srv-cleanup-disk"
				>
					<span className="flex flex-col items-start gap-1.5">
						<Input
							id="srv-cleanup-disk"
							type="number"
							className="w-28"
							min={CLEANUP_LIMITS.diskTriggerPercent.min}
							max={CLEANUP_LIMITS.diskTriggerPercent.max}
							value={form.diskTriggerPercent}
							placeholder={String(view.diskAlertThresholdPercent)}
							aria-invalid={errors.diskTriggerPercent !== undefined}
							onChange={(e) => setField({ diskTriggerPercent: e.target.value })}
						/>
						<FieldError>{errors.diskTriggerPercent}</FieldError>
					</span>
				</KvRow>
				<KvRow
					label="Always kept"
					description="Volumes, the running and the rollback images of every service, and the images NebulaCtrl runs itself."
					className="border-b-0"
				>
					<span className="text-ink-3 text-sm">
						A rollback to a retained release keeps working after a cleanup.
					</span>
				</KvRow>
			</CardContent>
			<CardFooter className="justify-between">
				<span className="min-w-0 text-xs">
					{save.isError ? (
						<FieldError>{errorMessage(save.error)}</FieldError>
					) : dirty ? (
						<span className="text-ink-3">
							You have unsaved changes. They apply from now: a new schedule does
							not catch up on earlier runs.
						</span>
					) : (
						<span className="text-ink-3">No unsaved changes.</span>
					)}
				</span>
				<span className="flex items-center gap-2">
					{dirty ? (
						<Button type="button" variant="ghost" onClick={discard}>
							Discard
						</Button>
					) : null}
					<Button
						type="button"
						variant="primary"
						disabled={!dirty || !valid || save.isPending}
						onClick={() => save.mutate(cleanupBody(flags, form))}
					>
						{save.isPending ? <Spinner /> : null}
						Save changes
					</Button>
				</span>
			</CardFooter>

			<CardContent className="border-line border-t py-1">
				<SectionLabel className="pt-4 pb-1">Run a cleanup</SectionLabel>
				<KvRow
					label="Run cleanup now"
					description="The same cleanup the schedule runs, started by you."
					className="border-b-0"
				>
					<span className="flex flex-col items-start gap-1.5">
						<Button
							type="button"
							variant="secondary"
							disabled={cannotRun || run.isPending}
							onClick={() => run.mutate()}
						>
							{run.isPending || view.running ? <Spinner /> : null}
							{view.running ? "Cleaning up" : "Run cleanup now"}
						</Button>
						{runHint ? (
							<span className="text-ink-3 text-xs">{runHint}</span>
						) : null}
						{run.isError ? (
							<FieldError>{errorMessage(run.error)}</FieldError>
						) : null}
					</span>
				</KvRow>
			</CardContent>

			<CardContent className="border-line border-t py-1">
				<SectionLabel className="pt-4 pb-1">Recent cleanups</SectionLabel>
				{view.runs.length === 0 ? (
					<p className="py-3.5 text-ink-3 text-sm">
						No cleanup has run on this server yet. The first one runs at the
						next scheduled time, or when you run one.
					</p>
				) : (
					<ul className="rule-rows">
						{view.runs.map((r) => (
							<RunRow key={r.id} run={r} />
						))}
					</ul>
				)}
			</CardContent>

			<CardContent className="border-line border-t py-1">
				<KvRow
					label="Unused volumes"
					description="Deletes volumes that no container mounts and that NebulaCtrl does not manage, such as anonymous volumes. Volumes of your services are never removed. This deletes data and cannot be undone."
					className="border-b-0"
				>
					<span className="flex flex-col items-start gap-1.5">
						<Button
							type="button"
							variant="danger"
							disabled={cannotRun || volumes.isPending}
							onClick={() =>
								void confirm({
									title: `Remove unused volumes on ${server.name}?`,
									body: (
										<>
											Volumes that no container uses, and that NebulaCtrl did
											not create for a service, are deleted with their data.
											Volumes of NebulaCtrl services are kept. This cannot be
											undone.
										</>
									),
									cta: "Remove unused volumes",
									danger: true,
									typeToConfirm: server.name,
									onConfirm: async () => {
										await volumes.mutateAsync();
									},
								})
							}
						>
							Remove unused volumes…
						</Button>
						{volumes.isError ? (
							<FieldError>{errorMessage(volumes.error)}</FieldError>
						) : null}
					</span>
				</KvRow>
			</CardContent>
		</Card>
	);
}

/** One run: when, who started it, what it freed; expands to its steps. */
function RunRow({ run }: { run: CleanupRun }) {
	const [open, setOpen] = useState(false);
	const failed = run.status === "failed";
	const hasSteps = run.steps.length > 0;
	return (
		<li className="py-2.5">
			<button
				type="button"
				className="flex w-full min-w-0 items-center gap-3 text-left disabled:cursor-default"
				aria-expanded={hasSteps ? open : undefined}
				disabled={!hasSteps && !run.error}
				onClick={() => setOpen((o) => !o)}
			>
				<span className="w-[128px] shrink-0 text-ink-3 text-xs">
					<Relative iso={run.createdAt} />
				</span>
				<span className="w-[104px] shrink-0 text-ink-2 text-sm">
					{triggerLabel(run)}
				</span>
				<span
					className={
						failed
							? "min-w-0 flex-1 truncate font-mono text-destructive-text text-xs"
							: "min-w-0 flex-1 truncate font-mono text-ink-1 text-xs tabular-nums"
					}
				>
					{run.status === "pending" ? <Spinner className="mr-1.5" /> : null}
					{runSummary(run)}
				</span>
				{hasSteps || run.error ? (
					<DisclosureChevron open={open} className="size-4 text-ink-3" />
				) : null}
			</button>
			<DisclosurePanel open={open}>
				<div className="mt-2 flex flex-col gap-1.5 pl-[132px]">
					{run.error ? (
						<p className="text-destructive-text text-xs">{run.error}</p>
					) : null}
					{run.steps.map((s) => (
						<div key={s.name} className="flex flex-col gap-0.5">
							<div className="flex items-baseline gap-3">
								<span className="w-[132px] shrink-0 text-ink-2 text-xs">
									{STEP_LABELS[s.name]}
								</span>
								<span className="font-mono text-ink-1 text-xs tabular-nums">
									{stepSummary(s)}
								</span>
							</div>
							{s.error ? (
								<span className="pl-[144px] text-destructive-text text-xs">
									{s.error}
								</span>
							) : null}
							{s.removed.length > 0 ? (
								<span className="pl-[144px] font-mono text-ink-3 text-xs">
									{s.removed.join(", ")}
									{s.items > s.removed.length
										? ` and ${s.items - s.removed.length} more`
										: ""}
								</span>
							) : null}
						</div>
					))}
				</div>
			</DisclosurePanel>
		</li>
	);
}
