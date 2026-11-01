// Adapted from lucide-animated icons/check.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `check` (ISC).
//
// The tick draws itself in 280ms when `animate` turns true (and on mount
// with `animate`), the same stroke Lucide's static `check` shows at rest.

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

export function CheckIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} {...props}>
			<motion.path d="M4 12 9 17L20 6" variants={DRAW} />
		</IconSvg>
	);
}
