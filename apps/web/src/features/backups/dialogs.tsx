import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listBackupDestinationsOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	createBackup,
	createBackupSchedule,
	restoreBackup,
	updateBackupSchedule,
} from "#/api/gen/sdk.gen";
import type {
	BackupView,
	DestinationView,
	ScheduleView,
} from "#/api/gen/types.gen";
import { serversQuery } from "#/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { absoluteTime } from "#/lib/format";
import {
	describeCron,
	presetFor,
	SCHEDULE_PRESETS,
	type SchedulePreset,
} from "./model";
import { invalidateBackups } from "./queries";

export function DestinationSelect({
	id,
	value,
	onChange,
	items,
}: {
	id: string;
	value: string;
	onChange: (v: string) => void;
	items: readonly DestinationView[];
}) {
	return (
		<Select
			value={value}
			items={items.map((d) => ({ value: d.id, label: d.name }))}
			onValueChange={(v) => onChange(String(v ?? ""))}
		>
			<SelectTrigger id={id} className="w-full">
				<SelectValue placeholder="Choose a destination" />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				{items.map((d) => (
					<SelectItem key={d.id} value={d.id}>
						{d.name}
						{d.offSite ? "" : " (not off-site)"}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

/** Back up now: choose a destination and start a run. */
export function BackupNowDialog({
	open,
	onOpenChange,
	serviceId,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	serviceId: string;
}) {
	const queryClient = useQueryClient();
	const destinations = useQuery({
		...listBackupDestinationsOptions(),
		enabled: open,
	});
	const items = destinations.data?.items ?? [];
	const [chosen, setChosen] = useState("");
	const destinationId = chosen || items[0]?.id || "";

	const start = useMutation({
		mutationFn: () =>
			createBackup({ path: { serviceId }, body: { destinationId } }),
		onSuccess: async () => {
			toast.success(
				"Backup started",
				"It streams from the database straight to the destination.",
			);
			await invalidateBackups(queryClient);
			onOpenChange(false);
		},
		onError: (e) => toast.error("Could not start the backup", errorMessage(e)),
	});

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<DialogHeader>
					<DialogTitle>Back up now</DialogTitle>
					<DialogDescription>
						A logical dump, streamed to the destination without a temp file. A
						failed dump never leaves a usable-looking object.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<Field>
						<FieldLabel htmlFor="now-dest">Destination</FieldLabel>
						<DestinationSelect
							id="now-dest"
							value={destinationId}
							onChange={setChosen}
							items={items}
						/>
					</Field>
				</DialogBody>
				<DialogFooter>
					<Button variant="ghost" onClick={() => onOpenChange(false)}>
						Cancel
					</Button>
					<Button
						variant="primary"
						disabled={destinationId === "" || start.isPending}
						onClick={() => start.mutate()}
					>
						{start.isPending ? <Spinner /> : null}
						Back up now
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

/** Create or edit a schedule. */
export function ScheduleDialog({
	open,
	onOpenChange,
	serviceId,
	schedule,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	serviceId: string;
	schedule: ScheduleView | null;
}) {
	const queryClient = useQueryClient();
	const destinations = useQuery({
		...listBackupDestinationsOptions(),
		enabled: open,
	});
	const items = destinations.data?.items ?? [];
	const [destChoice, setDestChoice] = useState("");
	const [preset, setPreset] = useState<SchedulePreset["id"]>("daily");
	const [cron, setCron] = useState("0 3 * * *");
	const [timezone, setTimezone] = useState("UTC");
	const [count, setCount] = useState("7");
	const [days, setDays] = useState("0");
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (!open) return;
		setError(null);
		setDestChoice(schedule?.destinationId ?? "");
		setCron(schedule?.cron ?? "0 3 * * *");
		setPreset(schedule ? presetFor(schedule.cron) : "daily");
		setTimezone(schedule?.timezone ?? "UTC");
		setCount(String(schedule?.retentionCount ?? 7));
		setDays(String(schedule?.retentionDays ?? 0));
	}, [open, schedule]);

	const destinationId = destChoice || items[0]?.id || "";
	const retentionCount = Number.parseInt(count, 10);
	const retentionDays = Number.parseInt(days, 10);
	const validRetention =
		Number.isInteger(retentionCount) &&
		retentionCount >= 0 &&
		Number.isInteger(retentionDays) &&
		retentionDays >= 0;

	const save = useMutation({
		mutationFn: async () => {
			const body = {
				cron: cron.trim(),
				timezone: timezone.trim() || "UTC",
				retentionCount,
				retentionDays,
			};
			if (schedule) {
				await updateBackupSchedule({
					path: { scheduleId: schedule.id },
					body: { ...body, destinationId },
				});
			} else {
				await createBackupSchedule({
					path: { serviceId },
					body: { ...body, destinationId },
				});
			}
		},
		onSuccess: async () => {
			toast.success(
				schedule ? "Schedule saved" : "Schedule added",
				describeCron(cron),
			);
			await invalidateBackups(queryClient);
			onOpenChange(false);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const choosePreset = (next: SchedulePreset["id"]) => {
		setPreset(next);
		const p = SCHEDULE_PRESETS.find((x) => x.id === next);
		if (p?.cron) setCron(p.cron);
	};
	const valid = destinationId !== "" && cron.trim() !== "" && validRetention;
	const submit = (e: FormEvent) => {
		e.preventDefault();
		if (!valid || save.isPending) return;
		setError(null);
		save.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>
							{schedule ? "Edit schedule" : "Schedule backups"}
						</DialogTitle>
						<DialogDescription>
							Runs while the database is running. When the server is offline or
							the database stopped, the run is recorded as skipped with the
							reason.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="sch-dest">Destination</FieldLabel>
							<DestinationSelect
								id="sch-dest"
								value={destinationId}
								onChange={setDestChoice}
								items={items}
							/>
						</Field>
						<Field>
							<FieldLabel id="sch-when">When</FieldLabel>
							<Segmented<SchedulePreset["id"]>
								aria-label="Schedule"
								font="sans"
								density="regular"
								value={preset}
								onValueChange={choosePreset}
								options={SCHEDULE_PRESETS.map((p) => ({
									value: p.id,
									label: p.label,
								}))}
							/>
						</Field>
						<div className="grid gap-3 sm:grid-cols-[1fr_160px]">
							<Field>
								<FieldLabel htmlFor="sch-cron">Cron expression</FieldLabel>
								<Input
									id="sch-cron"
									className="font-mono"
									autoComplete="off"
									spellCheck={false}
									value={cron}
									onChange={(e) => {
										setCron(e.target.value);
										setPreset(presetFor(e.target.value.trim()));
									}}
								/>
								<FieldDescription>
									{describeCron(cron)}. Minute hour day month weekday, or
									@hourly, @daily, @weekly. At most one run every 5 minutes.
								</FieldDescription>
							</Field>
							<Field>
								<FieldLabel htmlFor="sch-tz">Timezone</FieldLabel>
								<Input
									id="sch-tz"
									autoComplete="off"
									spellCheck={false}
									placeholder="UTC"
									value={timezone}
									onChange={(e) => setTimezone(e.target.value)}
								/>
							</Field>
						</div>
						<div className="grid gap-3 sm:grid-cols-2">
							<Field>
								<FieldLabel htmlFor="sch-count">Keep the newest</FieldLabel>
								<Input
									id="sch-count"
									inputMode="numeric"
									value={count}
									onChange={(e) => setCount(e.target.value)}
								/>
								<FieldDescription>
									Backups of this schedule; 0 = no limit.
								</FieldDescription>
							</Field>
							<Field>
								<FieldLabel htmlFor="sch-days">Delete after (days)</FieldLabel>
								<Input
									id="sch-days"
									inputMode="numeric"
									value={days}
									onChange={(e) => setDays(e.target.value)}
								/>
								<FieldDescription>
									0 = never. The newest backup and locked ones are always kept.
								</FieldDescription>
							</Field>
						</div>
						{error ? <FieldError>{error}</FieldError> : null}
					</DialogBody>
					<DialogFooter>
						<Button
							type="button"
							variant="ghost"
							onClick={() => onOpenChange(false)}
						>
							Cancel
						</Button>
						<Button
							type="submit"
							variant="primary"
							disabled={!valid || save.isPending}
						>
							{save.isPending ? <Spinner /> : null}
							{schedule ? "Save" : "Add schedule"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

/** Restore a backup: into a new sibling database (default) or over the database in place. */
export function RestoreDialog({
	backup,
	serviceSlug,
	serverId,
	onClose,
}: {
	backup: BackupView | null;
	serviceSlug: string;
	serverId: string | null;
	onClose: () => void;
}) {
	const queryClient = useQueryClient();
	const servers = useQuery({ ...serversQuery(), enabled: backup !== null });
	const [mode, setMode] = useState<"new" | "in_place">("new");
	const [name, setName] = useState("");
	const [server, setServer] = useState("");
	const [confirm, setConfirm] = useState("");
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (backup) {
			setMode("new");
			setName("");
			setServer("");
			setConfirm("");
			setError(null);
		}
	}, [backup]);

	const local = backup?.destinationKind === "local";
	// A local backup lives on one server's disk, so the new database must be there.
	const targetServer = local
		? (backup?.serverId ?? serverId ?? "")
		: server || serverId || "";
	const online = (servers.data ?? []).filter(
		(s) => s.status === "online" && (!local || s.id === backup?.serverId),
	);

	const start = useMutation({
		mutationFn: () => {
			if (!backup) throw new Error("no backup");
			return restoreBackup({
				path: { backupId: backup.id },
				body:
					mode === "new"
						? {
								mode: "new",
								...(name.trim() ? { name: name.trim() } : {}),
								...(targetServer ? { serverId: targetServer } : {}),
							}
						: { mode: "in_place", confirmSlug: confirm },
			});
		},
		onSuccess: async () => {
			toast.success(
				mode === "new" ? "Restoring into a new database" : "Restore started",
				mode === "new"
					? "A sibling database appears on the canvas; the original is untouched."
					: "A safety backup is taken first.",
			);
			await invalidateBackups(queryClient);
			onClose();
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const valid = mode === "new" ? targetServer !== "" : confirm === serviceSlug;
	const submit = (e: FormEvent) => {
		e.preventDefault();
		if (!valid || start.isPending) return;
		setError(null);
		start.mutate();
	};

	return (
		<Dialog open={backup !== null} onOpenChange={(o) => (o ? null : onClose())}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Restore backup</DialogTitle>
						<DialogDescription>
							{backup
								? `Taken ${absoluteTime(backup.createdAt)} from ${backup.database || backup.engine}.`
								: null}
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel id="restore-mode">How</FieldLabel>
							<Segmented<"new" | "in_place">
								aria-label="Restore mode"
								font="sans"
								density="regular"
								value={mode}
								onValueChange={setMode}
								options={[
									{ value: "new", label: "Into a new database" },
									{ value: "in_place", label: "Over this database" },
								]}
							/>
						</Field>
						{mode === "new" ? (
							<>
								<Alert variant="info">
									<AlertTitle>Safe by default</AlertTitle>
									<AlertDescription>
										A new database of the same engine and version is created
										next to this one with fresh credentials, started empty, and
										the backup is restored into it. This database is not
										touched. Point your apps at the new one when you are happy.
									</AlertDescription>
								</Alert>
								<Field>
									<FieldLabel htmlFor="restore-name">Name</FieldLabel>
									<Input
										id="restore-name"
										autoComplete="off"
										placeholder={`${serviceSlug}-restored-…`}
										value={name}
										onChange={(e) => setName(e.target.value)}
									/>
								</Field>
								<Field>
									<FieldLabel htmlFor="restore-server">Server</FieldLabel>
									<Select
										value={targetServer}
										items={online.map((s) => ({ value: s.id, label: s.name }))}
										onValueChange={(v) => setServer(String(v ?? ""))}
									>
										<SelectTrigger id="restore-server" className="w-full">
											<SelectValue placeholder="Choose an online server" />
										</SelectTrigger>
										<SelectContent alignItemWithTrigger={false}>
											{online.map((s) => (
												<SelectItem key={s.id} value={s.id}>
													{s.name}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
									{local ? (
										<FieldDescription>
											This backup is stored on that server's disk, so it can
											only be restored there.
										</FieldDescription>
									) : null}
								</Field>
							</>
						) : (
							<>
								<Alert variant="warning">
									<AlertTitle>This overwrites the database</AlertTitle>
									<AlertDescription>
										NebulaCtrl first takes a safety backup to the same
										destination; if that fails nothing is changed. Then the data
										is replaced with this backup's. Writes your apps make during
										the restore may be lost: stop them first if that matters.
									</AlertDescription>
								</Alert>
								<Field>
									<FieldLabel htmlFor="restore-confirm">
										Type <code className="type-mono">{serviceSlug}</code> to
										confirm
									</FieldLabel>
									<Input
										id="restore-confirm"
										autoComplete="off"
										spellCheck={false}
										className="font-mono"
										value={confirm}
										onChange={(e) => setConfirm(e.target.value)}
									/>
								</Field>
							</>
						)}
						{error ? <FieldError>{error}</FieldError> : null}
					</DialogBody>
					<DialogFooter>
						<Button type="button" variant="ghost" onClick={onClose}>
							Cancel
						</Button>
						<Button
							type="submit"
							variant={mode === "in_place" ? "danger-solid" : "primary"}
							disabled={!valid || start.isPending}
						>
							{start.isPending ? <Spinner /> : null}
							{mode === "new"
								? "Restore into new database"
								: "Overwrite database"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
