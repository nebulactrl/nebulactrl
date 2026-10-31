import { cn } from "cn";
import {
	type ElementType,
	type HTMLAttributes,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useDitherFrame } from "../../lib/motion";
import { useReducedMotionPref } from "../../lib/prefs";
import { useInView } from "../images/hooks";
import { cssTextColor, type TextColor } from "./colors";
import { maskTiling, maskUrl } from "./masks";
import {
	blockStep,
	leadingSlot,
	planScramble,
	resolveScrambleTiming,
	type ScrambleMode,
	type ScramblePlan,
	scrambleAt,
} from "./scramble";

export { SCRAMBLE_HEX, SCRAMBLE_MONO } from "./scramble";

export type ScrambleTrigger = "mount" | "in-view" | "view" | "hover" | "change";

export interface ScrambleTextProps
	extends Omit<HTMLAttributes<HTMLElement>, "children" | "color"> {
	children: string;
	/** Element to render. Default `span`. */
	as?: ElementType;
	/**
	 * `dither` (default): a block thickens through the levels of a 4×4 Bayer
	 * matrix, a few glyphs flicker, the letter lands. `blocks`: blocks only.
	 * `glyphs`: glyphs only.
	 */
	mode?: ScrambleMode;
	/** Glyph pool for the flicker. Default: the letters of the text itself. */
	characters?: string | undefined;
	/**
	 * When to play: `mount`; `in-view` once, when scrolled in (`view` is the
	 * same); `hover` whenever the closest link or button is hovered or focused;
	 * `change` only when the text changes. Default `in-view`.
	 */
	trigger?: ScrambleTrigger;
	/** Scramble again whenever `children` changes. Only the characters that differ move. Default true. */
	replayOnChange?: boolean;
	/** ms from a character's first placeholder to its landing. Default 520. */
	duration?: number;
	/** ms between the starts of consecutive characters. Default 34. */
	stagger?: number;
	/** ms before the first character starts. */
	delay?: number;
	/** Multiplies every timing: 2 plays twice as fast. Default 1. */
	speed?: number;
	/** Glyph changes per second while a character is unsettled. Default 20. */
	rate?: number;
	/** A caret at the leading edge while the text resolves. `always` keeps it blinking after the last character. Default `none`. */
	caret?: "none" | "running" | "always";
	/** Dither cell of the blocks in CSS px, at least 1. Default: the font size / 22. */
	cell?: number;
	/** Seed of the glyph flicker: the same seed always flickers the same way. */
	seed?: number;
	/** Colour of blocks, glyphs and the caret: a palette name such as `green`, a `--token` or any CSS colour. Settled letters fade from it to the text colour. Default `ink`. */
	color?: TextColor;
	/** Called when every character has settled. */
	onComplete?: () => void;
	/** Same as `onComplete`. */
	onDone?: () => void;
}

const SPACE = /\s/;
const BLOCK_N = 4;
const FALLBACK_CELL = 3;

/** The default block cell: the font size / 22, at least 1 CSS px. */
export function blockCell(fontSize: number): number {
	return Number.isFinite(fontSize) && fontSize > 0
		? Math.max(1, fontSize / 22)
		: FALLBACK_CELL;
}

/**
 * Text that resolves character by character through dithered blocks and
 * glyphs, with no layout shift: every placeholder sits in the slot of the real
 * character, which stays underneath in the DOM. The final text is always
 * readable by assistive tech (screen-reader copy plus `aria-label` on
 * headings); the scrambling layer is `aria-hidden`. Reduced motion shows the
 * final text and never plays.
 */
export function ScrambleText({
	children,
	as: Tag = "span",
	mode = "dither",
	characters,
	trigger = "in-view",
	replayOnChange = true,
	duration,
	stagger,
	delay,
	speed,
	rate,
	caret = "none",
	cell,
	seed = 1,
	color = "ink",
	onComplete,
	onDone,
	className,
	style,
	...rest
}: ScrambleTextProps): ReactNode {
	const hostRef = useRef<HTMLElement | null>(null);
	const slotRefs = useRef<(HTMLElement | null)[]>([]);
	const realRefs = useRef<(HTMLElement | null)[]>([]);
	const holdRefs = useRef<(HTMLElement | null)[]>([]);
	const blockRefs = useRef<(HTMLElement | null)[]>([]);
	const caretRef = useRef<HTMLSpanElement | null>(null);
	const painted = useRef<string[]>([]);
	const landed = useRef<boolean[]>([]);
	const planRef = useRef<ScramblePlan | null>(null);
	const startRef = useRef<number | null>(null);
	const previousText = useRef<string | null>(null);
	const playedView = useRef(false);
	/** True while a play is under way, false once it settled. */
	const playingRef = useRef(false);
	/** True while the text waits, hidden, for its trigger. */
	const waitingRef = useRef(false);
	const cellRef = useRef(FALLBACK_CELL);
	const lastLead = useRef<number | null>(null);
	const doneRef = useRef({ onComplete, onDone });
	doneRef.current = { onComplete, onDone };
	const [running, setRunning] = useState(false);
	const reduced = useReducedMotionPref();
	const inView = useInView(hostRef, { once: true, threshold: 0.2 });

	const when = trigger === "view" ? "in-view" : trigger;
	const colorCss = cssTextColor(color);
	const chars = useMemo(() => [...children], [children]);
	const timing = resolveScrambleTiming({
		duration,
		stagger,
		delay,
		speed,
		rate,
	});
	const { duration: ms, stagger: gap, delay: wait, frameMs } = timing;
	const options = useMemo(
		() => ({
			mode,
			characters,
			seed,
			duration: ms,
			stagger: gap,
			delay: wait,
			frameMs,
		}),
		[mode, characters, seed, ms, gap, wait, frameMs],
	);

	/** Put the caret at the leading slot, or after the last character once settled. */
	const syncCaret = (lead: number, force = false) => {
		const el = caretRef.current;
		if (!el) return;
		if (!force && lastLead.current === lead) return;
		lastLead.current = lead;
		let index = lead;
		let edge = 0;
		if (lead < 0) {
			const last = slotRefs.current.findLastIndex(Boolean);
			index = caret === "always" && !waitingRef.current ? last : -1;
			edge = 1;
		} else if (!playingRef.current) index = -1;
		const slot = index >= 0 ? slotRefs.current[index] : null;
		if (!slot) {
			el.style.opacity = "0";
			return;
		}
		el.style.opacity = "1";
		el.style.height = `${slot.offsetHeight}px`;
		el.style.transform = `translate(${slot.offsetLeft + edge * slot.offsetWidth}px, ${slot.offsetTop}px)`;
	};

	/** Write one frame into the DOM, touching only the slots that changed. */
	const paint = (elapsed: number) => {
		const plan = planRef.current;
		chars.forEach((_char, i) => {
			const real = realRefs.current[i];
			const hold = holdRefs.current[i];
			const block = blockRefs.current[i];
			if (!real || !hold || !block) return;
			const cell_ = plan
				? scrambleAt(plan, i, elapsed)
				: { char: "", kind: "final" as const, tone: 0 };
			const step = cell_.kind === "block" ? blockStep(cell_.tone) : 0;
			const sig =
				cell_.kind === "block"
					? `block:${step}`
					: `${cell_.kind}:${cell_.char}`;
			if (painted.current[i] === sig) return;
			painted.current[i] = sig;
			if (cell_.kind === "final") {
				hold.textContent = "";
				block.style.opacity = "0";
				real.style.opacity = "";
				if (
					plan?.slots[i]?.changes &&
					landed.current[i] === false &&
					!reduced
				) {
					real.animate(
						[{ color: colorCss }, { color: getComputedStyle(real).color }],
						{ duration: 320, easing: "ease-out" },
					);
				}
				landed.current[i] = true;
				return;
			}
			landed.current[i] = false;
			real.style.opacity = "0";
			if (cell_.kind === "block") {
				hold.textContent = "";
				const url = maskUrl(step, cellRef.current, BLOCK_N);
				Object.assign(block.style, {
					opacity: "1",
					backgroundColor: colorCss,
					maskImage: url,
					WebkitMaskImage: url,
				});
				return;
			}
			block.style.opacity = "0";
			hold.textContent = cell_.char;
			hold.style.color = cell_.kind === "from" ? "" : colorCss;
		});
		syncCaret(plan && playingRef.current ? leadingSlot(plan, elapsed) : -1);
	};

	const start = (from: string) => {
		if (reduced) return;
		const host = hostRef.current;
		cellRef.current =
			cell !== undefined && cell > 0
				? Math.max(1, cell)
				: blockCell(
						host ? Number.parseFloat(getComputedStyle(host).fontSize) : 0,
					);
		for (const block of blockRefs.current) {
			if (block)
				Object.assign(block.style, maskTiling(cellRef.current, BLOCK_N));
		}
		planRef.current = planScramble(from, children, options);
		startRef.current = null;
		landed.current = chars.map(() => false);
		painted.current = [];
		playingRef.current = true;
		waitingRef.current = false;
		lastLead.current = null;
		paint(-1);
		setRunning(true);
	};

	// A simulated unmount (StrictMode) must look like a fresh mount on the way back.
	useLayoutEffect(
		() => () => {
			previousText.current = null;
			playedView.current = false;
		},
		[],
	);

	// First paint and text changes: set the initial layer before the browser paints.
	// biome-ignore lint/correctness/useExhaustiveDependencies: start/paint close over the same inputs listed here.
	useLayoutEffect(() => {
		const previous = previousText.current;
		previousText.current = children;
		planRef.current = null;
		painted.current = [];
		playingRef.current = false;
		waitingRef.current = false;
		lastLead.current = null;
		if (reduced) {
			paint(Number.POSITIVE_INFINITY);
			return;
		}
		if (previous === null) {
			if (when === "mount") start("");
			else if (when === "in-view") {
				// Hold the letters back until the text scrolls into view.
				waitingRef.current = true;
				planRef.current = planScramble("", children, options);
				paint(-1);
			} else paint(Number.POSITIVE_INFINITY);
			return;
		}
		if (replayOnChange && previous !== children) start(previous);
		else paint(Number.POSITIVE_INFINITY);
	}, [children, reduced, options]);

	// Play once when scrolled into view.
	// biome-ignore lint/correctness/useExhaustiveDependencies: start only needs to run when the view flag flips.
	useEffect(() => {
		if (when !== "in-view" || !inView || playedView.current || reduced) return;
		playedView.current = true;
		start("");
	}, [inView, when, reduced]);

	// Replay on hover or focus of the closest link or button.
	// biome-ignore lint/correctness/useExhaustiveDependencies: start reads current props through refs and closures.
	useEffect(() => {
		if (when !== "hover" || reduced) return;
		const host = hostRef.current;
		const target = host?.closest("a, button") ?? host;
		if (!target) return;
		const replay = () => start("");
		target.addEventListener("pointerenter", replay);
		target.addEventListener("focusin", replay);
		return () => {
			target.removeEventListener("pointerenter", replay);
			target.removeEventListener("focusin", replay);
		};
	}, [when, reduced, children, options]);

	useDitherFrame(
		(time, _delta, still) => {
			const plan = planRef.current;
			if (still === "reduced" || !plan) {
				paint(Number.POSITIVE_INFINITY);
				return;
			}
			if (still === "disabled") return;
			startRef.current ??= time;
			const elapsed = (time - startRef.current) * 1000;
			paint(elapsed);
			if (elapsed >= plan.total) {
				planRef.current = null;
				playingRef.current = false;
				setRunning(false);
				syncCaret(-1, true);
				doneRef.current.onComplete?.();
				doneRef.current.onDone?.();
			}
		},
		{ fps: 30, enabled: running && !reduced, target: hostRef },
	);

	// A caret that stays blinks once the text has settled, and follows the end of the text on resize.
	// biome-ignore lint/correctness/useExhaustiveDependencies: syncCaret reads refs; it only needs to run when these inputs change.
	useEffect(() => {
		const el = caretRef.current;
		const host = hostRef.current;
		if (!el || !host || caret !== "always" || running) return;
		syncCaret(-1, true);
		const observer = new ResizeObserver(() => syncCaret(-1, true));
		observer.observe(host);
		const blink =
			reduced || el.style.opacity === "0"
				? null
				: el.animate([{ opacity: 1 }, { opacity: 0 }], {
						duration: 1000,
						iterations: Number.POSITIVE_INFINITY,
						easing: "steps(1, jump-end)",
					});
		return () => {
			observer.disconnect();
			blink?.cancel();
		};
	}, [caret, running, reduced, children]);

	slotRefs.current.length = chars.length;
	realRefs.current.length = chars.length;
	holdRefs.current.length = chars.length;
	blockRefs.current.length = chars.length;

	// Words stay whole so lines wrap at spaces; each character is a fixed slot.
	const body: ReactNode[] = [];
	let word: ReactNode[] = [];
	const flush = (key: string) => {
		if (word.length === 0) return;
		body.push(
			<span key={key} className="inline-block whitespace-nowrap">
				{word}
			</span>,
		);
		word = [];
	};
	chars.forEach((char, i) => {
		if (SPACE.test(char)) {
			flush(`w${i}`);
			// biome-ignore lint/suspicious/noArrayIndexKey: characters are the positions of a fixed string and are never reordered
			body.push(char === "\n" ? <br key={`b${i}`} /> : char);
			return;
		}
		word.push(
			<span
				// biome-ignore lint/suspicious/noArrayIndexKey: characters are the positions of a fixed string and are never reordered; the ref slots are positional too
				key={`c${i}`}
				ref={(el) => {
					slotRefs.current[i] = el;
				}}
				className="relative inline-block"
			>
				<span
					ref={(el) => {
						realRefs.current[i] = el;
					}}
				>
					{char}
				</span>
				<span
					ref={(el) => {
						blockRefs.current[i] = el;
					}}
					aria-hidden="true"
					className="pointer-events-none absolute inset-x-[6%] inset-y-[14%] opacity-0"
				/>
				<span
					ref={(el) => {
						holdRefs.current[i] = el;
					}}
					aria-hidden="true"
					className="pointer-events-none absolute inset-0 flex items-center justify-center"
				/>
			</span>,
		);
	});
	flush("wend");

	const isHeading = typeof Tag === "string" && /^h[1-6]$/.test(Tag);
	return (
		<Tag
			{...rest}
			ref={hostRef}
			className={cn(caret !== "none" && "relative", className)}
			style={style}
			{...(isHeading ? { "aria-label": children } : {})}
		>
			<span className="sr-only">{children}</span>
			<span aria-hidden="true">{body}</span>
			{caret !== "none" ? (
				<span
					ref={caretRef}
					aria-hidden="true"
					className="pointer-events-none absolute top-0 left-0 w-[0.08em] min-w-0.5 opacity-0"
					style={{ backgroundColor: colorCss }}
				/>
			) : null}
		</Tag>
	);
}
