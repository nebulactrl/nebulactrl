import { cn } from "cn";
import type * as React from "react";

// The design draws every spinner as a plain ring (a faint track under an
// accent-ink arc), never an icon glyph. An
// `<svg>` root — not a `<span>` — keeps `button.tsx`'s
// `[&_svg:not([class*='size-'])]:size-4` (and its per-size overrides) auto-
// sizing this the same way it already sizes every other inline button icon.
// The width and height attributes are its 16px default everywhere else:
// presentation attributes lose to any CSS size, so a `size-*` class or a
// button's sizing still wins, but a spinner on its own never stretches to
// fill its container.
function Spinner({ className, ...props }: React.ComponentProps<"svg">) {
	return (
		<svg
			data-slot="spinner"
			role="status"
			aria-label="Loading"
			width={16}
			height={16}
			viewBox="0 0 16 16"
			fill="none"
			className={cn("animate-nc-spin", className)}
			{...props}
		>
			<circle
				cx="8"
				cy="8"
				r="6.2"
				stroke="var(--color-line-strong)"
				strokeWidth="1.6"
			/>
			<path
				d="M8 1.8a6.2 6.2 0 0 1 6.2 6.2"
				stroke="var(--color-accent-ink)"
				strokeWidth="1.6"
				strokeLinecap="round"
			/>
		</svg>
	);
}

export { Spinner };
