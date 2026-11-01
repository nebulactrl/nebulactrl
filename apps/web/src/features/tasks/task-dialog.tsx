import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createServiceTask, updateServiceTask } from "#/api/gen/sdk.gen";
import type { TaskView } from "#/api/gen/types.gen";
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
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { CronPreview } from "./cron-preview";
import {
	browserTimezone,
	commandText,
	DEFAULT_TIMEOUT_SECONDS,
	isValidTimezone,
	parseTimeoutSeconds,
	presetFor,
	SCHEDULE_PRESETS,
	type SchedulePreset,
	splitCommand,
	timezoneOptions,
} from "./model";
import { invalidateTasks } from "./queries";

type Concurrency = TaskView["concurrency"];

const CONCURRENCY_HELP: Record<Concurrency, string> = {
	forbid:
		"Forbid skips a tick while the previous run is still active; the skip is recorded in the run history.",
	allow:
		"Allow starts a new run on every tick, even while an earlier one is still running.",
};

const DEFAULT_SCHEDULE = "0 3 * * *";

/** Create or edit a scheduled task of a service. */
export function TaskDialog({
	open,
	onOpenChange,
	serviceId,
	task,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	serviceId: string;
	/** The task being edited; null creates one. */
	task: TaskView | null;
}) {
	const queryClient = useQueryClient();
	const [name, setName] = useState("");
	const [preset, setPreset] = useState<SchedulePreset["id"]>("daily");
	const [schedule, setSchedule] = useState(DEFAULT_SCHEDULE);
	const [command, setCommand] = useState("");
	const [timeout, setTimeoutText] = useState(String(DEFAULT_TIMEOUT_SECONDS));
	const [concurrency, setConcurrency] = useState<Concurrency>("forbid");
	const [timezone, setTimezone] = useState("UTC");
	const zones = useMemo(() => timezoneOptions(), []);
	const [enabled, setEnabled] = useState(true);
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (!open) return;
		setError(null);
		setName(task?.name ?? "");
		setSchedule(task?.schedule ?? DEFAULT_SCHEDULE);
		setPreset(task ? presetFor(task.schedule) : "daily");
		setCommand(commandText(task?.command));
		setTimeoutText(String(task?.timeoutSeconds ?? DEFAULT_TIMEOUT_SECONDS));
		setConcurrency(task?.concurrency ?? "forbid");
		setEnabled(task?.enabled ?? true);
		setTimezone(task?.timezone ?? "UTC");
	}, [open, task]);

	const argv = splitCommand(command);
	const timeoutSeconds = parseTimeoutSeconds(timeout);
	const timezoneOk = isValidTimezone(timezone);
	const valid =
		name.trim() !== "" &&
		schedule.trim() !== "" &&
		argv.length > 0 &&
		timeoutSeconds !== null &&
		timezoneOk;

	const save = useMutation({
		mutationFn: async () => {
			const body = {
				name: name.trim(),
				schedule: schedule.trim(),
				command: argv,
				enabled,
				timeoutSeconds: timeoutSeconds ?? DEFAULT_TIMEOUT_SECONDS,
				concurrency,
				timezone: timezone.trim() || "UTC",
			};
			if (task) {
				await updateServiceTask({ path: { taskId: task.id }, body });
			} else {
				await createServiceTask({ path: { serviceId }, body });
			}
		},
		onSuccess: async () => {
			toast.success(
				task ? "Task saved" : "Task added",
				enabled ? undefined : "It is paused until you turn it on.",
			);
			await invalidateTasks(queryClient);
			onOpenChange(false);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const choosePreset = (next: SchedulePreset["id"]) => {
		setPreset(next);
		const picked = SCHEDULE_PRESETS.find((p) => p.id === next);
		if (picked?.cron) setSchedule(picked.cron);
	};

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!valid || save.isPending) return;
		setError(null);
		save.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>{task ? "Edit task" : "Add a task"}</DialogTitle>
						<DialogDescription>
							Runs a command on a schedule, in a fresh container of the
							service's current image, with its variables and volumes. Nothing
							is restarted.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="task-name">Name</FieldLabel>
							<Input
								id="task-name"
								autoComplete="off"
								placeholder="Nightly cleanup"
								value={name}
								onChange={(e) => setName(e.target.value)}
							/>
						</Field>

						<Field>
							<FieldLabel id="task-when">Schedule</FieldLabel>
							<Segmented<SchedulePreset["id"]>
								aria-label="Schedule preset"
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
						<Field>
							<FieldLabel htmlFor="task-cron">Cron expression</FieldLabel>
							<Input
								id="task-cron"
								className="font-mono"
								autoComplete="off"
								spellCheck={false}
								value={schedule}
								onChange={(e) => {
									setSchedule(e.target.value);
									setPreset(presetFor(e.target.value.trim()));
								}}
							/>
							<FieldDescription>
								Minute hour day-of-month month weekday, or @hourly, @daily,
								@weekly.
							</FieldDescription>
						</Field>
						<Field>
							<FieldLabel htmlFor="task-timezone">Time zone</FieldLabel>
							<div className="flex gap-2">
								<Input
									id="task-timezone"
									className="flex-1"
									list="task-timezones"
									autoComplete="off"
									spellCheck={false}
									placeholder="UTC"
									aria-invalid={!timezoneOk}
									value={timezone}
									onChange={(e) => setTimezone(e.target.value)}
								/>
								<datalist id="task-timezones">
									{zones.map((zone) => (
										<option key={zone} value={zone} />
									))}
								</datalist>
								<Button
									type="button"
									variant="ghost"
									onClick={() => setTimezone(browserTimezone())}
								>
									Use mine ({browserTimezone()})
								</Button>
							</div>
							{timezoneOk ? (
								<FieldDescription>
									The schedule is read in this IANA zone (UTC when empty). In a
									zone with daylight saving, a run in the skipped hour is
									skipped for that day and a repeated hour runs once.
								</FieldDescription>
							) : (
								<FieldError>
									"{timezone.trim()}" is not an IANA time zone, e.g.
									Europe/Berlin.
								</FieldError>
							)}
						</Field>
						<CronPreview schedule={schedule} timezone={timezone} />

						<Field>
							<FieldLabel htmlFor="task-command">Command</FieldLabel>
							<Input
								id="task-command"
								className="font-mono"
								autoComplete="off"
								spellCheck={false}
								placeholder='sh -c "rake cleanup && rake report"'
								value={command}
								onChange={(e) => setCommand(e.target.value)}
							/>
							<FieldDescription>
								Run without a shell, so pipes and && do nothing on their own.
								For shell syntax use{" "}
								<code className="type-mono">sh -c "…"</code>.
							</FieldDescription>
						</Field>

						<div className="grid gap-3 sm:grid-cols-[160px_1fr]">
							<Field>
								<FieldLabel htmlFor="task-timeout">
									Timeout (seconds)
								</FieldLabel>
								<Input
									id="task-timeout"
									inputMode="numeric"
									value={timeout}
									onChange={(e) => setTimeoutText(e.target.value)}
								/>
								<FieldDescription>
									The run is stopped after this long.
								</FieldDescription>
							</Field>
							<Field>
								<FieldLabel id="task-concurrency">If still running</FieldLabel>
								<Segmented<Concurrency>
									aria-label="Concurrency"
									font="sans"
									density="regular"
									value={concurrency}
									onValueChange={setConcurrency}
									options={[
										{ value: "forbid", label: "Skip the tick" },
										{ value: "allow", label: "Run anyway" },
									]}
								/>
								<FieldDescription>
									{CONCURRENCY_HELP[concurrency]}
								</FieldDescription>
							</Field>
						</div>

						<div className="flex items-center gap-2 text-ink-2 text-sm">
							<Switch
								id="task-enabled"
								size="sm"
								checked={enabled}
								onCheckedChange={setEnabled}
							/>
							<label htmlFor="task-enabled" className="cursor-pointer">
								{enabled ? "Enabled" : "Paused"}
							</label>
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
							{task ? "Save" : "Add task"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
