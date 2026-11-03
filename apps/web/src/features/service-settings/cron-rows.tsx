import { useMemo } from "react";
import { Switch } from "#/components/ui/switch";
import { CronPreview } from "#/features/tasks/cron-preview";
import {
	presetFor,
	SCHEDULE_PRESETS,
	timezoneOptions,
} from "#/features/tasks/model";
import { type DraftProps, SelectRow, SettingRow, TextRow } from "./fields";

const PRESET_OPTIONS = SCHEDULE_PRESETS.map((preset) => ({
	value: preset.id,
	label: preset.label,
	text: preset.label,
}));

/**
 * Run on a schedule: a one-shot service with a cron schedule starts no
 * container on deploy; its image runs its own command on every tick, one run
 * per tick, in the run history. Shown only while the service is a one-shot job.
 */
export function CronRows({ draft, errors, set }: DraftProps) {
	const zones = useMemo(() => {
		const all = timezoneOptions();
		return all.includes(draft.cronTimezone)
			? all
			: [draft.cronTimezone, ...all];
	}, [draft.cronTimezone]);
	const preset = presetFor(draft.cronSchedule.trim());
	return (
		<>
			<SettingRow
				label="Run on a schedule"
				htmlFor="set-cron"
				description="Deploys the image without starting it; the command runs on every tick."
			>
				<div className="flex items-center gap-3">
					<Switch
						id="set-cron"
						checked={draft.cronEnabled}
						onCheckedChange={(checked) => set("cronEnabled", checked)}
					/>
					<span className="text-[13px] text-ink-2">
						{draft.cronEnabled
							? "On: runs on the schedule below"
							: "Off: runs once per deploy"}
					</span>
				</div>
			</SettingRow>
			{draft.cronEnabled ? (
				<>
					<SelectRow
						id="set-cron-preset"
						label="Schedule"
						value={preset}
						options={PRESET_OPTIONS}
						onChange={(next) => {
							const picked = SCHEDULE_PRESETS.find((p) => p.id === next);
							if (picked?.cron) set("cronSchedule", picked.cron);
						}}
					/>
					<TextRow
						id="set-cron-schedule"
						label="Cron expression"
						field="config.cron.schedule"
						description="Minute hour day-of-month month weekday, or @hourly, @daily, @weekly."
						mono
						value={draft.cronSchedule}
						error={errors.cronSchedule}
						onChange={(value) => set("cronSchedule", value)}
					/>
					<SelectRow
						id="set-cron-timezone"
						label="Time zone"
						field="config.cron.timezone"
						description="The schedule is read in this IANA zone."
						value={draft.cronTimezone}
						options={zones.map((zone) => ({
							value: zone,
							label: zone,
							text: zone,
						}))}
						onChange={(value) => set("cronTimezone", value)}
					/>
					<TextRow
						id="set-cron-timeout"
						label="Timeout (s)"
						field="config.cron.timeoutSeconds"
						description="A run is stopped after this long. 1 to 86400; empty is 1800."
						type="number"
						narrow
						placeholder="1800"
						value={draft.cronTimeoutSeconds}
						error={errors.cronTimeoutSeconds}
						onChange={(value) => set("cronTimeoutSeconds", value)}
					/>
					<SettingRow
						label="Skip a run while the previous one runs"
						htmlFor="set-cron-forbid"
						field="config.cron.concurrency"
						description="Off starts a new run on every tick, even while an earlier one is still going."
					>
						<Switch
							id="set-cron-forbid"
							checked={draft.cronConcurrency === "forbid"}
							onCheckedChange={(checked) =>
								set("cronConcurrency", checked ? "forbid" : "allow")
							}
						/>
					</SettingRow>
					<CronPreview
						schedule={draft.cronSchedule}
						timezone={draft.cronTimezone}
					/>
				</>
			) : null}
		</>
	);
}
