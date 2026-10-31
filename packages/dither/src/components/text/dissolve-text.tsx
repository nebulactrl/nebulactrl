import { cn } from "cn";
import {
	type CSSProperties,
	type ElementType,
	type HTMLAttributes,
	type ReactNode,
	useEffect,
	useMemo,
	useRef,
} from "react";
import type { BayerSize } from "../../lib/bayer";
import { useReducedMotionPref } from "../../lib/prefs";
import { readCellVar } from "../images/grid";
import { useInView } from "../images/hooks";
import { cssTextColor, type TextColor } from "./colors";
import {
	arrivalRanks,
	type DissolveBy,
	type DissolveDirection,
	levelSequence,
	resolveDissolveTiming,
	splitUnits,
} from "./dissolve";
import { MASK_STEPS, maskTiling, maskUrl } from "./masks";

export type DissolveFill = "solid" | "gradient" | "dotted" | "hatched";

/** The Bayer patterns a dissolve fills in through. */
export type DissolveMatrix = "bayer2" | "bayer4" | "bayer8";

export interface DissolveTextProps
	extends Omit<HTMLAttributes<HTMLElement>, "children" | "color"> {
	children: string;
	/** Element to render. Default `span`. */
	as?: ElementType;
	/** Dissolve by `char`, `word` or `line` (lines split on `\n`; text without one is a single line). Default `char`. */
	by?: DissolveBy;
	/** Arrival order. `random` is a fixed shuffle, the same on every visit. Default `start`. */
	direction?: DissolveDirection;
	/** What the text settles into. `solid` ends as plain text. Default `solid`. */
	fill?: DissolveFill;
	/** For `gradient`: the Bayer level to stop at, 0..1. Default 0.625. */
	level?: number;
	/** Text colour: a palette name such as `blue`, a `--token` or any CSS colour. Default: the inherited text colour. */
	color?: TextColor;
	/** Threshold pattern the letters fill in through. Default `bayer4`. */
	matrix?: DissolveMatrix;
	/** Cell edge of the mask in CSS px. Default `--dither-cell`. */
	cell?: number;
	/** Time one unit takes, ms. Default 600. */
	duration?: number;
	/** Gap between the starts of consecutive units, ms. Default 28 per character, 90 per word or line. */
	stagger?: number;
	/** Wait before the first unit, ms. */
	delay?: number;
	/** Multiplies every timing: 2 plays twice as fast. Default 1. */
	speed?: number;
	/** Seed for `direction="random"`. */
	seed?: number;
	/** A colour each unit wears while it arrives, fading to the text colour over twice the duration. `none` (default) turns it off. */
	accent?: TextColor | "none";
	/** Play when scrolled into view (`in-view`, default; `view` is the same) or as soon as it mounts (`mount`). */
	trigger?: "in-view" | "view" | "mount";
	/** `true` dissolves in, `false` dissolves out. Default true. */
	show?: boolean;
	/** Same as `show`. */
	visible?: boolean;
	/** Change to play the dissolve again. */
	replayKey?: number | string;
	onDone?: (direction: "in" | "out") => void;
}

/** Share of cells inked when the dissolve ends, per fill. */
const END_TONE: Record<DissolveFill, number> = {
	solid: 1,
	gradient: 0.625,
	dotted: 0.3,
	hatched: 0.5,
};

const SHAPE: Record<DissolveFill, "square" | "dot" | "hatch"> = {
	solid: "square",
	gradient: "square",
	dotted: "dot",
	hatched: "hatch",
};

const MATRIX_SIZE: Record<DissolveMatrix, BayerSize> = {
	bayer2: 2,
	bayer4: 4,
	bayer8: 8,
};

/** Shown instead of the hidden units when scripts are off. */
const NO_SCRIPT = `[data-dissolve] [data-unit]{-webkit-mask-image:none!important;mask-image:none!important}`;

/**
 * Text that dissolves in through Bayer thresholds: every unit (character, word
 * or line) is masked by a Bayer matrix that fills level by level, so the
 * letters build up out of the dither. The real text is in the DOM the whole
 * time (screen-reader text and `aria-label` on headings) and is hidden from
 * assistive technology while dissolved out; the masked copy is decoration.
 * Reduced motion shows the final text at once.
 */
export function DissolveText({
	children,
	as: Tag = "span",
	by = "char",
	direction = "start",
	fill = "solid",
	level,
	color,
	matrix = "bayer4",
	cell,
	duration,
	stagger,
	delay,
	speed,
	seed = 1,
	accent = "none",
	trigger = "in-view",
	show,
	visible = true,
	replayKey,
	onDone,
	className,
	style,
	...rest
}: DissolveTextProps): ReactNode {
	const hostRef = useRef<HTMLElement | null>(null);
	const unitRefs = useRef<(HTMLElement | null)[]>([]);
	const playedRef = useRef<"in" | "out" | null>(null);
	const animations = useRef<Animation[]>([]);
	const reduced = useReducedMotionPref();
	const inView = useInView(hostRef, { once: true, threshold: 0.2 });
	const onDoneRef = useRef(onDone);
	onDoneRef.current = onDone;

	const units = useMemo(() => splitUnits(children, by), [children, by]);
	const animated = units.filter((u) => !u.space).length;
	const ranks = useMemo(
		() => arrivalRanks(animated, direction, seed),
		[animated, direction, seed],
	);
	const timing = resolveDissolveTiming({ by, duration, stagger, delay, speed });
	const n = MATRIX_SIZE[matrix];
	const cellPx = cell !== undefined && cell > 0 ? cell : readCellVar();
	const endTone =
		fill === "gradient" && level !== undefined
			? Math.min(1, Math.max(0, level))
			: END_TONE[fill];
	const shape = SHAPE[fill];
	const endStep = Math.round(endTone * MASK_STEPS);
	const accentCss = accent === "none" ? undefined : cssTextColor(accent);

	const wanted = show ?? visible;
	const want = wanted && (trigger === "mount" || inView);
	const isHeading = typeof Tag === "string" && /^h[1-6]$/.test(Tag);

	// biome-ignore lint/correctness/useExhaustiveDependencies: the dissolve restarts only on these inputs.
	useEffect(() => {
		for (const animation of animations.current) animation.cancel();
		animations.current = [];
		const els = unitRefs.current;

		if (reduced) {
			for (const el of els) if (el) el.style.opacity = want ? "" : "0";
			return;
		}
		// Not yet shown and not asked to be: hold every unit empty.
		if (!want && playedRef.current === null) return;

		const direction_ = want ? "in" : "out";
		const steps = levelSequence(endStep, direction_ === "out");
		const urls = steps.map((step) => maskUrl(step, cellPx, n, shape));
		let finished = 0;
		els.forEach((el, i) => {
			if (!el) return;
			const rank = ranks[i] ?? 0;
			const start = timing.delay + rank * timing.stagger;
			const keyframes = urls.map((url, k) => ({
				offset: k / Math.max(1, urls.length - 1),
				maskImage: url,
				WebkitMaskImage: url,
			}));
			const mask = el.animate(keyframes, {
				duration: timing.duration,
				delay: start,
				fill: "both",
				easing: "linear",
			});
			animations.current.push(mask);
			if (accentCss && direction_ === "in") {
				const settled = getComputedStyle(el).color;
				animations.current.push(
					el.animate([{ color: accentCss }, { color: settled }], {
						duration: timing.duration * 2,
						delay: start,
						easing: "ease-out",
					}),
				);
			}
			mask.finished
				.then(() => {
					finished += 1;
					// Settle: keep the final mask as plain style so no animation outlives the dissolve.
					el.style.maskImage =
						direction_ === "out" || fill !== "solid"
							? (urls.at(-1) ?? "")
							: "none";
					el.style.setProperty("-webkit-mask-image", el.style.maskImage);
					if (finished === els.filter(Boolean).length)
						onDoneRef.current?.(direction_);
				})
				.catch(() => {});
		});
		playedRef.current = direction_;
		return () => {
			for (const animation of animations.current) animation.cancel();
		};
	}, [
		want,
		reduced,
		by,
		direction,
		fill,
		endStep,
		cellPx,
		n,
		timing.duration,
		timing.stagger,
		timing.delay,
		seed,
		accentCss,
		replayKey,
		children,
	]);

	let slot = -1;
	const initialMask = reduced ? undefined : maskUrl(0, cellPx, n, shape);
	const unitStyle: CSSProperties | undefined = initialMask
		? {
				maskImage: initialMask,
				WebkitMaskImage: initialMask,
				...maskTiling(cellPx, n),
			}
		: undefined;

	const body: ReactNode[] = [];
	let word: ReactNode[] = [];
	const flushWord = (key: string) => {
		if (word.length === 0) return;
		body.push(
			<span key={key} className="inline-block whitespace-nowrap">
				{word}
			</span>,
		);
		word = [];
	};
	units.forEach((unit, i) => {
		if (unit.space) {
			flushWord(`w${i}`);
			// biome-ignore lint/suspicious/noArrayIndexKey: units are the positions of a fixed string and are never reordered
			body.push(unit.text === "\n" ? <br key={`b${i}`} /> : unit.text);
			return;
		}
		slot += 1;
		const index = slot;
		const node = (
			<span
				// biome-ignore lint/suspicious/noArrayIndexKey: units are the positions of a fixed string and are never reordered; the ref slots are positional too
				key={`u${i}`}
				ref={(el) => {
					unitRefs.current[index] = el;
				}}
				data-unit=""
				className={by === "line" ? "block" : "inline-block"}
				style={unitStyle}
			>
				{unit.text}
			</span>
		);
		if (by === "char") word.push(node);
		else body.push(node);
	});
	flushWord("wend");
	unitRefs.current.length = animated;

	return (
		<Tag
			{...rest}
			ref={hostRef}
			data-dissolve=""
			className={cn(className)}
			style={color ? { color: cssTextColor(color), ...style } : style}
			{...(isHeading ? { "aria-label": children } : {})}
			{...(want ? {} : { "aria-hidden": true })}
		>
			<span className="sr-only">{children}</span>
			<span aria-hidden="true">{body}</span>
			<noscript>
				<style>{NO_SCRIPT}</style>
			</noscript>
		</Tag>
	);
}
