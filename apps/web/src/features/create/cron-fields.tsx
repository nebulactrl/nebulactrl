import { useMemo } from "react";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { CronPreview } from "#/features/tasks/cron-preview";
import {
	browserTimezone,
	isValidTimezone,
	presetFor,
	SCHEDULE_PRESETS,
	timezoneOptions,
} from "#/features/tasks/model";

/** The reader's own zone when the runtime knows it as an IANA zone, else UTC. */
export function defaultCronTimezone(): string {
	const zone = browserTimezone();
	return isValidTimezone(zone) ? zone : "UTC";
}

/**
 * What the Cron job dialog asks beyond a service's source: when it runs (a
 * preset or any expression), in which zone, and an optional command to run in
 * place of the image's CMD. The preview is the control plane's own reading.
 */
export function CronFields({
	schedule,
	onSchedule,
	timezone,
	onTimezone,
	command,
	onCommand,
}: {
	schedule: string;
	onSchedule(value: string): void;
	timezone: string;
	onTimezone(value: string): void;
	command: string;
	onCommand(value: string): void;
}) {
	const zones = useMemo(() => {
		const all = timezoneOptions();
		return all.includes(timezone) ? all : [timezone, ...all];
	}, [timezone]);
	const presets = SCHEDULE_PRESETS.map((preset) => ({
		value: preset.id,
		label: preset.label,
	}));
	const zoneItems = zones.map((zone) => ({ value: zone, label: zone }));
	return (
		<>
			<div className="flex items-start gap-4">
				<Field className="min-w-0 flex-1">
					<FieldLabel htmlFor="create-cron-preset">Schedule</FieldLabel>
					<Select
						value={presetFor(schedule.trim())}
						items={presets}
						onValueChange={(next) => {
							const picked = SCHEDULE_PRESETS.find((p) => p.id === next);
							if (picked?.cron) onSchedule(picked.cron);
						}}
					>
						<SelectTrigger id="create-cron-preset" className="w-full">
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							{SCHEDULE_PRESETS.map((preset) => (
								<SelectItem key={preset.id} value={preset.id}>
									{preset.label}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</Field>
				<Field className="min-w-0 flex-1">
					<FieldLabel htmlFor="create-cron-schedule">
						Cron expression
					</FieldLabel>
					<Input
						id="create-cron-schedule"
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						value={schedule}
						onChange={(event) => onSchedule(event.target.value)}
					/>
				</Field>
			</div>
			<Field>
				<FieldLabel htmlFor="create-cron-timezone">Time zone</FieldLabel>
				<Select
					value={timezone}
					items={zoneItems}
					onValueChange={(next) => onTimezone(String(next ?? "UTC"))}
				>
					<SelectTrigger id="create-cron-timezone" className="w-full">
						<SelectValue />
					</SelectTrigger>
					<SelectContent alignItemWithTrigger={false}>
						{zones.map((zone) => (
							<SelectItem key={zone} value={zone}>
								{zone}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			</Field>
			<CronPreview schedule={schedule} timezone={timezone} />
			<Field>
				<FieldLabel htmlFor="create-cron-command">Command</FieldLabel>
				<Input
					id="create-cron-command"
					autoComplete="off"
					spellCheck={false}
					className="font-mono"
					placeholder="the image's own command"
					value={command}
					onChange={(event) => onCommand(event.target.value)}
				/>
				<FieldDescription>
					Run without a shell. For pipes and && use{" "}
					<code className="type-mono">sh -c "…"</code>.
				</FieldDescription>
			</Field>
		</>
	);
}
