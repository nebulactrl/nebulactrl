// Adapted from lucide-animated icons/rocket.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `rocket` (ISC).
//
// Upstream loops a 6s shake with a morphing flame. Here the rocket plays
// once, 520ms, when `animate` turns true: a small shudder on the pad, then
// lift-off up and to the right and a settle back, with the flame flickering.

import { motion, type Variants } from "motion/react";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const LAUNCH: Variants = {
	normal: { x: 0, y: 0 },
	animate: {
		x: [0, -0.5, 0.5, 0, 2.5, 0],
		y: [0, 0.5, -0.5, 0, -2.5, 0],
		transition: {
			duration: 0.52,
			ease: "easeOut",
			times: [0, 0.14, 0.28, 0.4, 0.75, 1],
		},
	},
};

const FLAME: Variants = {
	normal: { opacity: 1 },
	animate: {
		opacity: [1, 0.35, 1, 0.5, 1],
		transition: { duration: 0.52, ease: "linear" },
	},
};

export function RocketIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} {...props}>
			<motion.g variants={LAUNCH}>
				<motion.path
					d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09z"
					variants={FLAME}
				/>
				<path d="m12 15-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z" />
				<path d="M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0" />
				<path d="M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5" />
			</motion.g>
		</IconSvg>
	);
}
