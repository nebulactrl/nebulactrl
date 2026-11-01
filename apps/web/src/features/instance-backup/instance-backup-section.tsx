import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CloudIcon, HardDriveIcon, PlayIcon, ServerIcon } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listInstanceBackupRunsOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	runInstanceBackup,
	testInstanceBackup,
	updateInstanceBackup,
} from "#/api/gen/sdk.gen";
import type { InstanceBackupView, InstanceRunView } from "#/api/gen/types.gen";
import { CodeBlock } from "#/components/common/code-block";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
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
import { absoluteTime, formatBytes } from "#/lib/format";
import { InstanceDestinationDialog } from "./destination-dialog";
import { SecretKeyReminder } from "./key-reminder";
import {
	dailyCron,
	dailyTime,
	destKindLabel,
	NOT_OFF_SITE_WARNING,
	RETENTION_MAX,
	RETENTION_MIN,
	retentionSummary,
	runPresentation,
	scheduleSummary,
	TRIGGER_LABEL,
} from "./model";
import { PreUpgradeBackupNote } from "./pre-upgrade-note";
import {
	instanceBackupQuery,
	invalidateInstanceBackup,
	RUNNING_POLL_MS,
} from "./queries";

const RUNS_SHOWN = 20;

function TestState({ settings }: { settings: InstanceBackupView }) {
	const d = settings.destination;
	if (d.lastTestOk === undefined) {
		return (
			<span className="flex items-center gap-2 text-ink-3 text-xs">
				<StatusIcon kind="pending" label="" /> Not tested
			</span>
		);
	}
	return (
		<span className="flex flex-col gap-0.5">
			<span className="flex items-center gap-2 text-ink-2 text-xs">
				<StatusIcon kind={d.lastTestOk ? "healthy" : "failed"} label="" />
				{d.lastTestOk ? "Works" : "Failed"}
				{d.lastTestAt ? (
					<span className="text-ink-4">
						· <Relative iso={d.lastTestAt} />
					</span>
				) : null}
			</span>
			{d.lastTestOk ? null : (
				<span
					className="max-w-[420px] break-words text-destructive-text text-xs"
					title={d.lastTestMessage}
				>
					{d.lastTestMessage}
				</span>
			)}
		</span>
	);
}

/** The glyph of a destination kind: a cloud, this host's disk, or the built-in server. */
function DestIcon({
	kind,
	className,
}: {
	kind: "s3" | "local" | "builtin";
	className: string;
}) {
	if (kind === "s3") return <CloudIcon aria-hidden className={className} />;
	if (kind === "builtin")
		return <ServerIcon aria-hidden className={className} />;
	return <HardDriveIcon aria-hidden className={className} />;
}

function RunRow({ run }: { run: InstanceRunView }) {
	const p = runPresentation(run.status);
	return (
		<TableRow>
			<TableCell pin="start">
				<span className="flex flex-col gap-0.5">
					<span
						className="text-ink-1 text-xs"
						title={absoluteTime(run.startedAt)}
					>
						<Relative iso={run.startedAt} />
					</span>
					<span className="text-2xs text-ink-4">
						{TRIGGER_LABEL[run.trigger] ?? run.trigger}
					</span>
				</span>
			</TableCell>
			<TableCell label="Status">
				<span className="flex flex-col gap-1">
					<span className="flex items-center gap-2 text-ink-2 text-xs">
						<StatusIcon kind={p.kind} label="" />
						{p.label}
						{run.objectDeleted ? (
							<Badge variant="outline" shape="capsule">
								removed by retention
							</Badge>
						) : null}
					</span>
					{run.error ? (
						<span
							className="line-clamp-3 max-w-[420px] break-words text-destructive-text text-xs"
							title={run.error}
						>
							{run.error}
						</span>
					) : null}
				</span>
			</TableCell>
			<TableCell label="Destination" className="text-ink-3 text-xs">
				<span className="flex items-center gap-1.5">
					<DestIcon kind={run.destKind} className="size-3.5" />
					<span className="break-all font-mono">{run.destination}</span>
				</span>
			</TableCell>
			<TableCell label="Size" className="font-mono text-ink-3 text-xs">
				{run.status === "succeeded" ? formatBytes(run.sizeBytes) : "–"}
			</TableCell>
		</TableRow>
	);
}

/**
 * Admin → Control-plane backup: a scheduled pg_dump of the
 * control plane's own database to one instance-wide destination. Instance admin
 * only. The secret key is never in the dump; the reminder above says so until an
 * admin confirms it is stored elsewhere.
 */
export function InstanceBackupSection() {
	const queryClient = useQueryClient();
	const settings = useQuery(instanceBackupQuery());
	const data = settings.data;
	const running = data?.lastRun?.status === "running";
	const runs = useQuery({
		...listInstanceBackupRunsOptions({ query: { limit: RUNS_SHOWN } }),
		enabled: data !== undefined,
		refetchInterval: running ? RUNNING_POLL_MS : false,
	});
	const [editing, setEditing] = useState(false);
	const [time, setTime] = useState("");
	const [retention, setRetention] = useState("");
	const [scheduleError, setScheduleError] = useState<string | null>(null);

	useEffect(() => {
		if (!data) return;
		setTime(dailyTime(data.schedule) ?? "");
		setRetention(String(data.retentionCount));
		setScheduleError(null);
	}, [data]);

	const save = useMutation({
		mutationFn: (body: Parameters<typeof updateInstanceBackup>[0]["body"]) =>
			updateInstanceBackup({ body }),
		onSuccess: () => invalidateInstanceBackup(queryClient),
	});
	const test = useMutation({
		mutationFn: () => testInstanceBackup(),
		onSuccess: async (res) => {
			await invalidateInstanceBackup(queryClient);
			if (res.data?.destination.lastTestOk) {
				toast.success(
					"Destination works",
					"Wrote, read back and deleted a probe object.",
				);
			} else {
				toast.error(
					"Destination test failed",
					res.data?.destination.lastTestMessage,
				);
			}
		},
		onError: (e) =>
			toast.error("Could not test the destination", errorMessage(e)),
	});
	const run = useMutation({
		mutationFn: () => runInstanceBackup(),
		onSuccess: async () => {
			toast.success("Backup started", "It runs in the background.");
			await invalidateInstanceBackup(queryClient);
		},
		onError: (e) => toast.error("Could not start the backup", errorMessage(e)),
	});

	if (settings.isPending) return <Skeleton className="h-64 rounded-card" />;
	if (!data) {
		return (
			<ErrorState
				frame="raised"
				title="Backup settings did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => void settings.refetch()}
			/>
		);
	}

	const dest = data.destination;
	const toggle = (next: boolean) =>
		save.mutate(
			{ enabled: next },
			{
				onSuccess: () =>
					toast.success(
						next ? "Daily backups are on" : "Daily backups are off",
					),
				onError: (e) => toast.error("Could not change it", errorMessage(e)),
			},
		);

	const submitSchedule = (event: FormEvent) => {
		event.preventDefault();
		if (save.isPending) return;
		setScheduleError(null);
		const count = Number.parseInt(retention, 10);
		if (
			!Number.isInteger(count) ||
			count < RETENTION_MIN ||
			count > RETENTION_MAX
		) {
			setScheduleError(
				`Keep between ${RETENTION_MIN} and ${RETENTION_MAX} backups.`,
			);
			return;
		}
		const body: Parameters<typeof updateInstanceBackup>[0]["body"] = {
			retentionCount: count,
		};
		if (time.trim() !== "") {
			const cron = dailyCron(time);
			if (!cron) {
				setScheduleError(
					"Enter the time as HH:MM, 24 hour, for example 03:30.",
				);
				return;
			}
			body.schedule = cron;
		}
		save.mutate(body, {
			onSuccess: () => toast.success("Schedule saved"),
			onError: (e) => setScheduleError(errorMessage(e)),
		});
	};

	const items = runs.data?.items ?? [];
	const where = destKindLabel(dest.kind);
	const canEnable = dest.configured && data.toolsAvailable;

	return (
		<div className="flex flex-col gap-6">
			<SecretKeyReminder />

			<p className="type-secondary text-ink-3">
				Your services' data is not included, and neither is the instance secret
				key.
			</p>

			{data.toolsAvailable ? null : (
				<Alert variant="warning">
					<AlertTitle>Backups cannot run on this host</AlertTitle>
					<AlertDescription>
						{data.toolsMessage ??
							"The tool that dumps the database is not installed on the control-plane host."}
					</AlertDescription>
				</Alert>
			)}

			<div className="surface-well flex items-start justify-between gap-4 rounded-card p-4">
				<div className="flex min-w-0 flex-col gap-1">
					<label
						htmlFor="instance-backup-enabled"
						className="font-medium text-ink-1 text-sm"
					>
						Back up every day
					</label>
					<p className="max-w-[56ch] text-ink-3 text-xs leading-relaxed">
						{data.enabled
							? `${scheduleSummary(data.schedule)}. ${retentionSummary(data.retentionCount)}`
							: canEnable
								? "Off. Turn it on to back up on a schedule."
								: "Off. Set up a destination first."}
					</p>
					{data.enabled && data.nextRunAt ? (
						<p className="text-ink-3 text-xs">
							Next backup <Relative iso={data.nextRunAt} />
							<span className="text-ink-4">
								{" "}
								· {absoluteTime(data.nextRunAt)}
							</span>
						</p>
					) : null}
					<p className="flex items-center gap-2 text-ink-3 text-xs">
						{data.lastRun ? (
							<>
								<StatusIcon
									kind={runPresentation(data.lastRun.status).kind}
									label=""
								/>
								Last backup:{" "}
								{runPresentation(data.lastRun.status).label.toLowerCase()}{" "}
								<Relative iso={data.lastRun.startedAt} />
							</>
						) : (
							"No backup has run yet."
						)}
					</p>
				</div>
				<Switch
					id="instance-backup-enabled"
					checked={data.enabled}
					disabled={save.isPending || (!data.enabled && !canEnable)}
					onCheckedChange={toggle}
				/>
			</div>

			<section className="flex flex-col gap-3" aria-labelledby="ib-dest-title">
				<h4 id="ib-dest-title" className="font-medium text-ink-1 text-sm">
					Destination
				</h4>
				{data.notOffSite && dest.kind === "builtin" ? (
					<Alert variant="warning">
						<AlertTitle>Not off-site</AlertTitle>
						<AlertDescription>{NOT_OFF_SITE_WARNING}</AlertDescription>
					</Alert>
				) : null}
				<div className="surface-well flex flex-wrap items-start justify-between gap-3 rounded-card p-4">
					<div className="flex min-w-0 flex-col gap-1.5">
						<span className="flex flex-wrap items-center gap-2 text-ink-1 text-sm">
							<DestIcon kind={dest.kind} className="size-4 text-ink-3" />
							{where}
							{dest.offSite ? null : (
								<Badge tone="warn" shape="capsule">
									not off-site
								</Badge>
							)}
						</span>
						<span className="break-all font-mono text-ink-3 text-xs">
							{dest.where}
						</span>
						{dest.configured ? (
							<TestState settings={data} />
						) : (
							<span className="text-ink-3 text-xs">
								{dest.kind === "local" && !data.localDirEnabled
									? "NEBULACTRL_BACKUP_DIR is not set on the control plane, so a directory is unavailable."
									: "Not set up yet."}
							</span>
						)}
					</div>
					<div className="flex gap-1">
						<Button
							variant="ghost"
							size="sm"
							disabled={!dest.configured || test.isPending}
							onClick={() => test.mutate()}
						>
							{test.isPending ? <Spinner /> : null}
							Test
						</Button>
						<Button
							variant="secondary"
							size="sm"
							onClick={() => setEditing(true)}
						>
							{dest.configured ? "Edit" : "Set up"}
						</Button>
					</div>
				</div>
			</section>

			<form onSubmit={submitSchedule} className="flex flex-col gap-3">
				<h4 className="font-medium text-ink-1 text-sm">
					Schedule and retention
				</h4>
				<div className="grid gap-3 sm:grid-cols-2">
					<Field>
						<FieldLabel htmlFor="ib-time">Time of day (UTC)</FieldLabel>
						<Input
							id="ib-time"
							scale="lg"
							autoComplete="off"
							inputMode="numeric"
							placeholder={
								dailyTime(data.schedule) === undefined
									? `cron ${data.schedule}`
									: "03:30"
							}
							className="font-mono"
							value={time}
							onChange={(e) => setTime(e.target.value)}
						/>
						<FieldDescription>
							{dailyTime(data.schedule) === undefined
								? `The API set a custom schedule (${data.schedule}). Enter a time to switch to a daily one.`
								: "24 hour clock, UTC. Default 03:30."}
						</FieldDescription>
					</Field>
					<Field>
						<FieldLabel htmlFor="ib-retention">Backups to keep</FieldLabel>
						<Input
							id="ib-retention"
							scale="lg"
							inputMode="numeric"
							className="font-mono"
							value={retention}
							onChange={(e) => setRetention(e.target.value.replace(/\D/g, ""))}
						/>
						<FieldDescription>
							The newest ones stay; older ones are deleted from the destination.
							Default 14.
						</FieldDescription>
					</Field>
				</div>
				{scheduleError ? <FieldError>{scheduleError}</FieldError> : null}
				<div className="flex flex-wrap items-center gap-3">
					<Button type="submit" variant="secondary" disabled={save.isPending}>
						{save.isPending ? <Spinner /> : null}
						Save schedule
					</Button>
					<Button
						type="button"
						variant="ghost"
						disabled={
							!dest.configured ||
							!data.toolsAvailable ||
							running ||
							run.isPending
						}
						onClick={() => run.mutate()}
					>
						{run.isPending || running ? <Spinner /> : <PlayIcon aria-hidden />}
						{running ? "Backing up…" : "Back up now"}
					</Button>
				</div>
			</form>

			<section className="flex flex-col gap-3" aria-labelledby="ib-runs-title">
				<h4 id="ib-runs-title" className="font-medium text-ink-1 text-sm">
					History
				</h4>
				<PreUpgradeBackupNote />
				{runs.isPending ? (
					<Skeleton className="h-24 rounded-card" />
				) : items.length === 0 ? (
					<p className="text-ink-3 text-xs">
						No backups yet. The first one runs at the next scheduled time, or
						now with Back up now.
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
								</TableRow>
							</TableHeader>
							<TableBody>
								{items.map((r) => (
									<RunRow key={r.id} run={r} />
								))}
							</TableBody>
						</Table>
					</div>
				)}
			</section>

			<section
				className="flex flex-col gap-2"
				aria-labelledby="ib-restore-title"
			>
				<h4 id="ib-restore-title" className="font-medium text-ink-1 text-sm">
					Restore
				</h4>
				<p className="max-w-[64ch] text-ink-3 text-xs leading-relaxed">
					A restore runs from the command line into an empty database, with the
					original <code className="type-mono">NEBULACTRL_SECRET_KEY</code> in
					the environment of the control plane you start afterwards. It works
					with the control plane stopped, which is when you need it.
				</p>
				{dest.kind === "builtin" ? (
					<p className="max-w-[64ch] text-ink-3 text-xs leading-relaxed">
						The built-in storage is not reachable from outside this host, so the
						command below does not apply to it. The restore guide says how to
						read it, and an external bucket keeps a copy if this host is lost.
					</p>
				) : (
					<CodeBlock
						copyValue={`nebulactrl-server restore --from ${restoreSource(data)} --at latest`}
					>
						{`nebulactrl-server restore --from ${restoreSource(data)} --at latest`}
					</CodeBlock>
				)}
				<p className="text-ink-4 text-xs">
					See docs/operations/backup-restore.md for the full steps.
				</p>
			</section>

			<InstanceDestinationDialog
				open={editing}
				onOpenChange={setEditing}
				settings={data}
			/>
		</div>
	);
}

/** The `--from` value that names the current destination. */
function restoreSource(settings: InstanceBackupView): string {
	const d = settings.destination;
	if (d.kind === "s3") {
		const prefix = d.prefix ? `/${d.prefix}` : "";
		return `s3://${d.bucket ?? "<bucket>"}${prefix}`;
	}
	// "directory /var/lib/nebulactrl/backups/nightly": the path a restore reads from.
	return d.where.replace(/^directory /, "");
}
