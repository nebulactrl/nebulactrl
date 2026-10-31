// Adapted from lucide-animated icons/copy.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `copy` (ISC).
//
// The two sheets slide apart by 3 units in 200ms while `animate` is true.

import { motion, type Variants } from "motion/react";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const SLIDE = { duration: 0.2, ease: "easeOut" } as const;

const FRONT: Variants = {
	normal: { x: 0, y: 0 },
	animate: { x: -3, y: -3, transition: SLIDE },
};

const BACK: Variants = {
	normal: { x: 0, y: 0 },
	animate: { x: 3, y: 3, transition: SLIDE },
};

export function CopyIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} {...props}>
			<motion.rect
				width="14"
				height="14"
				x="8"
				y="8"
				rx="2"
				ry="2"
				variants={FRONT}
			/>
			<motion.path
				d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"
				variants={BACK}
			/>
		</IconSvg>
	);
}
