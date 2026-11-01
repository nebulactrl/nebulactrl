import { Trash2Icon } from "lucide-react";
import type { Service } from "#/api/models";
import { MoreOptions } from "#/components/settings/more-options";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Switch } from "#/components/ui/switch";
import { SettingRow, TextRow } from "#/features/service-settings/fields";
import {
	HealthFields,
	type HealthValue,
} from "#/features/service-settings/health-deploy-sections";
import type { ProcessDraft, ProcessErrors } from "./draft";
import { ScaleLine } from "./scale-rows";

/** What a blank limit inherits: the service's own values, shown as placeholders. */
export interface InheritedLimits {
	stopGraceSeconds: string;
	memoryMb: string;
	cpus: string;
}

/**
 * One additional process as a card: its name, command, port, shutdown time,
 * limits and health check. Everything but the scale is part of the settings
 * draft and waits for the save bar; the scale applies at once.
 */
export function ProcessEditor({
	service,
	process,
	errors,
	inherited,
	pending,
	onChange,
	onRemove,
}: {
	service: Service;
	process: ProcessDraft;
	errors: ProcessErrors;
	inherited: InheritedLimits;
	/** The process is in the staged config but not in the running release. */
	pending: boolean;
	onChange(patch: Partial<ProcessDraft>): void;
	onRemove(): void;
}) {
	const base = `process-${process.key}`;
	const renamed =
		process.originalName !== null && process.originalName !== process.name;
	const label = process.name === "" ? "this process" : process.name;
	const limitsInvalid = Boolean(
		errors.stopGraceSeconds || errors.memoryMb || errors.cpus,
	);
	const setHealth = <K extends keyof HealthValue>(
		key: K,
		next: HealthValue[K],
	) => onChange({ health: { ...process.health, [key]: next } });
	return (
		<li
			aria-label={`Process ${label}`}
			className="surface-raised flex flex-col rounded-card"
		>
			<div className="flex flex-wrap items-center gap-2 px-3 py-2.5">
				<span className="min-w-0 truncate font-medium font-mono text-[13px] text-ink-1">
					{process.name === "" ? "New process" : process.name}
				</span>
				{process.originalName === null ? (
					<Badge tone="staged">New</Badge>
				) : pending ? (
					<Badge tone="staged">Staged</Badge>
				) : null}
				<Button
					variant="ghost"
					size="icon-sm"
					className="ml-auto"
					aria-label={`Remove ${label}`}
					onClick={onRemove}
				>
					<Trash2Icon aria-hidden />
				</Button>
			</div>
			<div className="rule-rows border-line border-t px-3 py-1.5">
				<TextRow
					id={`${base}-name`}
					label="Name"
					description={
						renamed
							? `Renamed from ${process.originalName}. Its scale and domains move with it.`
							: "Names the containers, logs and metrics of this process."
					}
					mono
					value={process.name}
					error={errors.name}
					placeholder="worker"
					onChange={(name) => onChange({ name })}
				/>
				<TextRow
					id={`${base}-command`}
					label="Start command"
					description="Runs without a shell. Quote arguments with spaces."
					mono
					value={process.command}
					error={errors.command}
					placeholder="bin/jobs --queue default"
					onChange={(command) => onChange({ command })}
				/>
				<TextRow
					id={`${base}-port`}
					label="Port"
					description="Empty means no HTTP. Each process needs its own port."
					type="number"
					narrow
					value={process.port}
					error={errors.port}
					placeholder="none"
					onChange={(port) => onChange({ port })}
				/>
				<SettingRow
					label="Never run two instances at once"
					htmlFor={`${base}-singleton`}
					description="Stops the old container before starting the new one. Use it for schedulers."
				>
					<Switch
						id={`${base}-singleton`}
						checked={process.singleton}
						onCheckedChange={(singleton) => onChange({ singleton })}
					/>
				</SettingRow>
				<MoreOptions
					label="Shutdown and limits"
					count={3}
					invalid={limitsInvalid}
				>
					<TextRow
						id={`${base}-grace`}
						label="Stop grace period (s)"
						description="Time the old container gets to finish before it is killed"
						type="number"
						narrow
						value={process.stopGraceSeconds}
						error={errors.stopGraceSeconds}
						placeholder={inherited.stopGraceSeconds || "service setting"}
						onChange={(stopGraceSeconds) => onChange({ stopGraceSeconds })}
					/>
					<TextRow
						id={`${base}-memory`}
						label="Memory (MB)"
						description="Empty uses the service's limit"
						type="number"
						narrow
						value={process.memoryMb}
						error={errors.memoryMb}
						placeholder={inherited.memoryMb || "no limit"}
						onChange={(memoryMb) => onChange({ memoryMb })}
					/>
					<TextRow
						id={`${base}-cpus`}
						label="CPUs"
						description="Empty uses the service's limit"
						type="number"
						narrow
						value={process.cpus}
						error={errors.cpus}
						placeholder={inherited.cpus || "no limit"}
						onChange={(cpus) => onChange({ cpus })}
					/>
				</MoreOptions>
				<MoreOptions label="Health check" invalid={Boolean(errors.health)}>
					<HealthFields
						value={process.health}
						errors={{}}
						error={errors.health}
						idPrefix={`${base}-health`}
						portHint="process port"
						portPlaceholder={process.port || "process port"}
						subject={` of ${label}`}
						inlineTimings
						onChange={setHealth}
					/>
				</MoreOptions>
				<SettingRow
					label="Scale"
					description="Applies now, outside the save bar."
				>
					<ScaleLine
						service={service}
						process={process.originalName ?? process.name}
					/>
				</SettingRow>
			</div>
		</li>
	);
}
