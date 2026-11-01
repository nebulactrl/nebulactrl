import { DitherUptimeBar } from "@nebulactrl/dither/charts/uptime-bar";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DatabaseBackupIcon } from "lucide-react";
import { useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	getVolumeBackupConfigOptions,
	listBackupDestinationsOptions,
	listVolumeBackupsOptions,
	listVolumeRestoresOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import {
	createVolumeBackup,
	deleteVolumeBackup,
	lockVolumeBackup,
} from "#/api/gen/sdk.gen";
import type { VolumeBackupView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { DocsLink } from "#/components/docs/docs-link";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { StatusPill } from "#/components/ui/lamp";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { DestinationDialog } from "#/features/backups/destination-dialog";
import {
	BACKUP_DAY_WORDS,
	backupDays,
	backupSuccessShare,
	describeCron,
	inFlight,
} from "#/features/backups/model";
import { Section } from "#/features/service-settings/fields";
import { formatBytes } from "#/lib/format";
import { ConfigForm } from "./config-form";
import { FilesDialog } from "./files-dialog";
import { HistoryTable } from "./history";
import {
	describeKeep,
	restoreModeLabel,
	restoreStatusLabel,
	scheduleLine,
	stagedPointer,
} from "./model";
import {
	IN_FLIGHT_POLL_MS,
	invalidateVolumeBackups,
	VOLUME_BACKUP_LIMIT,
} from "./queries";
import { VolumeRestoreDialog } from "./restore-dialog";

/** Days the history strip above the list covers. */
const HISTORY_DAYS = 30;

export interface VolumeBackupsPanelProps {
	serviceId: string;
	serviceSlug: string;
	/** Changes staged on the service: the number on the Deploy key. */
	stagedCount: number;
}

/**
 * The Backups tab of a service with volumes: the settings, the run history
 * and the restores. File-level backups of the service's volumes, uploaded
 * encrypted to a destination.
 */
export function VolumeBackupsPanel({
	serviceId,
	serviceSlug,
	stagedCount,
}: VolumeBackupsPanelProps) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const config = useQuery(
		getVolumeBackupConfigOptions({ path: { serviceId } }),
	);
	const destinations = useQuery(listBackupDestinationsOptions());
	const backups = useQuery({
		...listVolumeBackupsOptions({
			path: { serviceId },
			query: { limit: VOLUME_BACKUP_LIMIT },
		}),
		refetchInterval: (q) =>
			(q.state.data?.items ?? []).some((b) => inFlight(b.status))
				? IN_FLIGHT_POLL_MS
				: false,
	});
	const restores = useQuery({
		...listVolumeRestoresOptions({ path: { serviceId } }),
		refetchInterval: (q) =>
			(q.state.data?.items ?? []).some((r) => inFlight(r.status))
				? IN_FLIGHT_POLL_MS
				: false,
	});

	const runs = backups.data?.items;
	const truncated = (runs?.length ?? 0) >= VOLUME_BACKUP_LIMIT;
	const history = useMemo(
		() => backupDays(runs ?? [], HISTORY_DAYS, new Date(), truncated),
		[runs, truncated],
	);
	const successShare = useMemo(
		() => backupSuccessShare(runs ?? [], history),
		[runs, history],
	);

	const [restoring, setRestoring] = useState<VolumeBackupView | null>(null);
	const [browsing, setBrowsing] = useState<VolumeBackupView | null>(null);
	const [destinationOpen, setDestinationOpen] = useState(false);

	const start = useMutation({
		mutationFn: () => createVolumeBackup({ path: { serviceId }, body: {} }),
		onSuccess: async () => {
			toast.success(
				"Backup started",
				"The files are copied locally first, then uploaded in the background.",
			);
			await invalidateVolumeBackups(queryClient);
		},
		onError: (e) => toast.error("Could not start the backup", errorMessage(e)),
	});
	const del = useMutation({
		mutationFn: (b: VolumeBackupView) =>
			deleteVolumeBackup({ path: { backupId: b.id } }),
		onSuccess: async () => {
			toast.success(
				"Deleting the stored backup",
				"The agent removes the snapshot; the row stays as history.",
			);
			await invalidateVolumeBackups(queryClient);
		},
		onError: (e) => toast.error("Could not delete the backup", errorMessage(e)),
	});
	const lock = useMutation({
		mutationFn: (b: VolumeBackupView) =>
			lockVolumeBackup({
				path: { backupId: b.id },
				body: { locked: !b.locked },
			}),
		onSuccess: () => invalidateVolumeBackups(queryClient),
		onError: (e) => toast.error("Could not change the lock", errorMessage(e)),
	});

	if (config.isPending || backups.isPending || destinations.isPending)
		return <Skeleton className="h-48 rounded-card" />;
	if ((config.isError && !config.data) || (backups.isError && !backups.data)) {
		return (
			<ErrorState
				frame="raised"
				title="Volume backups did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => {
					void config.refetch();
					void backups.refetch();
				}}
			/>
		);
	}

	const cfg = config.data;
	if (!cfg) return null;
	if (!cfg.eligible) {
		return (
			<Section
				title="Volume backups"
				description="Back up the files in this service's volumes to S3-compatible storage or a folder on the server."
			>
				<Alert variant="info">
					<AlertTitle>Volume backups are not available</AlertTitle>
					<AlertDescription>
						{cfg.ineligibleReason ||
							"This service needs a server and at least one volume."}
					</AlertDescription>
				</Alert>
			</Section>
		);
	}

	const items = backups.data?.items ?? [];
	const restoreItems = restores.data?.items ?? [];
	const destinationItems = destinations.data?.items ?? [];
	const volumeNames = (cfg.volumes ?? []).map((v) => v.name);
	const backupTime = new Map(items.map((b) => [b.id, b.createdAt]));

	const askDelete = (b: VolumeBackupView) =>
		void confirm({
			title: "Delete this backup?",
			body: `The snapshot (${formatBytes(b.sizeBytes)} of files) is removed from ${b.destinationName ?? "the destination"}. Data other backups share stays. This cannot be undone.`,
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
				title="Volume backups"
				description={
					<>
						Back up the files in this service's volumes to S3-compatible storage
						or a folder on the server. Each backup is one snapshot of all its
						volumes, encrypted before upload, and only what changed is uploaded.{" "}
						<DocsLink to="operations/backups" />
					</>
				}
				aside={
					<Button
						variant="primary"
						size="sm"
						disabled={!cfg.configured || start.isPending}
						title={cfg.configured ? undefined : "Save the settings below first"}
						onClick={() => start.mutate()}
					>
						{start.isPending ? <Spinner /> : <DatabaseBackupIcon aria-hidden />}
						Back up now
					</Button>
				}
			>
				{cfg.configured ? (
					<p className="py-1 text-ink-3 text-xs">
						{scheduleLine(cfg.cron, cfg.timezone, describeCron)}
						{cfg.cron.trim() !== "" && cfg.enabled && cfg.nextRunAt ? (
							<>
								{" "}
								· next run <Relative iso={cfg.nextRunAt} />
							</>
						) : cfg.cron.trim() !== "" ? (
							" · paused"
						) : null}
						{cfg.lastRunAt ? (
							<>
								{" "}
								· last run <Relative iso={cfg.lastRunAt} />
							</>
						) : null}
						{" · "}
						{describeKeep(cfg.keepDaily, cfg.keepWeekly, cfg.keepMonthly)}
					</p>
				) : null}
				<ConfigForm
					serviceId={serviceId}
					serviceSlug={serviceSlug}
					config={cfg}
					destinations={destinationItems}
					onAddDestination={() => setDestinationOpen(true)}
				/>
			</Section>

			<Section
				title="History"
				description="Newest first. Restore into a new volume, browse and download single files, or lock a backup so retention never removes it."
			>
				{items.length > 0 ? (
					<DitherUptimeBar
						label="Volume backups, last 30 days"
						days={history}
						words={BACKUP_DAY_WORDS}
						caption="succeeded"
						{...(successShare === undefined ? {} : { uptime: successShare })}
						height={28}
						cell={2}
						className="pb-3"
					/>
				) : null}
				{items.length === 0 ? (
					<p className="py-2 text-ink-3 text-sm">
						No backups yet.{" "}
						{cfg.configured
							? "Press Back up now, or set a schedule above."
							: "Save the settings above, then press Back up now."}
					</p>
				) : (
					<HistoryTable
						items={items}
						actions={{
							onRestore: setRestoring,
							onBrowse: setBrowsing,
							onLock: (b) => lock.mutate(b),
							onDelete: askDelete,
						}}
					/>
				)}
			</Section>

			{restoreItems.length > 0 ? (
				<Section
					title="Restores"
					description="Recent restores of this service's volumes."
				>
					{restoreItems.map((r) => {
						const p = restoreStatusLabel(r.status, r.swapStaged);
						return (
							<div key={r.id} className="flex flex-col gap-1 py-2">
								<span className="flex flex-wrap items-center gap-2">
									<StatusPill tone={p.tone}>{p.label}</StatusPill>
									<span className="text-ink-3 text-xs">
										<code className="type-mono">{r.volumeName}</code> ·{" "}
										{restoreModeLabel(r.mode)} · <Relative iso={r.createdAt} />
										{r.actor ? ` · ${r.actor}` : ""}
									</span>
								</span>
								{r.swapStaged ? (
									<span className="text-ink-2 text-xs">
										{stagedPointer(
											r.volumeName,
											backupTime.get(r.backupId),
											stagedCount,
										)}
									</span>
								) : null}
								{r.error ? (
									<span
										className="max-w-[480px] text-destructive-text text-xs"
										title={r.error}
									>
										{r.error}
									</span>
								) : null}
							</div>
						);
					})}
				</Section>
			) : null}

			<VolumeRestoreDialog
				backup={restoring}
				serviceSlug={serviceSlug}
				currentVolumes={volumeNames}
				onClose={() => setRestoring(null)}
			/>
			<FilesDialog backup={browsing} onClose={() => setBrowsing(null)} />
			<DestinationDialog
				open={destinationOpen}
				onOpenChange={setDestinationOpen}
				destination={null}
			/>
		</div>
	);
}
