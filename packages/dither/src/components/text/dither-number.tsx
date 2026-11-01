import { cn } from "cn";
import {
	type HTMLAttributes,
	memo,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import type { BayerSize } from "../../lib/bayer";
import { useReducedMotionPref } from "../../lib/prefs";
import { useTick } from "../../lib/tick";
import { useInView } from "../images/hooks";
import { cssTextColor, type TextColor } from "./colors";
import {
	complementMaskUrl,
	edgeFadeMask,
	MASK_STEPS,
	maskTiling,
	maskUrl,
} from "./masks";
import {
	changedRanks,
	formatNumber,
	type NumberFormatInput,
	numberParts,
	rollSteps,
} from "./number-parts";

export type DitherNumberEffect = "resolve" | "roll" | "tick";
export type DitherNumberSize = "sm" | "md" | "lg" | "xl" | "display";
export type DitherNumberMatrix = "bayer2" | "bayer4" | "bayer8";

export interface DitherNumberProps
	extends Omit<HTMLAttributes<HTMLElement>, "children" | "color"> {
	value: number;
	/**
	 * Where the first run starts: the digits wait here and move to `value` when
	 * the trigger fires. Default 0. Pass `value` to show it at once and animate
	 * only later changes.
	 */
	from?: number;
	/** `Intl.NumberFormat` options: currency, percent, compact, decimals. */
	format?: Intl.NumberFormatOptions;
	/** Passed to `Intl.NumberFormat`. Set it when server and client must format the same way. */
	locales?: Intl.LocalesArgument;
	/** Same as `locales`. */
	locale?: string | readonly string[];
	/** Text before the number, such as `+`. */
	prefix?: string | undefined;
	/** Text after the number, such as ` ms`. */
	suffix?: string | undefined;
	/**
	 * `roll` spins each changed digit on a strip, up when the value grows and
	 * down when it shrinks; `resolve` swaps it cell by cell through
	 * complementary Bayer masks; `tick` pops the whole value in (our shared
	 * number pop-in). Default `roll`.
	 */
	effect?: DitherNumberEffect;
	/** Start the first run on `mount` or when scrolled `in-view`. Default `in-view`. */
	trigger?: "mount" | "in-view";
	/**
	 * A type preset: `sm` 2xl, `md` 4xl, `lg` 6xl, `xl` 8xl, `display` a fluid
	 * size. Leave it unset to size the number with your own classes.
	 */
	size?: DitherNumberSize;
	/** Colour of the number: a palette name such as `green`, a `--token` or any CSS colour. Default: the inherited text colour. */
	color?: TextColor | undefined;
	/** Colour a digit takes while it moves, fading back as it lands. `none` turns it off. Default `ink`. */
	accent?: TextColor | "none";
	/** Time one digit takes, ms. Default 900 for `roll`, 560 for `resolve`. */
	duration?: number;
	/** Gap between consecutive changed digits, left to right, ms. Default 40. */
	stagger?: number;
	/** Threshold pattern of the dithered edges and the resolve. Default `bayer4`. */
	matrix?: DitherNumberMatrix;
	/** Dither cell in CSS px, at least 1. Default: the font size / 30. */
	cell?: number;
	/** Class for prefix, suffix and separators, e.g. `text-ink-3`. */
	affixClassName?: string;
}

const SIZE_CLASS: Record<DitherNumberSize, string> = {
	sm: "text-2xl",
	md: "text-4xl",
	lg: "text-6xl",
	xl: "text-8xl",
	display: "text-[clamp(3.5rem,12vw,9rem)]",
};

const MATRIX_SIZE: Record<DitherNumberMatrix, BayerSize> = {
	bayer2: 2,
	bayer4: 4,
	bayer8: 8,
};

/** Share of the font size one dither cell takes by default. */
const CELL_DIVISOR = 30;
/** Cell rows of the roll's faded edge, from the font size. */
const FADE_EM = 0.14;

/** The default dither cell for a font size: the size / 30, at least 1 CSS px. */
export function numberCell(fontSize: number): number {
	return Number.isFinite(fontSize) && fontSize > 0
		? Math.max(1, fontSize / CELL_DIVISOR)
		: 2;
}

interface DigitProps {
	char: string;
	previous: string | undefined;
	/** Rank among the digits that changed this time, or -1. */
	rank: number;
	/** Signed strip travel for `roll`: positive rolls up. */
	steps: number;
	effect: Exclude<DitherNumberEffect, "tick">;
	duration: number;
	stagger: number;
	n: BayerSize;
	cell: number | undefined;
	/** CSS colour a moving digit wears, or undefined for none. */
	accent: string | undefined;
	animate: boolean;
}

/** Lines of a roll strip, in the order they sit from top to bottom. */
function stripDigits(from: number, steps: number): string[] {
	const count = Math.abs(steps);
	const sign = steps > 0 ? 1 : -1;
	const seq = Array.from(
		{ length: count + 1 },
		(_, i) => (((from + sign * i) % 10) + 10) % 10,
	).map(String);
	// Up: the old digit is on top and the strip moves up. Down: the new one is on top.
	return sign > 0 ? seq : seq.reverse();
}

/**
 * One tabular digit. The outgoing digit (or the roll strip) lives in a second
 * layer over the same grid cell for the length of the animation, so the slot
 * never changes size.
 */
const Digit = memo(function Digit(props: DigitProps) {
	const { char } = props;
	const windowRef = useRef<HTMLSpanElement | null>(null);
	const nextRef = useRef<HTMLSpanElement | null>(null);
	const prevRef = useRef<HTMLSpanElement | null>(null);
	const stripRef = useRef<HTMLSpanElement | null>(null);
	const lastChar = useRef<string | undefined>(undefined);
	// The effect below re-runs only when the digit changes; it reads everything else from here.
	const latest = useRef(props);
	latest.current = props;

	useLayoutEffect(() => {
		const win = windowRef.current;
		const next = nextRef.current;
		const old = prevRef.current;
		const strip = stripRef.current;
		if (!win || !next || !old || !strip) return;
		const {
			previous,
			rank,
			steps,
			effect,
			duration,
			stagger,
			n,
			accent,
			animate,
		} = latest.current;
		const changed = lastChar.current !== char;
		lastChar.current = char;
		if (!changed || !animate || rank < 0) return;
		const delay = rank * stagger;
		const fontSize = Number.parseFloat(getComputedStyle(win).fontSize);
		const cell = latest.current.cell ?? numberCell(fontSize);
		const timing: KeyframeAnimationOptions = {
			duration,
			delay,
			fill: "both",
			easing: "linear",
		};
		const running: Animation[] = [];
		const finish = () => {
			for (const animation of running) animation.cancel();
			old.textContent = "";
			old.style.visibility = "hidden";
			strip.textContent = "";
			strip.style.visibility = "hidden";
			next.style.visibility = "";
			win.removeAttribute("style");
		};
		const settled = getComputedStyle(next).color;
		let tinted: HTMLElement = next;

		if (effect === "resolve") {
			old.textContent = previous ?? "";
			old.style.visibility = previous === undefined ? "hidden" : "visible";
			Object.assign(next.style, maskTiling(cell, n));
			Object.assign(old.style, maskTiling(cell, n));
			const levels = Array.from({ length: MASK_STEPS + 1 }, (_, k) => k);
			running.push(
				next.animate(
					levels.map((k) => {
						const url = maskUrl(k, cell, n);
						return {
							offset: k / MASK_STEPS,
							maskImage: url,
							WebkitMaskImage: url,
						};
					}),
					timing,
				),
				old.animate(
					levels.map((k) => {
						const url = complementMaskUrl(k, cell, n);
						return {
							offset: k / MASK_STEPS,
							maskImage: url,
							WebkitMaskImage: url,
						};
					}),
					timing,
				),
			);
		} else {
			const travel = steps === 0 ? 1 : steps;
			const from = previous === undefined ? 0 : Number(previous);
			strip.textContent = stripDigits(from, travel).join("\n");
			strip.style.visibility = "visible";
			next.style.visibility = "hidden";
			const rows = Math.max(
				2,
				Math.min(6, Math.round((fontSize * FADE_EM) / cell)),
			);
			Object.assign(win.style, edgeFadeMask(cell, rows, n));
			const count = Math.abs(travel);
			const distance = `${(count / (count + 1)) * 100}%`;
			const frames =
				travel > 0
					? [
							{ transform: "translateY(0)" },
							{ transform: `translateY(-${distance})` },
						]
					: [
							{ transform: `translateY(-${distance})` },
							{ transform: "translateY(0)" },
						];
			running.push(
				strip.animate(frames, {
					...timing,
					easing: "cubic-bezier(0.23, 1, 0.32, 1)",
				}),
			);
			tinted = strip;
		}
		// Wearing the accent while moving, settling to the text colour.
		if (accent) {
			running.push(
				tinted.animate([{ color: accent }, { color: settled }], {
					duration: duration * 2,
					delay,
					easing: "ease-out",
				}),
			);
		}
		running[0]?.finished.then(finish).catch(() => {});
		return finish;
	}, [char]);

	return (
		<span
			ref={windowRef}
			data-digit=""
			className="relative inline-grid [clip-path:inset(0)]"
		>
			<span ref={nextRef} className="col-start-1 row-start-1 text-center">
				{char}
			</span>
			<span
				ref={prevRef}
				aria-hidden="true"
				className="col-start-1 row-start-1 text-center"
				style={{ visibility: "hidden" }}
			/>
			<span
				ref={stripRef}
				aria-hidden="true"
				className="absolute inset-x-0 top-0 whitespace-pre text-center"
				style={{ visibility: "hidden" }}
			/>
		</span>
	);
});

/**
 * An animated number for KPI tiles and landing stats. Digits are tabular, so
 * the width changes only when the digit count does. When the value changes,
 * only the digits that differ move, left to right: `roll` spins them on a
 * strip (up when the value grows, down when it shrinks, an extra turn for
 * places that moved by ten or more) with dithered top and bottom edges,
 * `resolve` swaps them cell by cell through complementary Bayer masks, `tick`
 * pops the whole value in (our shared number pop-in). The formatted value is
 * always in the DOM as screen-reader text; the moving digits are decoration.
 * Reduced motion shows each value at once.
 */
export function DitherNumber({
	value,
	from = 0,
	format,
	locales,
	locale,
	prefix,
	suffix,
	effect = "roll",
	trigger = "in-view",
	size,
	color,
	accent = "ink",
	duration,
	stagger = 40,
	matrix = "bayer4",
	cell,
	className,
	affixClassName,
	style,
	...rest
}: DitherNumberProps): ReactNode {
	const hostRef = useRef<HTMLSpanElement | null>(null);
	const reduced = useReducedMotionPref();
	const inView = useInView(hostRef, { once: true, threshold: 0.3 });
	const tickRef = useTick();
	/** Digits animate only once the first run has been armed. */
	const armed = useRef(false);
	const [shown, setShown] = useState(value);
	const [primed, setPrimed] = useState(false);
	const started = trigger === "mount" || inView;

	// The server and the first client render show the final value; before the
	// browser paints, the digits drop back to `from` and wait for the trigger.
	// biome-ignore lint/correctness/useExhaustiveDependencies: runs once on mount.
	useLayoutEffect(() => {
		if (!reduced && from !== value) setShown(from);
		setPrimed(true);
	}, []);

	useEffect(() => {
		if (reduced) {
			armed.current = false;
			setShown(value);
			return;
		}
		if (!primed || !started) return;
		armed.current = true;
		setShown(value);
	}, [value, reduced, primed, started]);

	const input: NumberFormatInput = useMemo(
		() => ({ locale: locales ?? locale, format, prefix, suffix }),
		[locales, locale, format, prefix, suffix],
	);
	const parts = useMemo(() => numberParts(shown, input), [shown, input]);
	const label = useMemo(() => formatNumber(value, input), [value, input]);

	// Digits are compared with the previous render, aligned from the right edge.
	const digits = parts.filter((p) => p.type === "digit");
	const nextValues = digits.map((d) => d.value);
	const previousDigits = useRef<{ key: string; value: string }[]>([]);
	const previousByKey = new Map(
		previousDigits.current.map((d) => [d.key, d.value]),
	);
	const previousValues = previousDigits.current.map((d) => d.value);
	const ranks = changedRanks(previousValues, nextValues);
	useEffect(() => {
		previousDigits.current = digits.map((d) => ({
			key: d.key,
			value: d.value,
		}));
	});

	const n = MATRIX_SIZE[matrix];
	const ms = duration ?? (effect === "resolve" ? 560 : 900);
	const accentCss = accent === "none" ? undefined : cssTextColor(accent);

	let digitIndex = -1;
	const visual = parts.map((part) => {
		if (part.type === "literal") {
			return (
				<span key={part.key} className={cn("whitespace-pre", affixClassName)}>
					{part.value}
				</span>
			);
		}
		digitIndex += 1;
		if (effect === "tick") return <span key={part.key}>{part.value}</span>;
		return (
			<Digit
				key={part.key}
				char={part.value}
				previous={previousByKey.get(part.key)}
				rank={ranks[digitIndex] ?? -1}
				steps={
					(ranks[digitIndex] ?? -1) < 0
						? 0
						: rollSteps(previousValues, nextValues, digitIndex)
				}
				effect={effect}
				duration={ms}
				stagger={stagger}
				n={n}
				cell={cell}
				accent={accentCss}
				animate={!reduced && armed.current}
			/>
		);
	});

	return (
		<span
			{...rest}
			ref={hostRef}
			className={cn(
				"inline-flex items-baseline font-display tabular-nums leading-[1.1]",
				size && SIZE_CLASS[size],
				className,
			)}
			style={color ? { color: cssTextColor(color), ...style } : style}
		>
			<span className="sr-only">{label}</span>
			<span
				ref={effect === "tick" ? tickRef : undefined}
				aria-hidden="true"
				className={cn(
					"inline-flex items-baseline",
					effect === "tick" && "inline-block",
				)}
			>
				{visual}
			</span>
		</span>
	);
}
