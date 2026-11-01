import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { ditherMaskStyle } from "@nebulactrl/dither/lib/pattern";
import {
	type ComponentProps,
	type CSSProperties,
	useEffect,
	useRef,
} from "react";
import type { Service } from "#/api/models";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { processReplicaLabel } from "#/features/processes/model";
import {
	REPLICA_STATE_LABEL,
	type ReplicaPipModel,
	type ReplicaState,
	replicaStrip,
} from "./replica-states";

/** The static calm-tier texture of a starting replica: half the cells, 2px cells. */
const STARTING_TEXTURE = ditherMaskStyle({ tone: 0.5, cell: 2 });

/** Colours of the stacked bar; not running is the bare track, like an empty pip. */
const BAR_COLOR = {
	ready: "ok",
	starting: "var(--color-status-building)",
	failed: "bad",
} as const;

/** The key of one replica; `enter` is its stagger slot when it was added later. */
function PipShape({
	state,
	enter = null,
	swatch = false,
	...rest
}: {
	state: ReplicaState;
	enter?: number | null;
	/** The legend's smaller, round key. */
	swatch?: boolean;
} & ComponentProps<"span">) {
	return (
		<span
			{...rest}
			className="replica-pip"
			data-state={state}
			data-size={swatch ? "swatch" : undefined}
			data-enter={enter === null ? undefined : ""}
			style={
				enter === null ? undefined : ({ "--pip-i": enter } as CSSProperties)
			}
		>
			<i aria-hidden style={STARTING_TEXTURE} />
		</span>
	);
}

/**
 * Stagger slots for the pips: none on the strip's first paint, then one slot
 * (30ms apart) per pip added after it, counted from the first new one.
 */
function useEnterSlots(count: number): readonly (number | null)[] {
	const settled = useRef(false);
	const slots = useRef<(number | null)[]>([]);
	useEffect(() => {
		settled.current = true;
	}, []);
	const known = slots.current;
	if (known.length > count) known.length = count;
	const first = known.length;
	for (let slot = first; slot < count; slot += 1)
		known.push(settled.current ? slot - first : null);
	return known;
}

function pipTitle(pip: ReplicaPipModel, server: string | null): string {
	const state =
		pip.state === "stopped"
			? pip.detail
			: `${REPLICA_STATE_LABEL[pip.state]}, ${pip.detail.toLowerCase()}`;
	const who = pip.process
		? processReplicaLabel(pip.process, pip.index)
		: `Replica ${pip.index}`;
	return [who, state, server].filter(Boolean).join(" · ");
}

/**
 * Replicas at a glance: a pip per replica up to 16, one solid stacked bar
 * above that. The legend appears only when the replicas are in more than one
 * state (or as the bar's key): with one state the count above already says it.
 * With `process` only that process' replicas are drawn.
 */
export function ReplicaStrip({
	service,
	process,
}: {
	service: Service;
	process?: string | undefined;
}) {
	const strip = replicaStrip(service, process);
	const server = service.server?.name ?? null;
	const enterSlots = useEnterSlots(strip.pips.length);
	if (strip.total === 0) return null;
	return (
		<div className="flex min-w-0 flex-col gap-2 pt-0.5">
			{strip.mode === "bar" ? (
				<DitherProgressBar
					label="Replicas"
					hideLabel
					hideLegend
					max={strip.total}
					valueText={strip.summary}
					fill="solid"
					parts={strip.legend.flatMap(({ state, label, count }) =>
						state === "stopped"
							? []
							: [
									{
										label,
										value: count,
										color: BAR_COLOR[state],
										fill: "solid" as const,
									},
								],
					)}
				/>
			) : (
				<div
					role="img"
					aria-label={strip.summary}
					className="flex flex-wrap gap-1"
				>
					{strip.pips.map((pip, slot) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: a slot is a position, so a state change crossfades in place
						<Tooltip key={slot}>
							<TooltipTrigger
								render={
									<PipShape
										state={pip.state}
										enter={enterSlots[slot] ?? null}
									/>
								}
							/>
							<TooltipContent>{pipTitle(pip, server)}</TooltipContent>
						</Tooltip>
					))}
				</div>
			)}
			{strip.legend.length > 1 || strip.mode === "bar" ? (
				<ul className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
					{strip.legend.map(({ state, label, count }) => (
						<li
							key={state}
							className="flex items-center gap-1.5 font-mono text-2xs text-ink-2 leading-none"
						>
							<PipShape state={state} swatch aria-hidden />
							<span className="font-medium">{label}</span>
							<span className="text-ink-3 tabular-nums">{count}</span>
						</li>
					))}
				</ul>
			) : null}
		</div>
	);
}
