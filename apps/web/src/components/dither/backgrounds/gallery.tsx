import type {
	BackgroundEntry,
	KnobDef,
	KnobValues,
} from "@nebulactrl/dither/backgrounds/registry";
import { BACKGROUNDS_1 } from "@nebulactrl/dither/backgrounds/registry-1";
import { BACKGROUNDS_2 } from "@nebulactrl/dither/backgrounds/registry-2";
import { cn } from "cn";
import { useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Slider } from "#/components/ui/slider";
import { Switch } from "#/components/ui/switch";

/** Every background in the library, part 1 then part 2. */
export const BACKGROUNDS: readonly BackgroundEntry[] = [
	...BACKGROUNDS_1,
	...BACKGROUNDS_2,
];

/** Selects up to this many options render as a segmented control. */
const SEGMENTED_MAX = 4;

function defaultsOf(entry: BackgroundEntry): KnobValues {
	const values: Record<string, number | string | boolean> = {};
	for (const knob of entry.knobs) values[knob.key] = knob.value;
	return values;
}

function format(value: number, step: number): string {
	const decimals = step >= 1 ? 0 : step >= 0.1 ? 1 : 2;
	return value.toFixed(decimals);
}

function KnobControl({
	knob,
	value,
	onChange,
}: {
	knob: KnobDef;
	value: number | string | boolean | undefined;
	onChange: (next: number | string | boolean) => void;
}) {
	if (knob.type === "range") {
		const current = typeof value === "number" ? value : knob.value;
		return (
			<div className="flex items-center gap-3">
				<Slider
					aria-label={knob.label}
					min={knob.min}
					max={knob.max}
					step={knob.step}
					value={current}
					onValueChange={(next) => {
						if (typeof next === "number") onChange(next);
					}}
				/>
				<output className="w-10 shrink-0 text-right font-mono text-ink-3 text-xs tabular-nums">
					{format(current, knob.step)}
				</output>
			</div>
		);
	}
	if (knob.type === "toggle") {
		const current = typeof value === "boolean" ? value : knob.value;
		return (
			<Switch
				aria-label={knob.label}
				checked={current}
				onCheckedChange={(next) => onChange(next)}
			/>
		);
	}
	const current = typeof value === "string" ? value : knob.value;
	if (knob.options.length <= SEGMENTED_MAX) {
		return (
			<Segmented
				aria-label={knob.label}
				value={current}
				onValueChange={onChange}
				options={knob.options.map((option) => ({
					value: option,
					label: option,
				}))}
			/>
		);
	}
	return (
		<Select
			value={current}
			onValueChange={(next) => {
				if (typeof next === "string") onChange(next);
			}}
		>
			<SelectTrigger size="sm" aria-label={knob.label} className="w-full">
				<SelectValue />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				{knob.options.map((option) => (
					<SelectItem key={option} value={option}>
						{option}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

/** Every background with its knobs. Mounted by the dev gallery route. */
export function BackgroundsGallery({ className }: { className?: string }) {
	const [activeId, setActiveId] = useState(BACKGROUNDS[0]?.id ?? "");
	const [edits, setEdits] = useState<Readonly<Record<string, KnobValues>>>({});
	const entry =
		BACKGROUNDS.find((item) => item.id === activeId) ?? BACKGROUNDS[0];
	if (!entry) return null;
	const values: KnobValues = { ...defaultsOf(entry), ...edits[entry.id] };

	return (
		<div className={cn("flex flex-col gap-4", className)}>
			<div
				className="flex flex-wrap gap-1.5"
				role="tablist"
				aria-label="Backgrounds"
			>
				{BACKGROUNDS.map((item) => (
					<button
						key={item.id}
						type="button"
						role="tab"
						aria-selected={item.id === entry.id}
						onClick={() => setActiveId(item.id)}
						className={cn(
							"h-7 rounded-ctl border px-2.5 font-mono text-xs outline-none transition-[background-color,border-color,color] duration-quick ease-out focus-visible:focus-ring",
							item.id === entry.id
								? "border-accent-ink bg-white/7 text-ink-1"
								: "border-line text-ink-3 hover:border-line-strong hover:text-ink-1",
						)}
					>
						{item.name}
					</button>
				))}
			</div>
			<div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_300px]">
				<div className="relative isolate aspect-[16/10] overflow-hidden rounded-panel border border-line bg-surface-base">
					{/* Keyed so switching entries remounts and re-resolves its palette. */}
					<div key={entry.id} className="absolute inset-0 -z-10">
						{entry.render(values)}
					</div>
					<div className="flex size-full flex-col items-start justify-end gap-1.5 p-6">
						<h3 className="text-ink-1 text-xl font-medium tracking-tight">
							{entry.name}
						</h3>
						<p className="max-w-sm text-ink-3 text-sm">
							Sample text on top, to judge contrast against the dither.
						</p>
					</div>
				</div>
				<div className="flex max-h-[520px] flex-col gap-3 overflow-y-auto overscroll-contain pr-1">
					{entry.knobs.map((knob) => (
						<div key={knob.key} className="flex flex-col gap-1.5">
							<span className="type-label text-ink-3">{knob.label}</span>
							<KnobControl
								knob={knob}
								value={values[knob.key]}
								onChange={(next) =>
									setEdits((prev) => ({
										...prev,
										[entry.id]: { ...prev[entry.id], [knob.key]: next },
									}))
								}
							/>
						</div>
					))}
				</div>
			</div>
		</div>
	);
}
