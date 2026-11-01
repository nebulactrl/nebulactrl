import { ditherRampMask } from "@nebulactrl/dither/lib/pattern";
import type { CSSProperties } from "react";

/**
 * The dashboard, as the app draws it: a real capture of a project's production
 * canvas. The bottom of the plate dissolves into the void through an ordered
 * dither ramp (the same Bayer pattern as the nebula), so the picture melts into
 * dots and not into a grey gradient.
 *
 * The mask is two layers on the whole plate, so its border fades with it: a
 * solid layer over the top 86% and the ramp under it, one tile wide and 14% of
 * the plate long, below the service cards, so none of them dissolves. The ramp runs from nothing at the bottom edge to fully inked
 * where it meets the solid part ("inked" is opaque in a mask), in 16 bands of a
 * 3px cell.
 */
const RAMP = ditherRampMask({
	edge: "bottom",
	length: "100%",
	from: 0,
	to: 1,
	steps: 16,
	cell: 3,
});
const TILE = RAMP.size.split(" ")[0] as string;
const FADE = "14%";
const SOLID = "86.5%";

const MASK: CSSProperties = {
	WebkitMaskImage: `${RAMP.image}, linear-gradient(#000, #000)`,
	WebkitMaskRepeat: `${RAMP.repeat}, no-repeat`,
	WebkitMaskSize: `${TILE} ${FADE}, 100% ${SOLID}`,
	WebkitMaskPosition: `${RAMP.position}, 0 0`,
	maskImage: `${RAMP.image}, linear-gradient(#000, #000)`,
	maskRepeat: `${RAMP.repeat}, no-repeat`,
	maskSize: `${TILE} ${FADE}, 100% ${SOLID}`,
	maskPosition: `${RAMP.position}, 0 0`,
};

const SIZES = "(min-width: 1280px) 1240px, 100vw";

/**
 * The screen under the hero: a raised window plate at the container's full
 * width, the capture inside it (the app's own chrome, so none is drawn here),
 * and a one line caption. The figure rises 64px into the hero from 1024px, so
 * the hero's gas glows behind its top corners. It reveals once as it first
 * comes into view (`data-reveal`, see `initReveal` in `enhance.ts`); without a
 * script it is simply there.
 */
export function Screen() {
	return (
		<figure className="relative m-0 lg:-mt-16" data-reveal="">
			<div className="screen-plate" style={MASK}>
				<picture>
					<source
						media="(max-width: 767px)"
						type="image/avif"
						srcSet="/shots/dashboard-canvas-mobile.avif"
						width={1440}
						height={1280}
					/>
					<source
						media="(max-width: 767px)"
						type="image/webp"
						srcSet="/shots/dashboard-canvas-mobile.webp"
						width={1440}
						height={1280}
					/>
					<source
						type="image/avif"
						srcSet="/shots/dashboard-canvas-1440.avif 1440w, /shots/dashboard-canvas-2880.avif 2880w"
						sizes={SIZES}
					/>
					<source
						type="image/webp"
						srcSet="/shots/dashboard-canvas-1440.webp 1440w, /shots/dashboard-canvas-2880.webp 2880w"
						sizes={SIZES}
					/>
					<img
						src="/shots/dashboard-canvas-1440.webp"
						width={1440}
						height={900}
						alt="The NebulaCtrl dashboard: the production canvas of a project called Acme Shop, with the web, api and postgres services live behind the public domain shop.localhost."
						loading="lazy"
						decoding="async"
						className="block h-auto w-full"
					/>
				</picture>
			</div>
		</figure>
	);
}
