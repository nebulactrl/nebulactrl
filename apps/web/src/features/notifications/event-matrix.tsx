import type { ChannelEventType } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Segmented } from "#/components/ui/segmented";
import { Toggle } from "#/components/ui/toggle";
import {
	activePreset,
	groupEventTypes,
	type MatrixPreset,
	presetTypes,
	SEVERITIES,
	type Severity,
} from "./model";

const PRESETS: readonly { id: MatrixPreset; label: string }[] = [
	{ id: "alerts", label: "Failures and alerts" },
	{ id: "all", label: "Everything" },
	{ id: "none", label: "Nothing" },
];

const SEVERITY_TONE = { info: "info", warning: "warn", error: "bad" } as const;

/**
 * Which events a channel receives: one checkbox per event type, grouped, with
 * presets, plus the minimum severity. A type's severity badge says how loud it
 * is by default; the minimum filters on it.
 */
export function EventMatrix({
	types,
	selected,
	onSelectedChange,
	minSeverity,
	onMinSeverityChange,
}: {
	types: readonly ChannelEventType[];
	selected: readonly string[];
	onSelectedChange: (next: string[]) => void;
	minSeverity: Severity;
	onMinSeverityChange: (next: Severity) => void;
}) {
	const groups = groupEventTypes(types);
	const preset = activePreset(selected, types);
	const toggle = (type: string, on: boolean) => {
		const next = new Set(selected);
		if (on) next.add(type);
		else next.delete(type);
		// Keep catalogue order so the stored rule reads the same everywhere.
		onSelectedChange(types.map((t) => t.type).filter((t) => next.has(t)));
	};
	const setGroup = (group: readonly ChannelEventType[], on: boolean) => {
		const next = new Set(selected);
		for (const t of group) {
			if (on) next.add(t.type);
			else next.delete(t.type);
		}
		onSelectedChange(types.map((t) => t.type).filter((t) => next.has(t)));
	};

	return (
		<div className="flex flex-col gap-4">
			<Field>
				<FieldLabel id="matrix-presets">Events</FieldLabel>
				<fieldset
					aria-labelledby="matrix-presets"
					className="m-0 flex min-w-0 flex-wrap items-center gap-1.5 border-0 p-0"
				>
					{PRESETS.map((p) => (
						<Toggle
							key={p.id}
							variant="outline"
							size="sm"
							className="rounded-full"
							pressed={preset === p.id}
							onPressedChange={() => onSelectedChange(presetTypes(p.id, types))}
						>
							{p.label}
						</Toggle>
					))}
				</fieldset>
				<FieldDescription>
					New channels start with failures and alerts on and successes off.
				</FieldDescription>
			</Field>
			<div className="flex flex-col gap-2.5">
				{groups.map((g) => {
					const on = g.types.filter((t) => selected.includes(t.type)).length;
					return (
						<fieldset
							key={g.group}
							className="surface-well m-0 flex min-w-0 flex-col gap-2.5 rounded-card p-3"
						>
							<legend className="sr-only">{g.group}</legend>
							<div className="flex items-center justify-between gap-2">
								<span className="type-label">{g.group}</span>
								<span className="flex items-center gap-2">
									<span className="font-mono text-2xs text-ink-4">
										{on}/{g.types.length}
									</span>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										className="h-6 px-1.5 text-xs"
										onClick={() => setGroup(g.types, on < g.types.length)}
									>
										{on < g.types.length ? "Select all" : "Clear"}
									</Button>
								</span>
							</div>
							{g.types.map((t) => (
								<CheckLabel
									key={t.type}
									className="items-start gap-2.5 text-sm"
									checkboxClassName="mt-0.5"
									checked={selected.includes(t.type)}
									onCheckedChange={(next) => toggle(t.type, next)}
								>
									<span className="flex flex-col gap-0.5">
										<span className="flex flex-wrap items-center gap-2">
											<span className="text-ink-1">{t.label}</span>
											<Badge
												tone={SEVERITY_TONE[t.severity]}
												shape="capsule"
												title={`Default severity: ${t.severity}`}
											>
												{t.severity}
											</Badge>
										</span>
										<span className="text-ink-3 text-xs leading-relaxed">
											{t.description}
										</span>
									</span>
								</CheckLabel>
							))}
						</fieldset>
					);
				})}
			</div>
			<Field>
				<FieldLabel id="matrix-severity">Send</FieldLabel>
				<div>
					<Segmented<Severity>
						aria-label="Minimum severity"
						font="sans"
						density="regular"
						value={minSeverity}
						onValueChange={onMinSeverityChange}
						options={SEVERITIES.map((s) => ({
							value: s.value,
							label: s.label,
						}))}
					/>
				</div>
				<FieldDescription>
					Events below this severity are skipped, even when their type is
					checked.
				</FieldDescription>
			</Field>
		</div>
	);
}
