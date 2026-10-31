import { cn } from "cn";
import type { ReactNode } from "react";
import { useTick } from "../../lib/tick";
import type {
	ChartBloom,
	ChartColor,
	DitherMatrix,
	FillVariant,
} from "./shared";
import { DitherSparkline } from "./sparkline";
import { splitDeltaGlyph, statDelta, statValueText } from "./stat-math";

export type StatDeltaTone = "good" | "bad" | "neutral";

/** Tone per delta tone: improvement green, decline red, flat plain. No lamp, no dot. */
const DELTA_TONE = { good: "ok", bad: "bad", neutral: "off" } as const;

/** The delta chip's classes: the host's `tone-wash` reads `data-tone` for the label colour and the wash behind it. */
const DELTA_CHIP =
	"tone-wash inline-flex w-fit shrink-0 items-center justify-center gap-1.5 overflow-hidden whitespace-nowrap rounded-md border border-[color-mix(in_oklab,var(--tone)_30%,transparent)] px-1.5 py-[3px] font-medium font-mono text-2xs leading-none [&>svg]:pointer-events-none [&>svg]:size-3";

export interface DitherStatCardProps {
	/** The mono eyebrow: "Requests". */
	label: string;
	/** The headline number. Numbers get thousands separators; strings show as given. It rolls in when the text changes. */
	value: number | string;
	/** Your own headline in place of the plain number, for example a `DitherNumber`. It must carry the same text for screen readers as `value`. */
	valueNode?: ReactNode;
	/**
	 * Change against the previous period. A number is a percent (`8.1` renders `▲ 8.1%`, coloured by `good`);
	 * an object gives the badge text and tone yourself. The text carries the sign and the arrow, so colour is never the only cue.
	 */
	delta?: number | { text: string; tone: StatDeltaTone; title?: string };
	/** What a numeric `delta` compares with: "vs last month". Spoken with the change. */
	deltaLabel?: string;
	/** Which direction of a numeric `delta` counts as good (`down` for latency, error rates). Default `up`. */
	good?: "up" | "down";
	/** Small line next to the delta: "per UTC day". */
	hint?: string;
	/** Trend values, oldest first; drawn as a dithered sparkline along the bottom. Needs at least two. */
	trend?: readonly number[] | null;
	/** Accessible name of the trend. Default "<label> trend". */
	trendLabel?: string;
	/** Colour of the trend: a name (`"green"`), a CSS colour or a `--token`. */
	color?: ChartColor;
	/** Texture under the trend line. Default `gradient`. */
	fill?: FillVariant;
	/** Soft glow in the colour behind the trend. Default `off`. */
	bloom?: ChartBloom;
	/** Threshold matrix of the trend's fill. Default `bayer4`. */
	matrix?: DitherMatrix;
	/** Dither cell of the trend in px. Default 2. */
	cell?: number;
	className?: string;
	children?: ReactNode;
}

/**
 * A KPI card: eyebrow, a big tabular number, a coloured delta and a dithered
 * trend that runs to the card's bottom edge. The number and delta are text;
 * only the trend is texture.
 */
export function DitherStatCard({
	label,
	value,
	valueNode,
	delta,
	deltaLabel,
	good = "up",
	hint,
	trend,
	trendLabel,
	color = "ink",
	fill,
	bloom,
	matrix,
	cell,
	className,
	children,
}: DitherStatCardProps) {
	const tick = useTick();
	const badge =
		typeof delta === "number"
			? statDelta(delta, good, deltaLabel)
			: delta
				? { ...delta, spoken: undefined }
				: null;
	return (
		<div
			className={cn(
				"surface-raised flex min-w-0 flex-col gap-2.5 overflow-hidden rounded-card px-3.5 pt-3 pb-0",
				className,
			)}
		>
			<span className="type-micro truncate">{label}</span>
			<span className="type-metric truncate text-ink-1">
				{valueNode ?? (
					<span ref={tick} className="inline-block tabular-nums">
						{statValueText(value)}
					</span>
				)}
			</span>
			<div className="flex min-h-5 items-center gap-2 text-xs leading-none">
				{badge ? (
					<span
						data-slot="badge"
						data-tone={DELTA_TONE[badge.tone]}
						title={
							"title" in badge && badge.title !== undefined
								? badge.title
								: deltaLabel
						}
						className={DELTA_CHIP}
					>
						<DeltaText
							text={badge.text}
							tone={badge.tone}
							spoken={badge.spoken}
						/>
					</span>
				) : null}
				{deltaLabel && badge ? (
					<span className="truncate text-ink-3">{deltaLabel}</span>
				) : null}
				{hint ? <span className="truncate text-ink-3">{hint}</span> : null}
			</div>
			{children}
			{trend && trend.length > 1 ? (
				<DitherSparkline
					data={trend}
					label={trendLabel ?? `${label} trend`}
					color={color}
					height={44}
					className="-mx-3.5 mt-auto"
					dot={false}
					{...(fill ? { fill } : {})}
					{...(bloom ? { bloom } : {})}
					{...(matrix ? { matrix } : {})}
					{...(cell !== undefined ? { cell } : {})}
				/>
			) : (
				<span aria-hidden className="block h-3" />
			)}
		</div>
	);
}

/** The change as an arrow icon and words; the words carry the sign too. */
function DeltaText({
	text,
	tone,
	spoken,
}: {
	text: string;
	tone: StatDeltaTone;
	spoken: string | undefined;
}) {
	const { direction, text: words } = splitDeltaGlyph(text);
	const dir =
		direction ?? (tone === "neutral" && words !== "–" ? "flat" : null);
	const Arrow =
		dir === "up"
			? ArrowUp
			: dir === "down"
				? ArrowDown
				: dir === "flat"
					? Minus
					: null;
	return (
		<>
			{Arrow ? <Arrow /> : null}
			{spoken ? (
				<>
					<span aria-hidden>{words}</span>
					<span className="sr-only">{spoken}</span>
				</>
			) : (
				words
			)}
		</>
	);
}

function Glyph({ d }: { d: string }) {
	return (
		<svg
			aria-hidden
			viewBox="0 0 24 24"
			fill="none"
			stroke="currentColor"
			strokeWidth={2}
			strokeLinecap="round"
			strokeLinejoin="round"
		>
			<path d={d} />
		</svg>
	);
}

const ArrowUp = () => <Glyph d="m5 12 7-7 7 7M12 19V5" />;
const ArrowDown = () => <Glyph d="M12 5v14m7-7-7 7-7-7" />;
const Minus = () => <Glyph d="M5 12h14" />;
