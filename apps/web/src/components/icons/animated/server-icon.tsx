// Adapted from lucide-animated icons/server.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `server` (ISC).
//
// The two units swap places and settle in 600ms when `animate` turns true
// (upstream repeats the swap twice).

import { motion, type Transition, type Variants } from "motion/react";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const TIMING: Transition = {
	duration: 0.6,
	ease: "easeInOut",
	times: [0, 0.35, 0.65, 1],
};

const TOP: Variants = {
	normal: { y: 0 },
	animate: { y: [0, 12, 12, 0], transition: TIMING },
};

const BOTTOM: Variants = {
	normal: { y: 0 },
	animate: { y: [0, -12, -12, 0], transition: TIMING },
};

export function ServerIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} className="overflow-visible" {...props}>
			<motion.g variants={TOP}>
				<rect width="20" height="8" x="2" y="2" rx="2" ry="2" />
				<line x1="6" x2="10" y1="6" y2="6" />
			</motion.g>
			<motion.g variants={BOTTOM}>
				<rect width="20" height="8" x="2" y="14" rx="2" ry="2" />
				<line x1="6" x2="10" y1="18" y2="18" />
			</motion.g>
		</IconSvg>
	);
}
