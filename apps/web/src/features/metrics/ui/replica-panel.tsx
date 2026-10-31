import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { cn } from "cn";
import { Toggle } from "#/components/ui/toggle";
import { formatMegabytes } from "#/lib/format";
import { type ReplicaBar, replicaColor } from "../model";

/** The colour swatch of a replica; "replica N" always sits beside it. */
function Swatch({ index, className }: { index: number; className?: string }) {
	return (
		<span
			aria-hidden
			className={cn("size-2.5 shrink-0 rounded-[3px]", className)}
			style={{
				backgroundColor: replicaColor(index),
				boxShadow: "inset 0 1px 0 rgb(255 255 255 / 0.35)",
			}}
		/>
	);
}

export interface ReplicaFilterProps {
	replicas: readonly { index: number }[];
	/** Picked replica indexes; empty means all. */
	picked: ReadonlySet<number>;
	onPickedChange: (next: ReadonlySet<number>) => void;
	/** Names a replica by its index (a stack's components); the default is "replica N". */
	labelFor?: ((index: number) => string) | undefined;
}

/**
 * Toggle chips for the replicas listed under the chart: "All" plus one chip
 * per replica, each with its colour swatch and its "replica N" label.
 */
export function ReplicaFilter({
	replicas,
	picked,
	onPickedChange,
	labelFor,
}: ReplicaFilterProps) {
	const toggle = (index: number) => {
		const next = new Set(picked);
		if (next.has(index)) next.delete(index);
		else next.add(index);
		// Picking every replica is the same as picking none: "All".
		onPickedChange(next.size === replicas.length ? new Set() : next);
	};
	return (
		<ul className="flex flex-wrap items-center gap-1.5" aria-label="Replicas">
			<li>
				<Toggle
					variant="outline"
					size="sm"
					pressed={picked.size === 0}
					onPressedChange={() => onPickedChange(new Set())}
					className="font-mono text-2xs"
				>
					All
				</Toggle>
			</li>
			{replicas.map((r) => (
				<li key={r.index}>
					<Toggle
						variant="outline"
						size="sm"
						pressed={picked.has(r.index)}
						onPressedChange={() => toggle(r.index)}
						className="gap-1.5 font-mono text-2xs"
					>
						<Swatch index={r.index} />
						{labelFor ? labelFor(r.index) : `replica ${r.index}`}
					</Toggle>
				</li>
			))}
		</ul>
	);
}

function Meter({
	label,
	frac,
	text,
	hint,
}: {
	label: string;
	/** 0–1, or null when there is nothing to measure against. */
	frac: number | null;
	text: string;
	hint?: string | undefined;
}) {
	return (
		<div className="flex min-w-0 items-center gap-2.5">
			<span className="w-8 shrink-0 font-mono text-3xs text-ink-4 uppercase tracking-[0.08em]">
				{label}
			</span>
			{frac === null ? (
				<span className="h-2 min-w-0 flex-1" aria-hidden />
			) : (
				<DitherProgressBar
					hideLabel
					size="sm"
					label={`${label} ${text}`}
					value={Math.round(frac * 100)}
					warnAt={90}
					className="min-w-0 flex-1"
				/>
			)}
			<span
				title={hint}
				className="w-24 shrink-0 text-right font-medium font-mono text-ink-1 text-xs tabular-nums"
			>
				{text}
			</span>
		</div>
	);
}

export interface ReplicaListProps {
	bars: readonly ReplicaBar[];
	/** What a full CPU bar means, for the hint: "of 200% quota". */
	cpuHint: string;
	/** Per-replica memory limit in MiB, or null when unlimited. */
	memLimitMb: number | null;
	/** Names a replica by its index (a stack's components); the default is "replica N". */
	labelFor?: ((index: number) => string) | undefined;
}

/**
 * Current CPU and memory of each replica, from the newest sample: a bar and
 * the exact figure in text. The API returns no per-replica history, so this
 * is a "now" list, not a chart.
 */
export function ReplicaList({
	bars,
	cpuHint,
	memLimitMb,
	labelFor,
}: ReplicaListProps) {
	return (
		<ul className="flex flex-col gap-2" aria-label="Per-replica usage now">
			{bars.map((bar) => (
				<li
					key={bar.index}
					className="surface-well grid items-center gap-x-4 gap-y-1.5 rounded-ctl px-3 py-2 @xl:grid-cols-[6.5rem_1fr_1fr]"
				>
					<span className="flex items-center gap-2 font-mono text-ink-2 text-xs">
						<Swatch index={bar.index} />
						{labelFor ? labelFor(bar.index) : `replica ${bar.index}`}
					</span>
					<Meter
						label="CPU"
						frac={bar.cpuFrac}
						text={`${bar.cpuPercent.toFixed(1)}%`}
						hint={cpuHint}
					/>
					<Meter
						label="Mem"
						frac={bar.memFrac}
						text={formatMegabytes(bar.memMb)}
						hint={
							memLimitMb
								? `of ${formatMegabytes(memLimitMb)} limit`
								: "no limit"
						}
					/>
				</li>
			))}
		</ul>
	);
}
