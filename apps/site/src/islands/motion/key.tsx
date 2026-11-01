import { exact } from "@nebulactrl/tokens";
import { AnimatePresence, LazyMotion, m, useReducedMotion } from "motion/react";
import type { ComponentProps, ReactNode } from "react";

/**
 * The site's microanimations: one
 * `motion` setup shared by the islands that hydrate in view. Always
 * `LazyMotion` with `m` and `domAnimation`, so every island shares one small
 * feature chunk, fetched after the island hydrates (`features.ts`): the key
 * renders and presses through CSS until it arrives.
 */

const loadFeatures = () => import("./features.ts").then((mod) => mod.default);

/** The app's press spring (apps/web/src/lib/ease.ts, SPRING_PRESS): quick, no bounce on a console. */
const SPRING_PRESS = {
	type: "spring",
	stiffness: 500,
	damping: 30,
	mass: 0.6,
} as const;

/** A label or an icon trading places inside a key (SPRING_SWAP). */
const SPRING_SWAP = {
	type: "spring",
	stiffness: 460,
	damping: 30,
	mass: 0.55,
} as const;

/**
 * A raised key with a spring press. The look is the `btn-*` utility, which
 * also sinks the key onto its --key-travel edge with `translate` while it is
 * pressed; motion adds the small `whileTap` scale on `transform`, so the two
 * compose. No `pressable`: a CSS transform transition would fight the spring.
 * Reduced motion keeps the sink (feedback) and drops the spring.
 */
export function MotionKey({
	children,
	className,
	disabled,
	...rest
}: Omit<ComponentProps<typeof m.button>, "children"> & {
	children: ReactNode;
}) {
	const reduce = useReducedMotion();
	return (
		<LazyMotion features={loadFeatures} strict>
			<m.button
				{...(reduce || disabled
					? {}
					: { whileTap: { scale: exact.key.scale } })}
				transition={SPRING_PRESS}
				disabled={disabled}
				className={`key-motion ${className ?? ""}`.trim()}
				{...rest}
			>
				{children}
			</m.button>
		</LazyMotion>
	);
}

/**
 * One slot in a key that swaps its content: the old part leaves as the new
 * one arrives from 0.25 scale with a 4px blur (exact.iconSwap), on SPRING_SWAP.
 * `swapKey` names the content; a new key plays the swap.
 */
export function Swap({
	swapKey,
	children,
}: {
	swapKey: string;
	children: ReactNode;
}) {
	const reduce = useReducedMotion();
	const from = reduce
		? { opacity: 0 }
		: {
				opacity: 0,
				scale: exact.iconSwap.scaleFrom,
				filter: `blur(${exact.iconSwap.blur}px)`,
			};
	return (
		<span className="relative inline-grid place-items-center">
			<AnimatePresence initial={false} mode="popLayout">
				<m.span
					key={swapKey}
					className="inline-flex items-center"
					initial={from}
					animate={{ opacity: 1, scale: 1, filter: "blur(0px)" }}
					exit={from}
					transition={reduce ? { duration: 0.12 } : SPRING_SWAP}
				>
					{children}
				</m.span>
			</AnimatePresence>
		</span>
	);
}
