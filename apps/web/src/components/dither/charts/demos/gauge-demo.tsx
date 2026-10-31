import { DitherGauge } from "@nebulactrl/dither/charts/gauge";
import type { GaugeShape } from "@nebulactrl/dither/charts/gauge-math";
import { useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";
import { Switch } from "#/components/ui/switch";

type Variant = GaugeShape;

const VARIANTS = [
	{ value: "dial", label: "Dial" },
	{ value: "half", label: "Half" },
	{ value: "ring", label: "Ring" },
] as const;

export function GaugeDemo() {
	const [variant, setVariant] = useState<Variant>("dial");
	const [value, setValue] = useState(64);
	const [thresholds, setThresholds] = useState(true);
	const [ticks, setTicks] = useState(true);
	const [range, setRange] = useState(false);
	const rangeId = useId();
	const valueLabelId = useId();
	const thresholdsId = useId();
	const ticksId = useId();

	return (
		<div className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
				<span className="type-micro">CPU load</span>
				<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
					<Segmented
						aria-label="Variant"
						value={variant}
						onValueChange={setVariant}
						options={VARIANTS}
					/>
					<div className="flex items-center gap-2 font-mono text-2xs text-ink-3">
						<span id={valueLabelId}>Value {value}</span>
						<Slider
							aria-labelledby={valueLabelId}
							className="w-28"
							min={0}
							max={100}
							step={1}
							value={value}
							onValueChange={setValue}
						/>
					</div>
					<label
						htmlFor={thresholdsId}
						className="flex items-center gap-2 font-mono text-2xs text-ink-3"
					>
						Warn 70 · bad 90
						<Switch
							id={thresholdsId}
							size="sm"
							aria-label="Thresholds"
							checked={thresholds}
							onCheckedChange={setThresholds}
						/>
					</label>
					<label
						htmlFor={ticksId}
						className="flex items-center gap-2 font-mono text-2xs text-ink-3"
					>
						Ticks
						<Switch
							id={ticksId}
							size="sm"
							aria-label="Ticks"
							checked={ticks}
							onCheckedChange={setTicks}
						/>
					</label>
					<label
						htmlFor={rangeId}
						className="flex items-center gap-2 font-mono text-2xs text-ink-3"
					>
						Min / max
						<Switch
							id={rangeId}
							size="sm"
							aria-label="Show min and max"
							checked={range}
							onCheckedChange={setRange}
						/>
					</label>
				</div>
			</div>
			<div className="flex justify-center">
				<DitherGauge
					label="CPU"
					value={value}
					unit="%"
					variant={variant}
					ticks={ticks}
					showRange={range}
					{...(thresholds ? { warnAt: 70, badAt: 90 } : {})}
				/>
			</div>
		</div>
	);
}
