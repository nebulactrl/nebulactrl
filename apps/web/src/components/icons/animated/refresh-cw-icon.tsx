// Adapted from lucide-animated icons/refresh-cw.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `refresh-cw` (ISC).
//
// Upstream tilts 50° on hover. Here the icon is the "running" cue: it spins
// while `animate` is true and, when it stops, finishes the turn in progress
// instead of snapping back to 0°.

import {
	type AnimationPlaybackControls,
	animate,
	useMotionValue,
	useReducedMotion,
} from "motion/react";
import { useEffect, useRef } from "react";
import { Spinner } from "#/components/ui/spinner";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const TURN_SECONDS = 0.9;
const SETTLE_SECONDS = 0.45;

export function RefreshCwIcon({
	animate: spinning,
	ref,
	...props
}: AnimatedIconProps) {
	const playing = usePlaying(spinning, ref);
	const rotate = useMotionValue(0);
	const settling = useRef<AnimationPlaybackControls | undefined>(undefined);

	useEffect(() => {
		if (!playing) return;
		settling.current?.stop();
		const from = rotate.get();
		const spin = animate(rotate, [from, from + 360], {
			duration: TURN_SECONDS,
			ease: "linear",
			repeat: Number.POSITIVE_INFINITY,
		});
		return () => {
			spin.stop();
			const current = rotate.get();
			const target = Math.ceil(current / 360) * 360;
			if (target === current) {
				rotate.set(0);
				return;
			}
			settling.current = animate(rotate, target, {
				duration: (SETTLE_SECONDS * (target - current)) / 360,
				ease: "easeOut",
				onComplete: () => rotate.set(0),
			});
		};
	}, [playing, rotate]);

	return (
		<IconSvg playing={false} style={{ rotate }} {...props}>
			<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8" />
			<path d="M21 3v5h-5" />
			<path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16" />
			<path d="M8 16H3v5" />
		</IconSvg>
	);
}

/**
 * The refresh glyph of a redeploy action: it turns while the request is
 * pending. Under reduced motion it cannot turn, so the pending state shows
 * the static progress ring instead of an unchanged icon.
 */
export function PendingRefreshIcon({
	pending,
	...props
}: Omit<AnimatedIconProps, "animate" | "ref"> & { pending: boolean }) {
	const reducedMotion = useReducedMotion() ?? false;
	if (pending && reducedMotion) return <Spinner />;
	return <RefreshCwIcon animate={pending} {...props} />;
}
