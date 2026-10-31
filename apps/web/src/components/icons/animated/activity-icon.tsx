// Adapted from lucide-animated icons/activity.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `activity` (ISC).
//
// Upstream draws the pulse line in. Here the line beats once, stretching
// vertically and settling, in 400ms when `animate` turns true (transform
// only).

import type { Variants } from "motion/react";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const BEAT: Variants = {
	normal: { scaleY: 1 },
	animate: {
		scaleY: [1, 1.22, 0.88, 1],
		transition: { duration: 0.4, ease: "easeOut", times: [0, 0.3, 0.65, 1] },
	},
};

export function ActivityIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} variants={BEAT} {...props}>
			<path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2" />
		</IconSvg>
	);
}
