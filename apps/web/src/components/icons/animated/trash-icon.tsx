// Adapted from lucide-animated icons/delete.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `trash-2` (ISC).
//
// Upstream calls this icon `delete`; it is Lucide's trash can. The lid
// lifts and tilts open in 280ms when `animate` turns true and stays open
// until it ends, so a confirm dialog shows the can with its lid up.
// (Upstream also morphs the body path and starts it 2 units below the lid;
// here the body is Lucide's, joined to the lid, so the icon at rest is
// pixel-identical to the static `trash-2` it replaces, and only transforms
// move.)

import { motion, type Variants } from "motion/react";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const LID: Variants = {
	normal: { y: 0, rotate: 0, transition: { duration: 0.18, ease: "easeOut" } },
	animate: {
		y: [0, -2.4, -1.6],
		rotate: [0, -12, -8],
		transition: { duration: 0.28, ease: "easeOut" },
	},
};

export function TrashIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} {...props}>
			<motion.g variants={LID} style={{ originX: 0.15, originY: 1 }}>
				<path d="M3 6h18" />
				<path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2" />
			</motion.g>
			<path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6" />
			<line x1="10" x2="10" y1="11" y2="17" />
			<line x1="14" x2="14" y1="11" y2="17" />
		</IconSvg>
	);
}
