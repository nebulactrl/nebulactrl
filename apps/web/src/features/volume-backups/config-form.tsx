import { useMutation, useQueryClient } from "@tanstack/react-query";
import { PlusIcon } from "lucide-react";
import { type FormEvent, useEffect, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	deleteVolumeBackupConfig,
	putVolumeBackupConfig,
} from "#/api/gen/sdk.gen";
import type {
	DestinationView,
	VolumeBackupConfigView,
} from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { OptionCard, OptionCardGroup } from "#/components/ui/option-card";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { DestinationSelect } from "#/features/backups/dialogs";
import { describeCron } from "#/features/backups/model";
import { SettingRow } from "#/features/service-settings/fields";
import { HookField } from "./hook-field";
import {
	buildConfigInput,
	type ConfigDraft,
	draftFromConfig,
	ENCRYPTION_NOTE,
	FREEZE_BUDGET_MAX,
	FREEZE_BUDGET_MIN,
	isDirty,
	LOCAL_DESTINATION_WARNING,
	MODE_OPTIONS,
	SCHEDULE_CHOICES,
	type ScheduleChoice,
	scheduleChoiceFor,
	type VolumeMode,
} from "./model";
import { invalidateVolumeBackups } from "./queries";
import { RecoveryKeyButton } from "./recovery-key-button";

export interface ConfigFormProps {
	serviceId: string;
	serviceSlug: string;
	config: VolumeBackupConfigView;
	destinations: readonly DestinationView[];
	onAddDestination: () => void;
}

/**
 * The settings of a service's volume backups: where they go, how the service
 * is treated while its files are copied, the commands around it, excludes,
 * retention and an optional schedule. One Save; nothing runs until a backup
 * is started.
 */
export function ConfigForm({
	serviceId,
	serviceSlug,
	config,
	destinations,
	onAddDestination,
}: ConfigFormProps) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const saved = useMemo(
		() => draftFromConfig(config, destinations[0]?.id ?? ""),
		[config, destinations],
	);
	const savedKey = JSON.stringify(saved);
	const [draft, setDraft] = useState<ConfigDraft>(saved);
	const [error, setError] = useState<string | null>(null);
	// Take what the control plane now says, unless the person is mid-edit and
	// only an unrelated field (run times, the destination list) changed.
	// biome-ignore lint/correctness/useExhaustiveDependencies: keyed by the draft's own content
	useEffect(() => setDraft(saved), [savedKey]);

	const result = useMemo(() => buildConfigInput(draft), [draft]);
	const errors = result.ok ? {} : result.errors;
	const changed = isDirty(draft, saved);
	// Defaults are worth saving as they are: the first Save creates the config.
	const dirty = changed || !config.configured;
	const set = <K extends keyof ConfigDraft>(key: K, value: ConfigDraft[K]) =>
		setDraft((prev) => ({ ...prev, [key]: value }));

	const destination = destinations.find((d) => d.id === draft.destinationId);
	const local = destination?.kind === "local";

	const save = useMutation({
		mutationFn: async () => {
			if (!result.ok) throw new Error("Fix the highlighted fields first.");
			await putVolumeBackupConfig({
				path: { serviceId },
				body: result.input,
			});
		},
		onSuccess: async () => {
			toast.success("Volume backup settings saved");
			await invalidateVolumeBackups(queryClient);
		},
		onError: (e) => setError(errorMessage(e)),
	});
	const remove = useMutation({
		mutationFn: () => deleteVolumeBackupConfig({ path: { serviceId } }),
		onSuccess: async () => {
			toast.success(
				"Volume backups turned off",
				"Backups already made are kept.",
			);
			await invalidateVolumeBackups(queryClient);
		},
		onError: (e) => toast.error("Could not turn them off", errorMessage(e)),
	});

	const submit = (e: FormEvent) => {
		e.preventDefault();
		if (!result.ok || !dirty || save.isPending) return;
		setError(null);
		save.mutate();
	};

	const scheduled = draft.schedule !== "manual";
	const chooseSchedule = (id: ScheduleChoice) => {
		const choice = SCHEDULE_CHOICES.find((c) => c.id === id);
		setDraft((prev) => ({
			...prev,
			schedule: id,
			cron: choice?.cron || (id === "custom" ? prev.cron : ""),
			enabled: id === "manual" ? prev.enabled : true,
		}));
	};

	return (
		<form onSubmit={submit} className="flex flex-col gap-1" noValidate>
			<SettingRow
				label="Destination"
				description="Where the encrypted backups are stored."
				htmlFor="vb-destination"
			>
				<div className="flex items-center gap-2">
					<div className="min-w-0 flex-1">
						<DestinationSelect
							id="vb-destination"
							value={draft.destinationId}
							onChange={(v) => set("destinationId", v)}
							items={destinations}
						/>
					</div>
					<Button
						type="button"
						variant="secondary"
						size="md"
						onClick={onAddDestination}
					>
						<PlusIcon aria-hidden />
						Add
					</Button>
				</div>
				{errors.destination ? (
					<FieldError>{errors.destination}</FieldError>
				) : null}
				{local ? (
					<Alert variant="warning">
						<AlertDescription>{LOCAL_DESTINATION_WARNING}</AlertDescription>
					</Alert>
				) : null}
			</SettingRow>

			<SettingRow
				label="While files are copied"
				description="How the service is treated for the local copy. The upload happens afterwards and never holds the service."
			>
				<OptionCardGroup<VolumeMode>
					aria-label="While files are copied"
					value={draft.mode}
					onValueChange={(v) => set("mode", v)}
					className="grid grid-cols-1 gap-2"
				>
					{MODE_OPTIONS.map((o) => (
						<OptionCard
							key={o.value}
							value={o.value}
							title={o.label}
							description={o.description}
							className="w-full"
						/>
					))}
				</OptionCardGroup>
			</SettingRow>

			{draft.mode === "freeze" ? (
				<SettingRow
					label="Longest pause"
					description="If the copy would take longer, the backup stops and the service is not left paused."
					htmlFor="vb-budget"
				>
					<div className="flex items-center gap-2">
						<Input
							id="vb-budget"
							inputMode="numeric"
							className="w-20 font-mono"
							aria-invalid={errors.freezeBudget ? true : undefined}
							value={draft.freezeBudget}
							onChange={(e) => set("freezeBudget", e.target.value)}
						/>
						<span className="text-ink-3 text-xs">
							seconds ({FREEZE_BUDGET_MIN} to {FREEZE_BUDGET_MAX})
						</span>
					</div>
					{errors.freezeBudget ? (
						<FieldError>{errors.freezeBudget}</FieldError>
					) : null}
				</SettingRow>
			) : null}

			<HookField
				label="Before backup"
				description="Runs inside the container, before the freeze. If it fails the backup fails, unless you allow it to go on."
				draft={draft.preHook}
				onChange={(h) => set("preHook", h)}
				error={errors.preHook}
				timeoutError={errors.preHookTimeout}
				templates
			/>
			<HookField
				label="After backup"
				description="Runs inside the container once the service is running again, for example to remove a file the first command made."
				draft={draft.postHook}
				onChange={(h) => set("postHook", h)}
				error={errors.postHook}
				timeoutError={errors.postHookTimeout}
			/>

			<SettingRow
				label="Excludes"
				description="One pattern per line, matched against paths like /data/cache. Empty means every file."
				htmlFor="vb-excludes"
			>
				<Textarea
					id="vb-excludes"
					className="font-mono"
					rows={2}
					spellCheck={false}
					placeholder="/data/cache&#10;*.tmp"
					value={draft.excludes}
					onChange={(e) => set("excludes", e.target.value)}
				/>
			</SettingRow>

			<SettingRow
				label="Keep"
				description="The newest backup of each day, week and month is kept; the rest are removed. Locked backups are never removed. 0 means no limit."
			>
				<div className="grid grid-cols-3 gap-2">
					{(
						[
							["keepDaily", "Daily"],
							["keepWeekly", "Weekly"],
							["keepMonthly", "Monthly"],
						] as const
					).map(([key, label]) => (
						<div key={key} className="flex flex-col gap-1">
							<label htmlFor={`vb-${key}`} className="type-label">
								{label}
							</label>
							<Input
								id={`vb-${key}`}
								inputMode="numeric"
								className="font-mono"
								aria-invalid={errors[key] ? true : undefined}
								value={draft[key]}
								onChange={(e) => set(key, e.target.value)}
							/>
						</div>
					))}
				</div>
				{errors.keepDaily || errors.keepWeekly || errors.keepMonthly ? (
					<FieldError>Enter 0 or more; 0 means no limit.</FieldError>
				) : null}
			</SettingRow>

			<SettingRow
				label="Schedule"
				description="Optional. A run is skipped, with the reason recorded, when the server is offline or a deployment is in progress."
			>
				<Segmented<ScheduleChoice>
					aria-label="Schedule"
					font="sans"
					density="regular"
					value={draft.schedule}
					onValueChange={chooseSchedule}
					options={SCHEDULE_CHOICES.map((c) => ({
						value: c.id,
						label: c.label,
					}))}
				/>
				{scheduled ? (
					<>
						<div className="grid grid-cols-1 gap-2 @md/section:grid-cols-[1fr_160px]">
							<div className="flex flex-col gap-1">
								<label htmlFor="vb-cron" className="type-label">
									Cron expression
								</label>
								<Input
									id="vb-cron"
									className="font-mono"
									autoComplete="off"
									spellCheck={false}
									aria-invalid={errors.cron ? true : undefined}
									value={draft.cron}
									onChange={(e) => {
										setDraft((prev) => ({
											...prev,
											cron: e.target.value,
											schedule:
												e.target.value.trim() === ""
													? "custom"
													: scheduleChoiceFor(e.target.value),
										}));
									}}
								/>
							</div>
							<div className="flex flex-col gap-1">
								<label htmlFor="vb-tz" className="type-label">
									Timezone
								</label>
								<Input
									id="vb-tz"
									autoComplete="off"
									spellCheck={false}
									placeholder="UTC"
									value={draft.timezone}
									onChange={(e) => set("timezone", e.target.value)}
								/>
							</div>
						</div>
						{errors.cron ? (
							<FieldError>{errors.cron}</FieldError>
						) : (
							<span className="text-ink-3 text-xs">
								{describeCron(draft.cron)}. Minute hour day month weekday, or
								@hourly, @daily, @weekly. At most one run every 5 minutes.
							</span>
						)}
						<div className="flex items-center gap-2 text-ink-2 text-sm">
							<Switch
								id="vb-enabled"
								size="sm"
								checked={draft.enabled}
								onCheckedChange={(v) => set("enabled", v)}
							/>
							<label htmlFor="vb-enabled">
								{draft.enabled ? "Runs on this schedule" : "Paused"}
							</label>
						</div>
					</>
				) : null}
			</SettingRow>

			<SettingRow label="Encryption" description={ENCRYPTION_NOTE}>
				{config.repoReady ? (
					<div>
						<RecoveryKeyButton
							serviceId={serviceId}
							serviceSlug={serviceSlug}
						/>
					</div>
				) : (
					<span className="text-ink-3 text-xs">
						The key is created with the first backup. Download it then.
					</span>
				)}
			</SettingRow>

			{error ? <FieldError>{error}</FieldError> : null}
			<div className="flex flex-wrap items-center justify-end gap-2 pt-2">
				{config.configured ? (
					<Button
						type="button"
						variant="ghost"
						size="sm"
						className="mr-auto text-destructive-text"
						disabled={remove.isPending}
						onClick={() =>
							void confirm({
								title: "Turn off volume backups?",
								body: "The settings and the schedule are removed. Backups already made are kept and can still be restored.",
								cta: "Turn off",
								danger: true,
								hold: false,
								onConfirm: async () => {
									await remove.mutateAsync();
								},
							})
						}
					>
						Turn off
					</Button>
				) : null}
				{changed ? (
					<Button
						type="button"
						variant="ghost"
						size="sm"
						onClick={() => {
							setError(null);
							setDraft(saved);
						}}
					>
						Discard
					</Button>
				) : null}
				<Button
					type="submit"
					variant="primary"
					size="sm"
					disabled={!result.ok || !dirty || save.isPending}
				>
					{save.isPending ? <Spinner /> : null}
					{config.configured ? "Save" : "Turn on volume backups"}
				</Button>
			</div>
		</form>
	);
}
