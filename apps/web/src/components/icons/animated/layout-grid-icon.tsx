// Adapted from lucide-animated icons/layout-grid.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `layout-grid` (ISC).
//
// The four tiles each slide to the next corner and back in 500ms when
// `animate` turns true.

import { motion, type Transition, type Variants } from "motion/react";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const TIMING: Transition = {
	duration: 0.5,
	ease: "easeInOut",
	times: [0, 0.4, 0.6, 1],
};

function slide(x: number, y: number): Variants {
	return {
		normal: { x: 0, y: 0 },
		animate: { x: [0, x, x, 0], y: [0, y, y, 0], transition: TIMING },
	};
}

const TOP_LEFT = slide(11, 0);
const TOP_RIGHT = slide(0, 11);
const BOTTOM_RIGHT = slide(-11, 0);
const BOTTOM_LEFT = slide(0, -11);

export function LayoutGridIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} {...props}>
			<motion.rect
				width="7"
				height="7"
				x="3"
				y="3"
				rx="1"
				variants={TOP_LEFT}
			/>
			<motion.rect
				width="7"
				height="7"
				x="14"
				y="3"
				rx="1"
				variants={TOP_RIGHT}
			/>
			<motion.rect
				width="7"
				height="7"
				x="14"
				y="14"
				rx="1"
				variants={BOTTOM_RIGHT}
			/>
			<motion.rect
				width="7"
				height="7"
				x="3"
				y="14"
				rx="1"
				variants={BOTTOM_LEFT}
			/>
		</IconSvg>
	);
}
