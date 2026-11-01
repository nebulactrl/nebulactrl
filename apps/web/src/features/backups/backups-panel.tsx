import { DitherUptimeBar } from "@nebulactrl/dither/charts/uptime-bar";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
	DatabaseBackupIcon,
	FileUpIcon,
	LockIcon,
	LockOpenIcon,
	PlusIcon,
} from "lucide-react";
import { useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	listBackupDestinationsOptions,
	listBackupRestoresOptions,
	listBackupSchedulesOptions,
	listBackupsOptions,
	listDumpRestoresOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import {
	deleteBackup,
	deleteBackupSchedule,
	lockBackup,
	updateBackupSchedule,
} from "#/api/gen/sdk.gen";
import type { BackupView, ScheduleView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { DocsLink } from "#/components/docs/docs-link";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { StatusPill } from "#/components/ui/lamp";
import { Skeleton } from "#/components/ui/skeleton";
import { Switch } from "#/components/ui/switch";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { Section } from "#/features/service-settings/fields";
import { absoluteTime, formatBytes } from "#/lib/format";
import { DestinationDialog } from "./destination-dialog";
import { BackupNowDialog, RestoreDialog, ScheduleDialog } from "./dialogs";
import { canRestoreDump } from "./dump";
import { DumpRestoreDialog } from "./dump-restore-dialog";
import {
	BACKUP_DAY_WORDS,
	backupDays,
	backupPresentation,
	backupSuccessShare,
	canRestore,
	describeCron,
	describeRetention,
	inFlight,
	restorePresentation,
	TRIGGER_LABEL,
} from "./model";
import { IN_FLIGHT_POLL_MS, invalidateBackups } from "./queries";

/** Days the history strip above the backup list covers. */
const HISTORY_DAYS = 30;

/** The newest runs the panel loads: the list and the strip both read them. */
const BACKUP_LIMIT = 50;

export interface BackupsPanelProps {
	serviceId: string;
	serviceSlug: string;
	/** The engine id (postgres, mysql, …); gates restore for engines without one. */
	engine: string;
	/** The server the database runs on. */
	serverId: string | null;
}

function BackupRow({
	backup,
	engine,
	onRestore,
	onDelete,
	onLock,
}: {
	backup: BackupView;
	engine: string;
	onRestore: (b: BackupView) => void;
	onDelete: (b: BackupView) => void;
	onLock: (b: BackupView) => void;
}) {
	const p = backupPresentation(backup.status);
	const done = backup.status === "succeeded";
	return (
		<TableRow>
			<TableCell pin="start">
				<span className="flex flex-col gap-0.5">
					<span
						className="text-ink-1 text-xs"
						title={absoluteTime(backup.createdAt)}
					>
						<Relative iso={backup.createdAt} />
					</span>
					<span className="text-2xs text-ink-4">
						{TRIGGER_LABEL[backup.trigger] ?? backup.trigger}
					</span>
				</span>
			</TableCell>
			<TableCell label="Status">
				<span className="flex flex-col gap-1">
					<StatusPill tone={p.tone} pulse={p.pulse}>
						{p.label}
					</StatusPill>
					{backup.error ? (
						<span
							className="line-clamp-2 max-w-[360px] text-destructive-text text-xs"
							title={backup.error}
						>
							{backup.error}
						</span>
					) : null}
				</span>
			</TableCell>
			<TableCell label="Destination" className="text-ink-3 text-xs">
				<span className="flex items-center gap-1.5">
					{backup.destinationName}
					{backup.destinationKind === "local" ? (
						<Badge tone="warn" shape="capsule">
							not off-site
						</Badge>
					) : null}
				</span>
			</TableCell>
			<TableCell label="Size" className="font-mono text-ink-3 text-xs">
				{done ? formatBytes(backup.sizeBytes) : "–"}
			</TableCell>
			<TableCell pin="end">
				<span className="flex justify-end gap-1">
					{done && backup.restorable && canRestore(engine) ? (
						<Button variant="ghost" size="sm" onClick={() => onRestore(backup)}>
							Restore
						</Button>
					) : null}
					{done && backup.restorable ? (
						<Button
							variant="ghost"
							size="sm"
							aria-label={backup.locked ? "Unlock backup" : "Lock backup"}
							title={
								backup.locked
									? "Locked: retention never removes it"
									: "Lock to keep it forever"
							}
							onClick={() => onLock(backup)}
						>
							{backup.locked ? (
								<LockIcon aria-hidden />
							) : (
								<LockOpenIcon aria-hidden />
							)}
							{backup.locked ? "Locked" : "Lock"}
						</Button>
					) : null}
					{inFlight(backup.status) || backup.objectDeleted ? null : (
						<Button
							variant="ghost"
							size="sm"
							className="text-destructive-text"
							onClick={() => onDelete(backup)}
						>
							Delete
						</Button>
					)}
				</span>
			</TableCell>
		</TableRow>
	);
}

/**
 * The Backups tab of a managed database: back up now, schedules with
 * retention, the run history and restores. Mounted by the database inspector.
 */
export function BackupsPanel({
	serviceId,
	serviceSlug,
	engine,
	serverId,
}: BackupsPanelProps) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const destinations = useQuery(listBackupDestinationsOptions());
	const backups = useQuery({
		...listBackupsOptions({
			path: { serviceId },
			query: { limit: BACKUP_LIMIT },
		}),
		refetchInterval: (q) =>
			(q.state.data?.items ?? []).some((b) => inFlight(b.status))
				? IN_FLIGHT_POLL_MS
				: false,
	});
	const schedules = useQuery(
		listBackupSchedulesOptions({ path: { serviceId } }),
	);
	const restores = useQuery({
		...listBackupRestoresOptions({ path: { serviceId } }),
		refetchInterval: (q) =>
			(q.state.data?.items ?? []).some((r) => inFlight(r.status))
				? IN_FLIGHT_POLL_MS
				: false,
	});

	const dumpRestores = useQuery({
		...listDumpRestoresOptions({ path: { serviceId } }),
		refetchInterval: (q) =>
			(q.state.data?.items ?? []).some((r) => inFlight(r.status))
				? IN_FLIGHT_POLL_MS
				: false,
	});

	const runs = backups.data?.items;
	const truncated = (runs?.length ?? 0) >= BACKUP_LIMIT;
	const history = useMemo(
		() => backupDays(runs ?? [], HISTORY_DAYS, new Date(), truncated),
		[runs, truncated],
	);
	const successShare = useMemo(
		() => backupSuccessShare(runs ?? [], history),
		[runs, history],
	);

	const [nowOpen, setNowOpen] = useState(false);
	const [scheduleOpen, setScheduleOpen] = useState(false);
	const [editing, setEditing] = useState<ScheduleView | null>(null);
	const [restoring, setRestoring] = useState<BackupView | null>(null);
	const [dumpOpen, setDumpOpen] = useState(false);
	const [destinationOpen, setDestinationOpen] = useState(false);

	const del = useMutation({
		mutationFn: (b: BackupView) => deleteBackup({ path: { backupId: b.id } }),
		onSuccess: async () => {
			toast.success(
				"Deleting the stored backup",
				"The agent removes the object; the row disappears when it is gone.",
			);
			await invalidateBackups(queryClient);
		},
		onError: (e) => toast.error("Could not delete the backup", errorMessage(e)),
	});
	const lock = useMutation({
		mutationFn: (b: BackupView) =>
			lockBackup({ path: { backupId: b.id }, body: { locked: !b.locked } }),
		onSuccess: () => invalidateBackups(queryClient),
		onError: (e) => toast.error("Could not change the lock", errorMessage(e)),
	});
	const toggleSchedule = useMutation({
		mutationFn: (s: ScheduleView) =>
			updateBackupSchedule({
				path: { scheduleId: s.id },
				body: { enabled: !s.enabled },
			}),
		onSuccess: () => invalidateBackups(queryClient),
		onError: (e) =>
			toast.error("Could not change the schedule", errorMessage(e)),
	});
	const removeSchedule = useMutation({
		mutationFn: (s: ScheduleView) =>
			deleteBackupSchedule({ path: { scheduleId: s.id } }),
		onSuccess: async () => {
			toast.success("Schedule removed", "Backups it made are kept.");
			await invalidateBackups(queryClient);
		},
		onError: (e) =>
			toast.error("Could not remove the schedule", errorMessage(e)),
	});

	if (backups.isPending || destinations.isPending)
		return <Skeleton className="h-48 rounded-card" />;
	if (backups.isError && !backups.data) {
		return (
			<ErrorState
				frame="raised"
				title="Backups did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => void backups.refetch()}
			/>
		);
	}

	const noDestination = (destinations.data?.items ?? []).length === 0;
	const items = backups.data?.items ?? [];
	const scheduleItems = schedules.data?.items ?? [];
	const restoreItems = [
		...(restores.data?.items ?? []).map((r) => ({
			id: r.id,
			status: r.status,
			mode: r.mode,
			createdAt: r.createdAt,
			actor: r.actor,
			error: r.error,
			file: undefined as string | undefined,
		})),
		...(dumpRestores.data?.items ?? []).map((r) => ({
			id: r.id,
			status: r.status,
			mode: r.mode,
			createdAt: r.createdAt,
			actor: r.actor,
			error: r.error,
			file: r.fileName ?? "a dump file",
		})),
	].sort((a, b) => b.createdAt.localeCompare(a.createdAt));

	const askDelete = (b: BackupView) =>
		void confirm({
			title: "Delete this backup?",
			body: `The stored dump (${formatBytes(b.sizeBytes)}) is removed from ${b.destinationName}. This cannot be undone.`,
			cta: "Delete backup",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await del.mutateAsync(b);
			},
		});

	return (
		<div className="flex flex-col gap-6">
			<Section
				title="Backups"
				description={
					<>
						Logical dumps streamed from the database to a destination. A backup
						is proven by a restore: try one into a new database now and then.{" "}
						<DocsLink to="operations/database-backups" />
					</>
				}
				aside={
					<span className="flex items-center gap-2">
						{canRestoreDump(engine) ? (
							<Button
								variant="secondary"
								size="sm"
								onClick={() => setDumpOpen(true)}
							>
								<FileUpIcon aria-hidden />
								Restore from a file
							</Button>
						) : null}
						<Button
							variant="primary"
							size="sm"
							disabled={noDestination}
							onClick={() => setNowOpen(true)}
						>
							<DatabaseBackupIcon aria-hidden />
							Back up now
						</Button>
					</span>
				}
			>
				{!noDestination && items.length > 0 ? (
					<DitherUptimeBar
						label="Backups, last 30 days"
						days={history}
						words={BACKUP_DAY_WORDS}
						caption="succeeded"
						{...(successShare === undefined ? {} : { uptime: successShare })}
						height={28}
						cell={2}
						className="pb-3"
					/>
				) : null}
				{noDestination ? (
					<div className="flex flex-col items-start gap-3 py-2">
						<p className="text-ink-3 text-sm">
							Backups need somewhere to go: an S3-compatible bucket, or a
							directory on the server. Add one here; it is also kept in{" "}
							<Link
								to="/settings/$section"
								params={{ section: "backup-storage" }}
								className="text-accent-ink underline"
							>
								Team settings → Backup storage
							</Link>
							, where it can be edited and tested.
						</p>
						<Button
							variant="primary"
							size="sm"
							onClick={() => setDestinationOpen(true)}
						>
							<PlusIcon aria-hidden />
							Add destination…
						</Button>
					</div>
				) : items.length === 0 ? (
					<p className="py-2 text-ink-3 text-sm">
						No backups yet. Press Back up now, or add a schedule below.
					</p>
				) : (
					<div className="overflow-hidden border-line border-y">
						<Table stack>
							<TableHeader>
								<TableRow>
									<TableHead>When</TableHead>
									<TableHead>Status</TableHead>
									<TableHead>Destination</TableHead>
									<TableHead>Size</TableHead>
									<TableHead className="w-px" />
								</TableRow>
							</TableHeader>
							<TableBody>
								{items.map((b) => (
									<BackupRow
										key={b.id}
										backup={b}
										engine={engine}
										onRestore={setRestoring}
										onDelete={askDelete}
										onLock={(x) => lock.mutate(x)}
									/>
								))}
							</TableBody>
						</Table>
					</div>
				)}
				{canRestore(engine) ? null : (
					<p className="py-2 text-ink-3 text-xs">
						Dumps of this engine are RDB snapshots. They are real backups, but
						NebulaCtrl does not load them back yet: place the file as{" "}
						<code className="type-mono">dump.rdb</code> in the data volume of a
						stopped server.
					</p>
				)}
			</Section>

			<Section
				title="Schedules"
				description="Runs while the database is running; skipped runs are recorded with the reason."
				aside={
					<Button
						variant="secondary"
						size="sm"
						disabled={noDestination}
						onClick={() => setScheduleOpen(true)}
						title={noDestination ? "Add a backup destination first" : undefined}
					>
						<PlusIcon aria-hidden />
						Add schedule
					</Button>
				}
			>
				{scheduleItems.length === 0 ? (
					<p className="py-2 text-ink-3 text-sm">
						No schedule. Add one to back up automatically.
					</p>
				) : (
					scheduleItems.map((s) => (
						<div
							key={s.id}
							className="flex flex-wrap items-center justify-between gap-3 py-2"
						>
							<div className="flex min-w-0 flex-col gap-0.5">
								<span className="font-medium text-ink-1 text-sm">
									{describeCron(s.cron)}
								</span>
								<span className="text-ink-3 text-xs">
									<code className="type-mono">{s.cron}</code> · {s.timezone} →{" "}
									{s.destinationName} ·{" "}
									{describeRetention(s.retentionCount, s.retentionDays)}
								</span>
								<span className="text-ink-4 text-xs">
									{s.enabled && s.nextRunAt ? (
										<>
											Next run <Relative iso={s.nextRunAt} />
										</>
									) : (
										"Paused"
									)}
									{s.lastRunAt ? (
										<>
											{" "}
											· last run <Relative iso={s.lastRunAt} />
										</>
									) : null}
								</span>
							</div>
							<div className="flex items-center gap-2">
								<Switch
									size="sm"
									aria-label={s.enabled ? "Pause schedule" : "Resume schedule"}
									checked={s.enabled}
									onCheckedChange={() => toggleSchedule.mutate(s)}
								/>
								<Button
									variant="ghost"
									size="sm"
									onClick={() => {
										setEditing(s);
										setScheduleOpen(true);
									}}
								>
									Edit
								</Button>
								<Button
									variant="ghost"
									size="sm"
									className="text-destructive-text"
									onClick={() =>
										void confirm({
											title: "Remove this schedule?",
											body: "Backups it already made are kept, but retention no longer manages them.",
											cta: "Remove schedule",
											danger: true,
											hold: false,
											onConfirm: async () => {
												await removeSchedule.mutateAsync(s);
											},
										})
									}
								>
									Remove
								</Button>
							</div>
						</div>
					))
				)}
			</Section>

			{restoreItems.length > 0 ? (
				<Section
					title="Restores"
					description="Recent restores into or from this database."
				>
					{restoreItems.map((r) => {
						const p = restorePresentation(r.status);
						return (
							<div
								key={r.id}
								className="flex flex-wrap items-center justify-between gap-3 py-2"
							>
								<div className="flex flex-col gap-1">
									<StatusPill tone={p.tone} pulse={p.pulse}>
										{p.label}
									</StatusPill>
									<span className="text-ink-3 text-xs">
										{r.file ? `From ${r.file} · ` : ""}
										{r.mode === "new" ? "Into a new database" : "In place"} ·{" "}
										<Relative iso={r.createdAt} />
										{r.actor ? ` · ${r.actor}` : ""}
									</span>
									{r.error ? (
										<span
											className="max-w-[480px] text-destructive-text text-xs"
											title={r.error}
										>
											{r.error}
										</span>
									) : null}
								</div>
							</div>
						);
					})}
				</Section>
			) : null}

			<BackupNowDialog
				open={nowOpen}
				onOpenChange={setNowOpen}
				serviceId={serviceId}
			/>
			<ScheduleDialog
				open={scheduleOpen}
				onOpenChange={(o) => {
					setScheduleOpen(o);
					if (!o) setEditing(null);
				}}
				serviceId={serviceId}
				schedule={editing}
			/>
			<DestinationDialog
				open={destinationOpen}
				onOpenChange={setDestinationOpen}
				destination={null}
			/>
			<DumpRestoreDialog
				open={dumpOpen}
				onClose={() => setDumpOpen(false)}
				serviceId={serviceId}
				serviceSlug={serviceSlug}
				engine={engine}
				serverId={serverId}
				scheduleDestinationId={scheduleItems[0]?.destinationId}
			/>
			<RestoreDialog
				backup={restoring}
				serviceSlug={serviceSlug}
				serverId={serverId}
				onClose={() => setRestoring(null)}
			/>
		</div>
	);
}
