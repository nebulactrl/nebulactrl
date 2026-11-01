// Adapted from lucide-animated icons/circle-check.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `circle-check` (ISC).
//
// The ring is static and the tick draws itself in 280ms when `animate`
// turns true (and on mount with `animate`). Used by the deploy toast.

import { motion, type Variants } from "motion/react";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const DRAW: Variants = {
	normal: { opacity: 1, pathLength: 1 },
	animate: {
		opacity: [0, 1],
		pathLength: [0, 1],
		transition: {
			duration: 0.28,
			ease: "easeOut",
			opacity: { duration: 0.08 },
		},
	},
};

export function CircleCheckIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} {...props}>
			<circle cx="12" cy="12" r="10" />
			<motion.path d="m9 12 2 2 4-4" variants={DRAW} />
		</IconSvg>
	);
}
