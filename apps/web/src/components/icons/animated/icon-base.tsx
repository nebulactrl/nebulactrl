import { cn } from "cn";
import {
	type MotionStyle,
	motion,
	type Transition,
	useReducedMotion,
	type Variants,
} from "motion/react";
import {
	type ComponentProps,
	type ReactNode,
	type Ref,
	useEffect,
	useImperativeHandle,
	useState,
} from "react";
import { iconVariant, shouldPlay } from "./playback";

// The animated icons are vendored from lucide-animated (MIT, see ./LICENSE):
// https://github.com/pqoqubbw/icons. Upstream ships each icon as a
// self-contained client component with its own mouse handlers; here the
// shared parts live in this file so the icons are only their artwork and
// their variants, and the app decides *when* they move (see ./playback).
//
// Conventions every icon follows:
// - The root is an `<svg>` (not a wrapper element), so a button's
//   `[&_svg:not([class*='size-'])]:size-4` and `size-*` classes size it like
//   any Lucide icon.
// - Stroke is `currentColor`, width 2 (Lucide's), round caps and joins.
// - `animate` plays the icon while true; the ref handle's
//   `startAnimation()` / `stopAnimation()` do the same imperatively.
// - Under `prefers-reduced-motion` the icon renders at rest, whatever it is
//   told, and nothing runs.
// - Variants animate transform and opacity. The one exception is the
//   check and circle-check "draw" (`pathLength`, a stroke-dash change),
//   which is the point of those two icons and runs on a 24px glyph.

export interface AnimatedIconHandle {
	startAnimation(): void;
	stopAnimation(): void;
}

type SvgProps = Omit<
	ComponentProps<"svg">,
	| "ref"
	| "children"
	| "onAnimationStart"
	| "onAnimationEnd"
	| "onDrag"
	| "onDragStart"
	| "onDragEnd"
	| "values"
	| "style"
>;

export interface AnimatedIconProps extends SvgProps {
	/** Pixel size of the (square) glyph; a `size-*` class overrides it. */
	size?: number | undefined;
	/** Plays the animation while true. */
	animate?: boolean | undefined;
	/** Imperative `startAnimation()` / `stopAnimation()`, as upstream. */
	ref?: Ref<AnimatedIconHandle> | undefined;
}

/**
 * Whether the icon is playing: told to by `animate` or the ref handle, and
 * not suppressed by reduced motion. An icon that mounts already playing
 * (the success toast's check) starts a frame later, as a change from rest:
 * motion does not reliably run a variant change that is present on mount.
 */
export function usePlaying(
	animate: boolean | undefined,
	ref: Ref<AnimatedIconHandle> | undefined,
): boolean {
	const reducedMotion = useReducedMotion() ?? false;
	const [started, setStarted] = useState(false);
	const [mounted, setMounted] = useState(false);
	useEffect(() => setMounted(true), []);
	useImperativeHandle(
		ref,
		() => ({
			startAnimation: () => setStarted(true),
			stopAnimation: () => setStarted(false),
		}),
		[],
	);
	return (
		mounted &&
		(animate === true || started) &&
		shouldPlay("state", { reducedMotion, keyboard: false, hoverCapable: true })
	);
}

export interface IconSvgProps extends SvgProps {
	playing: boolean;
	size?: number | undefined;
	/** Variants for the `<svg>` itself (whole-glyph motion). */
	variants?: Variants | undefined;
	transition?: Transition | undefined;
	style?: MotionStyle | undefined;
	children: ReactNode;
}

/**
 * The shared `<svg>` shell. Children that declare `variants` with `normal`
 * and `animate` labels follow the root's state.
 */
export function IconSvg({
	playing,
	size = 24,
	className,
	variants,
	transition,
	style,
	children,
	strokeWidth = 2,
	...props
}: IconSvgProps) {
	// `exactOptionalPropertyTypes`: motion's props reject an explicit undefined.
	const motionProps = {
		...(variants === undefined ? {} : { variants }),
		...(transition === undefined ? {} : { transition }),
		...(style === undefined ? {} : { style }),
	};
	return (
		<motion.svg
			xmlns="http://www.w3.org/2000/svg"
			width={size}
			height={size}
			viewBox="0 0 24 24"
			fill="none"
			stroke="currentColor"
			strokeWidth={strokeWidth}
			strokeLinecap="round"
			strokeLinejoin="round"
			aria-hidden={props["aria-label"] ? undefined : true}
			className={cn(className)}
			initial="normal"
			animate={iconVariant(playing)}
			{...motionProps}
			{...props}
		>
			{children}
		</motion.svg>
	);
}
