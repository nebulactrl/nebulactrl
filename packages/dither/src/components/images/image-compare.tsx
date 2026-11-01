import { cn } from "cn";
import {
	type CSSProperties,
	type KeyboardEvent,
	memo,
	type PointerEvent,
	type ReactNode,
	type RefObject,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useReducedMotionPref } from "../../lib/prefs";
import {
	clampPercent as clamp,
	keyTarget,
	labelHidden,
	pointerPosition,
	roundPercent,
} from "./compare-math";
import { DitherLayer } from "./dither-layer";
import { boxAspect } from "./grid";
import { useInView } from "./hooks";
import type { DitherLook } from "./look";
import { type CrossOrigin, useNaturalSize } from "./use-dithered";

/** One side of the comparison: the untouched photo, or a dither with its own look. */
export type CompareSide =
	| ({ kind: "dither" } & Omit<DitherLook, "fit">)
	| { kind: "original" };

export interface ImageCompareProps {
	src: string;
	/** Describes the image and the comparison. Used as the group's label. */
	alt: string;
	/** Left side. Default: the `duotone` dither. */
	before?: CompareSide;
	/** Right side. Default: the original photo. */
	after?: CompareSide;
	/** Label of the left side, also in the slider's name. Default "Dithered" or "Original". */
	beforeLabel?: string;
	/** Label of the right side. Default "Dithered" or "Original". */
	afterLabel?: string;
	/** Show the corner labels; they fade out when the divider covers them. Default true. */
	labels?: boolean;
	/** Start position, 0..100. Default 50. */
	defaultValue?: number;
	/** Controlled position, 0..100. */
	value?: number;
	onValueChange?: (value: number) => void;
	/** Nudge the divider once when the image first comes into view. Default true. */
	hint?: boolean;
	/** Same as `hint`. Wins over it when both are set. */
	intro?: boolean;
	/** Width ÷ height, reserved before the image loads. Default: the image's own ratio. */
	ratio?: number;
	/** How the image fills its box on both sides. Default `cover`. */
	fit?: "cover" | "contain";
	crossOrigin?: CrossOrigin;
	className?: string;
	style?: CSSProperties;
}

const DEFAULT_BEFORE: CompareSide = { kind: "dither" };
const DEFAULT_AFTER: CompareSide = { kind: "original" };

const labelOf = (side: CompareSide) =>
	side.kind === "original" ? "Original" : "Dithered";

interface SideProps {
	side: CompareSide;
	src: string;
	fit: "cover" | "contain";
	enabled: boolean;
	boxRef: RefObject<Element | null>;
	crossOrigin: CrossOrigin | undefined;
}

const Side = memo(function Side({
	side,
	src,
	fit,
	enabled,
	boxRef,
	crossOrigin,
}: SideProps) {
	const look = useMemo(() => {
		if (side.kind === "original") return null;
		const { kind: _kind, ...rest } = side;
		return { ...rest, fit };
	}, [side, fit]);
	if (!look) {
		return (
			<img
				src={src}
				alt=""
				aria-hidden="true"
				draggable={false}
				decoding="async"
				crossOrigin={crossOrigin}
				className={cn(
					"absolute inset-0 size-full select-none",
					fit === "contain" ? "object-contain" : "object-cover",
				)}
			/>
		);
	}
	return (
		<DitherLayer
			src={src}
			look={look}
			boxRef={boxRef}
			crossOrigin={crossOrigin}
			enabled={enabled}
		/>
	);
});

const CornerLabel = ({
	side,
	hidden,
	children,
}: {
	side: "left" | "right";
	hidden: boolean;
	children: string;
}) => (
	<span
		aria-hidden="true"
		className={cn(
			"pointer-events-none absolute top-3 rounded-sm bg-surface-float/80 px-1.5 py-0.5 font-mono text-[11px] text-ink-2 backdrop-blur-sm transition-opacity duration-200 ease-out motion-reduce:transition-none",
			side === "left" ? "left-3" : "right-3",
			hidden ? "opacity-0" : "opacity-100",
		)}
	>
		{children}
	</span>
);

/**
 * A draggable before/after slider. The left side is clipped with `clip-path`
 * so both layers stay put while the divider moves. Drag, click anywhere (the
 * divider glides there in 200ms), or focus the handle and use the arrow keys
 * (1% each, Shift or Page Up/Down 10%, Home and End for the ends). The handle
 * is a `role="slider"`. On touch a sideways drag moves the divider and a
 * vertical swipe still scrolls. Either side is the original or any dither
 * look. The divider nudges once when first seen; with reduced motion there is
 * no hint and no glide.
 */
export function ImageCompare({
	src,
	alt,
	before = DEFAULT_BEFORE,
	after = DEFAULT_AFTER,
	beforeLabel,
	afterLabel,
	labels = true,
	defaultValue = 50,
	value,
	onValueChange,
	hint,
	intro = true,
	ratio,
	fit = "cover",
	crossOrigin,
	className,
	style,
}: ImageCompareProps): ReactNode {
	const boxRef = useRef<HTMLFieldSetElement | null>(null);
	const topRef = useRef<HTMLDivElement | null>(null);
	const handleRef = useRef<HTMLDivElement | null>(null);
	const dragging = useRef(false);
	const nudge = useRef<Animation[]>([]);
	const reduced = useReducedMotionPref();
	const inView = useInView(boxRef, { once: true, threshold: 0.4 });
	const near = useInView(boxRef, { rootMargin: "320px 0px", once: true });
	const natural = useNaturalSize(src, near, crossOrigin);
	const [inner, setInner] = useState(clamp(defaultValue));
	const [glide, setGlide] = useState(true);
	const position = clamp(value ?? inner);
	const hinted = hint ?? intro;
	const leftLabel = beforeLabel ?? labelOf(before);
	const rightLabel = afterLabel ?? labelOf(after);

	const set = useCallback(
		(next: number) => {
			const v = roundPercent(next);
			if (value === undefined) setInner(v);
			onValueChange?.(v);
		},
		[value, onValueChange],
	);

	const stopNudge = () => {
		for (const animation of nudge.current) animation.cancel();
		nudge.current = [];
	};

	const fromPointer = (event: PointerEvent) => {
		const rect = boxRef.current?.getBoundingClientRect();
		if (!rect) return;
		const next = pointerPosition(event.clientX, rect.left, rect.width);
		if (next !== null) set(next);
	};

	const onKeyDown = (event: KeyboardEvent) => {
		const next = keyTarget(event.key, event.shiftKey, position);
		if (next === null) return;
		event.preventDefault();
		event.stopPropagation();
		stopNudge();
		setGlide(false);
		set(next);
	};

	// One nudge, left then right then home, the first time it is seen.
	// biome-ignore lint/correctness/useExhaustiveDependencies: runs once when first in view.
	useEffect(() => {
		const top = topRef.current;
		const handle = handleRef.current;
		if (!hinted || reduced || !inView || !top || !handle) return;
		const track = [
			position,
			clamp(position - 9),
			clamp(position + 9),
			position,
		];
		const timing: KeyframeAnimationOptions = {
			duration: 1300,
			easing: "ease-in-out",
		};
		nudge.current = [
			top.animate(
				track.map((v) => ({ clipPath: `inset(0 ${100 - v}% 0 0)` })),
				timing,
			),
			handle.animate(
				track.map((v) => ({ left: `${v}%` })),
				timing,
			),
		];
		return stopNudge;
	}, [inView, hinted, reduced]);

	// Clicks glide; drags and key presses follow exactly.
	const glideStyle: CSSProperties | undefined =
		glide && !reduced
			? { transition: "clip-path 200ms ease-out, left 200ms ease-out" }
			: undefined;

	const aspect = boxAspect(ratio, natural, className);

	return (
		<fieldset
			ref={boxRef}
			aria-label={alt}
			aria-roledescription="image comparison"
			className={cn(
				"relative isolate m-0 aspect-[4/3] min-w-0 cursor-ew-resize touch-pan-y select-none overflow-hidden border-0 bg-surface-well p-0",
				className,
			)}
			style={{
				...(aspect ? { aspectRatio: String(aspect) } : {}),
				...style,
			}}
			onPointerDown={(event) => {
				if (event.button !== 0) return;
				stopNudge();
				dragging.current = true;
				setGlide(true);
				event.currentTarget.setPointerCapture(event.pointerId);
				fromPointer(event);
			}}
			onPointerMove={(event) => {
				if (!dragging.current) return;
				setGlide(false);
				fromPointer(event);
			}}
			onPointerUp={(event) => {
				dragging.current = false;
				setGlide(true);
				event.currentTarget.releasePointerCapture(event.pointerId);
			}}
			onPointerCancel={() => {
				dragging.current = false;
				setGlide(true);
			}}
		>
			<Side
				side={after}
				src={src}
				fit={fit}
				enabled={near}
				boxRef={boxRef}
				crossOrigin={crossOrigin}
			/>
			<div
				ref={topRef}
				className="absolute inset-0"
				style={{ clipPath: `inset(0 ${100 - position}% 0 0)`, ...glideStyle }}
			>
				<Side
					side={before}
					src={src}
					fit={fit}
					enabled={near}
					boxRef={boxRef}
					crossOrigin={crossOrigin}
				/>
			</div>

			{labels ? (
				<>
					<CornerLabel side="left" hidden={labelHidden("left", position)}>
						{leftLabel}
					</CornerLabel>
					<CornerLabel side="right" hidden={labelHidden("right", position)}>
						{rightLabel}
					</CornerLabel>
				</>
			) : null}

			<div
				ref={handleRef}
				className="absolute inset-y-0 w-0"
				style={{ left: `${position}%`, ...glideStyle }}
			>
				<div
					aria-hidden="true"
					className="absolute inset-y-0 -left-px w-0.5 bg-ink-1/80"
				/>
				<div
					role="slider"
					tabIndex={0}
					aria-label={`${leftLabel} and ${rightLabel} divider`}
					aria-orientation="horizontal"
					aria-valuemin={0}
					aria-valuemax={100}
					aria-valuenow={Math.round(position)}
					aria-valuetext={`${Math.round(position)}% ${leftLabel.toLowerCase()}`}
					onKeyDown={onKeyDown}
					className="-translate-x-1/2 -translate-y-1/2 absolute top-1/2 grid size-9 place-items-center rounded-full border border-line-strong bg-surface-float text-ink-1 shadow-md outline-none focus-visible:focus-ring"
				>
					<svg
						viewBox="0 0 24 24"
						width="18"
						height="18"
						aria-hidden="true"
						fill="none"
						stroke="currentColor"
						strokeWidth="2"
						strokeLinecap="round"
						strokeLinejoin="round"
					>
						<path d="m9 7-5 5 5 5" />
						<path d="m15 7 5 5-5 5" />
					</svg>
				</div>
			</div>
		</fieldset>
	);
}
