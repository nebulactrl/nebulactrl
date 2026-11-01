import {
	FolderOpenIcon,
	LockIcon,
	LockOpenIcon,
	Trash2Icon,
} from "lucide-react";
import type { VolumeBackupView } from "#/api/gen/types.gen";
import { Relative } from "#/components/common/relative";
import { ToneIcon } from "#/components/common/tone-icon";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { StatusPill } from "#/components/ui/lamp";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import {
	backupPresentation,
	inFlight,
	TRIGGER_LABEL,
} from "#/features/backups/model";
import { absoluteTime, formatBytes } from "#/lib/format";
import {
	backupSummary,
	hookNote,
	modeLabel,
	shortSnapshotId,
	volumesLine,
} from "./model";

export interface HistoryRowActions {
	onRestore: (b: VolumeBackupView) => void;
	onBrowse: (b: VolumeBackupView) => void;
	onLock: (b: VolumeBackupView) => void;
	onDelete: (b: VolumeBackupView) => void;
}

function HistoryRow({
	backup,
	actions,
}: {
	backup: VolumeBackupView;
	actions: HistoryRowActions;
}) {
	const p = backupPresentation(backup.status);
	const done = backup.status === "succeeded";
	const warnings = backup.warnings ?? [];
	const note = hookNote(backup);
	const used = backup.consistency || backup.mode;
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
				<span className="flex min-w-0 flex-col gap-1">
					<StatusPill tone={p.tone} pulse={p.pulse}>
						{p.label}
					</StatusPill>
					<span
						className={
							backup.status === "failed" || backup.status === "skipped"
								? "line-clamp-2 max-w-[420px] text-destructive-text text-xs"
								: "max-w-[420px] text-ink-3 text-xs"
						}
						title={backupSummary(backup)}
					>
						{backupSummary(backup)}
					</span>
					{done || backup.status === "running" ? (
						<span className="text-2xs text-ink-4">
							{modeLabel(used)}
							{backup.volumes?.length
								? ` · ${volumesLine(backup.volumes)}`
								: ""}
							{done && backup.files > 0
								? ` · ${backup.files.toLocaleString("en-US")} files`
								: ""}
						</span>
					) : null}
					{note ? (
						<span className="flex items-start gap-1.5 text-status-staged-on text-xs">
							<ToneIcon tone="warning" className="mt-px size-3.5" />
							{note}
						</span>
					) : null}
					{warnings.map((w) => (
						<span
							key={w}
							className="flex items-start gap-1.5 text-status-staged-on text-xs"
						>
							<ToneIcon tone="warning" className="mt-px size-3.5" />
							{w}
						</span>
					))}
				</span>
			</TableCell>
			<TableCell label="Snapshot" className="text-ink-3 text-xs">
				<span className="flex flex-col gap-0.5">
					{backup.snapshotId ? (
						<code className="type-mono" title={backup.snapshotId}>
							{shortSnapshotId(backup.snapshotId)}
						</code>
					) : (
						<span className="text-ink-4">–</span>
					)}
					<span className="flex items-center gap-1.5">
						{backup.destinationName}
						{backup.destinationKind === "local" ? (
							<Badge tone="warn" shape="capsule">
								not off-site
							</Badge>
						) : null}
					</span>
				</span>
			</TableCell>
			<TableCell label="Size" className="font-mono text-ink-3 text-xs">
				{done ? formatBytes(backup.sizeBytes) : "–"}
			</TableCell>
			<TableCell pin="end">
				<span className="flex flex-wrap justify-end gap-1">
					{done && backup.restorable ? (
						<>
							<Button
								variant="ghost"
								size="sm"
								onClick={() => actions.onRestore(backup)}
							>
								Restore
							</Button>
							<Button
								variant="ghost"
								size="sm"
								onClick={() => actions.onBrowse(backup)}
							>
								<FolderOpenIcon aria-hidden />
								Browse files
							</Button>
							<Button
								variant="ghost"
								size="sm"
								aria-label={backup.locked ? "Unlock backup" : "Lock backup"}
								title={
									backup.locked
										? "Locked: retention never removes it"
										: "Lock to keep it forever"
								}
								onClick={() => actions.onLock(backup)}
							>
								{backup.locked ? (
									<LockIcon aria-hidden />
								) : (
									<LockOpenIcon aria-hidden />
								)}
								{backup.locked ? "Locked" : "Lock"}
							</Button>
						</>
					) : null}
					{inFlight(backup.status) || backup.objectDeleted ? null : (
						<Button
							variant="ghost"
							size="sm"
							className="text-destructive-text"
							aria-label="Delete backup"
							onClick={() => actions.onDelete(backup)}
						>
							<Trash2Icon aria-hidden />
							Delete
						</Button>
					)}
				</span>
			</TableCell>
		</TableRow>
	);
}

/** The run history, newest first. */
export function HistoryTable({
	items,
	actions,
}: {
	items: readonly VolumeBackupView[];
	actions: HistoryRowActions;
}) {
	return (
		<div className="overflow-hidden border-line border-y">
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>When</TableHead>
						<TableHead>Status</TableHead>
						<TableHead>Snapshot</TableHead>
						<TableHead>Size</TableHead>
						<TableHead className="w-px" />
					</TableRow>
				</TableHeader>
				<TableBody>
					{items.map((b) => (
						<HistoryRow key={b.id} backup={b} actions={actions} />
					))}
				</TableBody>
			</Table>
		</div>
	);
}
