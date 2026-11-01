// Adapted from lucide-animated icons/settings.tsx
// (https://github.com/pqoqubbw/icons, MIT, Copyright (c) 2024-2026 pqoqubbw;
// see ./LICENSE). Artwork is Lucide's `settings` (ISC).
//
// Upstream turns the gear 180° on a slow spring. Here it turns a quarter
// turn in 280ms while `animate` is true (the gear repeats every 45°, so a
// quarter turn reads clearly) and turns back when it ends.

import type { Variants } from "motion/react";
import { type AnimatedIconProps, IconSvg, usePlaying } from "./icon-base";

const TURN: Variants = {
	normal: { rotate: 0, transition: { duration: 0.2, ease: "easeOut" } },
	animate: { rotate: 90, transition: { duration: 0.28, ease: "easeOut" } },
};

export function SettingsIcon({ animate, ref, ...props }: AnimatedIconProps) {
	const playing = usePlaying(animate, ref);
	return (
		<IconSvg playing={playing} variants={TURN} {...props}>
			<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
			<circle cx="12" cy="12" r="3" />
		</IconSvg>
	);
}
