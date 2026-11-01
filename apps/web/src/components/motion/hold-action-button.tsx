"use client";

// beui.dev/components/motion/expanding-arrow-button, re-skinned as the danger key.

import { exact } from "@nebulactrl/tokens";
import { cn } from "cn";
import { type HTMLMotionProps, motion, useReducedMotion } from "motion/react";
import {
	forwardRef,
	type KeyboardEvent,
	type PointerEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { EASE_OUT, SPRING_PRESS } from "#/lib/ease";
import {
	capturePointer,
	releasePointer,
	TOUCH_GESTURE_CLASS,
} from "#/lib/touch";

// The fill grows linearly over the hold, so how long is left to hold reads
// as distance. Letting go (or dragging off) drains it in 200ms on the
// strong ease-out.
const HOLD_GROW_EASE = "linear";
const RELEASE_TRANSITION = {
	duration: 0.2,
	ease: [0.23, 1, 0.32, 1] as [number, number, number, number],
};

export interface HoldActionButtonProps
	extends Omit<
		HTMLMotionProps<"button">,
		| "children"
		| "type"
		| "onClick"
		| "onPointerDown"
		| "onPointerUp"
		| "onPointerCancel"
		| "onPointerLeave"
		| "onPointerMove"
		| "onKeyDown"
		| "onKeyUp"
	> {
	children: ReactNode;
	type?: "vertical" | "horizontal";
	holdingLabel?: ReactNode;
	completeLabel?: ReactNode;
	holdDuration?: number;
	onHoldComplete?: () => void;
	fillClassName?: string;
	labelClassName?: string;
	/** Applied to the "holding" and "complete" labels only, so they can read
	 * against a fill colour the idle label never sits on (the design turns
	 * the label white once the destructive gradient fills in behind it). */
	activeLabelClassName?: string;
	/** The hold is not allowed yet: a press shakes the button instead of starting. */
	refused?: boolean;
}

export const HoldActionButton = forwardRef<
	HTMLButtonElement,
	HoldActionButtonProps
>(function HoldActionButton(
	{
		children,
		type = "vertical",
		holdingLabel = "Keep holding",
		completeLabel = "Done",
		holdDuration = 2000,
		onHoldComplete,
		fillClassName,
		labelClassName,
		activeLabelClassName = "text-white",
		className,
		disabled,
		refused = false,
		...rest
	},
	ref,
) {
	const reduce = useReducedMotion();
	const completedRef = useRef(false);
	const holdTimer = useRef<number | null>(null);
	const [holding, setHolding] = useState(false);
	const [completed, setCompleted] = useState(false);

	useEffect(
		() => () => {
			if (holdTimer.current !== null) window.clearTimeout(holdTimer.current);
		},
		[],
	);

	// A completed hold stays full and ignores every press (`completedRef`,
	// `holding`) until something clears it. The caller disables the button
	// while the action runs, so enabling it again means the action ended
	// without closing the dialog — a failure the user may retry. Re-arm, and
	// the fill springs back so the next hold visibly fills again.
	useEffect(() => {
		if (disabled || !completedRef.current) return;
		completedRef.current = false;
		holdTimer.current = null;
		setHolding(false);
		setCompleted(false);
	}, [disabled]);

	const startHold = (target: HTMLElement) => {
		// The design shakes a hold that is not allowed yet (the typed name does
		// not match) instead of starting it: translateX -6, 5, -3 over 340 ms.
		if (refused && !disabled) {
			if (!reduce) {
				target.animate(
					[
						{ transform: "none" },
						{ transform: "translateX(-6px)" },
						{ transform: "translateX(5px)" },
						{ transform: "translateX(-3px)" },
						{ transform: "none" },
					],
					{ duration: 340 },
				);
			}
			return;
		}
		if (disabled || holding) return;
		completedRef.current = false;
		setCompleted(false);
		setHolding(true);
		holdTimer.current = window.setTimeout(() => {
			completedRef.current = true;
			setCompleted(true);
			onHoldComplete?.();
		}, holdDuration);
	};

	const cancelHold = () => {
		if (completedRef.current) return;
		if (holdTimer.current !== null) window.clearTimeout(holdTimer.current);
		holdTimer.current = null;
		setHolding(false);
		setCompleted(false);
	};

	const handlePointerDown = (event: PointerEvent<HTMLButtonElement>) => {
		if (event.button !== 0) return;
		// Start first: capture is a convenience, and a browser that refuses it
		// must not take the hold down with it.
		startHold(event.currentTarget);
		capturePointer(event.currentTarget, event.pointerId);
	};

	const handlePointerUp = (event: PointerEvent<HTMLButtonElement>) => {
		releasePointer(event.currentTarget, event.pointerId);
		cancelHold();
	};

	const handlePointerLeave = (event: PointerEvent<HTMLButtonElement>) => {
		// Only reached when the capture below was refused: a captured pointer gets
		// no boundary events at all. A finger does not get this reading either way
		// — WebKit fires leave repeatedly for a touch that never moved, which
		// would cancel every hold on the frame it started.
		if (event.pointerType !== "touch") cancelHold();
	};

	const handlePointerMove = (event: PointerEvent<HTMLButtonElement>) => {
		// Leaving the button abandons the hold, and while the pointer is captured
		// nothing announces that: no boundary events, and `touch-none` means a
		// finger sliding off cannot scroll and so cannot cancel either. Measure it
		// instead — off the button ends the hold, wherever the pointer then goes.
		if (!holding) return;

		const rect = event.currentTarget.getBoundingClientRect();
		const outside =
			event.clientX < rect.left ||
			event.clientX > rect.right ||
			event.clientY < rect.top ||
			event.clientY > rect.bottom;

		if (outside) cancelHold();
	};

	const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
		if ((event.key === " " || event.key === "Enter") && !event.repeat) {
			event.preventDefault();
			startHold(event.currentTarget);
		}
	};

	const handleKeyUp = (event: KeyboardEvent<HTMLButtonElement>) => {
		if (event.key === " " || event.key === "Enter") {
			event.preventDefault();
			cancelHold();
		}
	};

	const active = holding || completed;
	// The design's fill grows left-to-right (horizontal) or bottom-to-top
	// (vertical) as a flat scale, not a translating wipe — `transform-origin`
	// pins the growing edge so the opposite edge never moves.
	const growAxis = type === "horizontal" ? "scaleX" : "scaleY";
	const growOrigin = type === "horizontal" ? "left center" : "center bottom";

	return (
		<motion.button
			ref={ref}
			type="button"
			disabled={disabled}
			aria-label={typeof children === "string" ? children : undefined}
			onPointerDown={handlePointerDown}
			onPointerMove={handlePointerMove}
			onPointerUp={handlePointerUp}
			onPointerCancel={cancelHold}
			onPointerLeave={handlePointerLeave}
			onKeyDown={handleKeyDown}
			onKeyUp={handleKeyUp}
			onContextMenu={(event) => event.preventDefault()}
			data-pressed={holding || undefined}
			{...(reduce || disabled
				? {}
				: { whileTap: { scale: exact.press.scale } })}
			transition={SPRING_PRESS}
			className={cn(
				"btn-danger relative inline-grid h-9 min-w-0 touch-none place-items-center overflow-hidden rounded-[var(--hold-radius)] px-4",
				"[--hold-radius:var(--radius-ctl)]",
				TOUCH_GESTURE_CLASS,
				"disabled:pointer-events-none disabled:opacity-50",
				className,
			)}
			{...rest}
		>
			{/* Safari hands the fill its own compositing layer and then stops
          applying the button's rounded overflow clip to it, so a scaling
          fill bleeds past the corners. clip-path survives compositing. It
          lives on this static layer rather than the button so it never cuts
          the focus ring, and reads the same radius so the two cannot drift
          apart. */}
			<span
				aria-hidden="true"
				className="pointer-events-none absolute inset-0 [clip-path:inset(0_round_var(--hold-radius))]"
			>
				<motion.span
					aria-hidden="true"
					initial={false}
					style={{ transformOrigin: growOrigin }}
					animate={
						reduce
							? { opacity: active ? 1 : 0 }
							: { [growAxis]: active ? 1 : 0 }
					}
					transition={
						reduce
							? // Opacity is not movement, so it stays: it ramps over the whole
								// hold so the fill still shows how long to keep pressing, and only
								// reads full when the hold completes.
								active
								? { duration: holdDuration / 1000, ease: "linear" }
								: { duration: 0.15 }
							: active
								? { duration: holdDuration / 1000, ease: HOLD_GROW_EASE }
								: RELEASE_TRANSITION
					}
					className={cn(
						// Framer animates this element via the `transform` property (see
						// `initial`/`animate` above). A static `scale-x-0`/`scale-y-0`
						// Tailwind class sets the independent CSS `scale` property instead,
						// which composes with `transform` rather than being replaced by it —
						// that pinned this axis at zero forever and the fill never appeared.
						"absolute inset-0 bg-linear-to-b from-[oklch(0.55_0.19_22)] to-[oklch(0.48_0.18_22)] will-change-[opacity,transform]",
						fillClassName,
					)}
				/>
			</span>

			<span
				className={cn(
					"relative z-10 grid place-items-center font-semibold text-[13px] leading-none",
					labelClassName,
				)}
			>
				<motion.span
					animate={{ opacity: active ? 0 : 1 }}
					transition={{ duration: reduce ? 0 : 0.12, ease: EASE_OUT }}
					className="col-start-1 row-start-1"
				>
					{children}
				</motion.span>
				<motion.span
					aria-hidden={!holding || completed}
					animate={{ opacity: holding && !completed ? 1 : 0 }}
					transition={{ duration: reduce ? 0 : 0.12, ease: EASE_OUT }}
					className={cn("col-start-1 row-start-1", activeLabelClassName)}
				>
					{holdingLabel}
				</motion.span>
				<motion.span
					aria-hidden={!completed}
					animate={{ opacity: completed ? 1 : 0 }}
					transition={{ duration: reduce ? 0 : 0.12, ease: EASE_OUT }}
					className={cn("col-start-1 row-start-1", activeLabelClassName)}
				>
					{completeLabel}
				</motion.span>
			</span>
		</motion.button>
	);
});
