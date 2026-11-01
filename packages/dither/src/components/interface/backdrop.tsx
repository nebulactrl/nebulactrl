import { cn } from "cn";
import { DitherShader } from "../../lib/gl";
import { useDitherPalette } from "../../lib/tokens";

// A slow interference of three waves under a vignette: soft cloud in the
// middle, nothing at the edges, so it never competes with the text above it.
const BACKDROP_FIELD = /* glsl */ `
float field(vec2 uv, float t) {
	vec2 p = (uv - 0.5) * vec2(u_aspect, 1.0);
	float w = sin(p.x * 7.0 + t * 0.35) + sin(p.y * 9.0 - t * 0.28) + sin((p.x + p.y) * 5.0 + t * 0.2);
	float cloud = 0.5 + 0.5 * w / 3.0;
	float vignette = smoothstep(0.78, 0.05, length(p));
	return cloud * vignette * 0.6;
}
`;

export interface DitherBackdropProps {
	/** Drift slowly. A still frame is drawn under reduced motion either way. */
	animate?: boolean;
	className?: string;
}

/**
 * A subtle dither field to sit behind an illustration or a message. Fills its
 * nearest positioned ancestor, sits below the content (`-z-10`), is hidden
 * from assistive tech, and falls back to a plain accent wash without WebGL.
 */
function DitherBackdrop({ animate = true, className }: DitherBackdropProps) {
	const palette = useDitherPalette();
	return (
		<DitherShader
			aria-hidden
			field={BACKDROP_FIELD}
			palette={[palette.paper, palette.accent]}
			cell={3}
			fps={12}
			animate={animate}
			staticTime={3}
			className={cn(
				"pointer-events-none absolute inset-0 -z-10 size-full opacity-35 [mask-image:radial-gradient(closest-side,#000,transparent)] forced-colors:hidden",
				className,
			)}
			fallback={
				<div
					aria-hidden
					className="pointer-events-none absolute inset-0 -z-10 bg-[radial-gradient(closest-side,var(--color-accent-wash),transparent)]"
				/>
			}
		/>
	);
}

export { DitherBackdrop };
