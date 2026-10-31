import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { purgeTeamSlice } from "#/api/gen/sdk.gen";
import type { InstanceServicesView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Progress } from "#/components/ui/progress";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { SettingsPlate } from "#/features/settings/section";
import { absoluteDate, count } from "#/lib/format";
import {
	canEditQuota,
	purgeIn,
	quotaSource,
	sizeText,
	sliceStatus,
	type TeamRow,
	teamRows,
	totals,
	usageNote,
	usedPercent,
	WARN_PERCENT,
} from "./model";
import { invalidateInstanceServices } from "./queries";
import { QuotaDialog } from "./quota-dialog";

function Usage({ row }: { row: TeamRow }) {
	const slice = row.backup;
	if (!slice) return <span className="text-ink-4 text-xs">None yet</span>;
	if (row.deleted) {
		return (
			<span className="font-mono text-ink-3 text-xs tabular-nums">
				{sizeText(slice.usedBytes)} kept
			</span>
		);
	}
	const percent = usedPercent(slice.usedBytes, slice.effectiveQuotaBytes);
	const note = usageNote(row);
	return (
		<div className="flex w-full min-w-[10rem] flex-col gap-1.5">
			<span className="flex items-baseline justify-between gap-3 font-mono text-xs tabular-nums">
				<span className="text-ink-1">
					{sizeText(slice.usedBytes)}{" "}
					<span className="text-ink-3">
						of {sizeText(slice.effectiveQuotaBytes)}
					</span>
				</span>
				<span className="text-ink-3">{percent}%</span>
			</span>
			<Progress
				value={Math.min(percent, 100)}
				warnAt={WARN_PERCENT}
				aria-label={`Backup storage used by ${row.teamName}`}
			/>
			{note ? <span className="text-2xs text-warning-text">{note}</span> : null}
		</div>
	);
}

function State({ row, now }: { row: TeamRow; now: number }) {
	if (row.deleted) {
		return (
			<span className="flex flex-col gap-0.5">
				<Badge tone="warn" shape="capsule">
					Team deleted
				</Badge>
				<span
					className="text-ink-3 text-xs"
					title={
						row.purgeAt ? `Purged on ${absoluteDate(row.purgeAt)}` : undefined
					}
				>
					{row.purgeAt
						? `Purged ${absoluteDate(row.purgeAt)} (${purgeIn(row.purgeAt, now)})`
						: "Purge date unknown"}
				</span>
			</span>
		);
	}
	const status = sliceStatus(row.state);
	return (
		<span className="flex flex-col gap-0.5">
			<span className="flex items-center gap-1.5 text-ink-1 text-xs">
				<StatusIcon kind={status.kind} label="" />
				{status.label}
			</span>
			{row.state === "failed" && row.message !== "" ? (
				<span className="max-w-[34ch] break-words text-destructive-text text-xs">
					{row.message}
				</span>
			) : null}
		</span>
	);
}

/**
 * Every team's slice: how much of its quota the backups use, whether the slice
 * works, and the quota edit. A deleted team's slice stays until its purge date;
 * "Delete now" removes it at once, after typing the team's name.
 */
export function TeamsPlate({ view }: { view: InstanceServicesView }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [editing, setEditing] = useState<TeamRow | null>(null);
	const rows = teamRows(view.slices ?? []);
	const now = Date.now();
	const sums = totals(view);
	const summary = `${count(sums.teams, "team")} · ${sizeText(sums.usedBytes)} of backups${
		sums.deleted > 0 ? ` · ${sums.deleted} deleted, waiting to be purged` : ""
	}`;

	const purge = useMutation({
		mutationFn: async (row: TeamRow) => {
			await purgeTeamSlice({
				path: { teamId: row.teamId },
				query: { confirm: row.teamName },
				throwOnError: true,
			});
			return row;
		},
		onSuccess: async (row) => {
			toast.success(
				`Deleted ${row.teamName}'s built-in storage`,
				"Its backups, images and logs are gone.",
			);
			await invalidateInstanceServices(queryClient);
		},
	});

	const askPurge = (row: TeamRow) =>
		void confirm({
			title: `Delete ${row.teamName}'s data now?`,
			body: (
				<span className="flex flex-col gap-2">
					<span>
						This deletes the team's backup bucket, its registry images and its
						central logs
						{row.backup
							? ` (${sizeText(row.backup.usedBytes)} of backups)`
							: ""}
						. It cannot be undone.
					</span>
					<span>
						Without this the data is kept until{" "}
						{row.purgeAt ? absoluteDate(row.purgeAt) : "its purge date"}.
					</span>
				</span>
			),
			cta: "Delete now",
			danger: true,
			hold: false,
			typeToConfirm: row.teamName,
			onConfirm: async () => {
				await purge.mutateAsync(row);
			},
		});

	return (
		<SettingsPlate
			id="teams"
			title="Teams"
			flush
			tip="Each team has its own bucket, with a key that can only touch that bucket, and its own share of the registry and the logs. Teams never see the keys. When a team is deleted, its slice is kept for 30 days and then deleted."
			tipLabel="About team slices"
		>
			{rows.length === 0 ? (
				<p className="m-0 px-5 py-5 text-ink-3 text-sm">
					No team has a slice yet. A team gets one when it is created, and the
					check above creates any that are missing.
				</p>
			) : (
				<Table stack>
					<TableHeader>
						<TableRow>
							<TableHead>Team</TableHead>
							<TableHead>Backup storage</TableHead>
							<TableHead>State</TableHead>
							<TableHead className="w-px" />
						</TableRow>
					</TableHeader>
					<TableBody>
						{rows.map((row) => (
							<TableRow key={row.teamId}>
								<TableCell pin="start">
									<span className="flex flex-col gap-0.5">
										<span className="font-medium text-ink-1">
											{row.teamName}
										</span>
										{row.backup && !row.deleted ? (
											<span className="text-2xs text-ink-4">
												Quota: {quotaSource(row.backup)}
											</span>
										) : null}
									</span>
								</TableCell>
								<TableCell label="Backup storage">
									<Usage row={row} />
								</TableCell>
								<TableCell label="State">
									<State row={row} now={now} />
								</TableCell>
								<TableCell pin="end">
									<span className="flex justify-end gap-1">
										{canEditQuota(row) ? (
											<Button
												variant="ghost"
												size="sm"
												onClick={() => setEditing(row)}
											>
												Edit quota
											</Button>
										) : null}
										{row.deleted ? (
											<Button
												variant="ghost"
												size="sm"
												className="text-destructive-text"
												disabled={purge.isPending}
												onClick={() => askPurge(row)}
											>
												Delete now
											</Button>
										) : null}
									</span>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			)}
			{rows.length > 0 ? (
				<p className="m-0 border-line-faint border-t px-5 py-3 text-ink-3 text-xs">
					{summary}
				</p>
			) : null}
			<QuotaDialog
				row={editing}
				defaultQuotaBytes={view.defaultQuotaBytes}
				onClose={() => setEditing(null)}
			/>
		</SettingsPlate>
	);
}
