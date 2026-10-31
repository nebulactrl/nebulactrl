import { cn } from "cn";
import type { ReactNode } from "react";
import { useId } from "react";
import { NEBULA_MARK_PETALS, NEBULA_MARK_STARS } from "./nebula-mark";
import {
	MARK_G_TIERS,
	MARK_G_WINDOW,
	type MarkGTier,
} from "./nebula-mark-g.generated";

/** Below this display size the mark is the plain solid gradient mark: dither cells under 24 px read as noise. */
export const NEBULA_MARK_DITHER_MIN = 24;

/** Deep ink of the shadow and the shaded bevel. */
const DEEP = "#2a2891";

/** The generated grid built for the display size nearest to `size`. */
export function markTier(size: number): MarkGTier {
	let best = MARK_G_TIERS[0] as MarkGTier;
	for (const tier of MARK_G_TIERS) {
		if (Math.abs(tier.size - size) < Math.abs(best.size - size)) best = tier;
	}
	return best;
}

export interface NebulaMarkProps {
	/** Display size in px (the mark is square). From 24 it is drawn embossed over a dithered shadow; below, solid. */
	size?: number;
	className?: string;
	/** Accessible name. Without it the mark is decorative (`aria-hidden`). */
	label?: string;
	/**
	 * Splay on hover: the petals open, four stars fly out and the mark tilts
	 * with a soft glow when it, or an enclosing link or button, is hovered or
	 * keyboard-focused (`.nc-mark-*` rules in @nebulactrl/tokens/dither.css).
	 * The emboss and the shadow fade while it moves; reduced motion only
	 * shows the stars. The app's logo uses it; the site's marks stay still.
	 */
	hover?: boolean;
}

/**
 * The NebulaCtrl mark, variant G: the brand gradient fully crisp, a lit
 * top-left and a shaded bottom-right bevel, and a Bayer dithered drop shadow
 * in deep ink. The cells are generated at build time per display size
 * (`scripts/gen-mark.ts`), so no canvas, filter or blur is involved and the
 * result is the same at any pixel ratio. At rest it is still; `hover` opts
 * into the splay. Below 24 px, and in forced colours, only the solid gradient
 * petals are drawn.
 */
export function NebulaMark({
	size = 24,
	className,
	label,
	hover = false,
}: NebulaMarkProps): ReactNode {
	const uid = useId().replaceAll(":", "");
	const treated = size >= NEBULA_MARK_DITHER_MIN;
	const tier = markTier(size);
	const { x, y, size: win } = MARK_G_WINDOW;
	const cell = win / tier.cells;
	const ids = {
		gradient: `${uid}g`,
		clip: `${uid}c`,
		light: `${uid}l`,
		shade: `${uid}s`,
		petals: NEBULA_MARK_PETALS.map((_, i) => `${uid}p${i}`),
	};
	const uses = ids.petals.map((id) => <use key={id} href={`#${id}`} />);
	const rect = { x, y, width: win, height: win } as const;

	return (
		<svg
			viewBox={`${x} ${y} ${win} ${win}`}
			width={size}
			height={size}
			className={cn("block shrink-0", hover && "nc-mark", className)}
			role={label ? "img" : undefined}
			aria-label={label}
			aria-hidden={label ? undefined : true}
		>
			{label ? <title>{label}</title> : null}
			<defs>
				<linearGradient
					id={ids.gradient}
					gradientUnits="userSpaceOnUse"
					x1="22"
					y1="249"
					x2="244"
					y2="23"
				>
					<stop style={{ stopColor: "var(--brand-mark-from, #9a6bff)" }} />
					<stop
						offset=".55"
						style={{ stopColor: "var(--brand-mark-mid, #6d6bff)" }}
					/>
					<stop
						offset="1"
						style={{ stopColor: "var(--brand-mark-to, #4fa8ff)" }}
					/>
				</linearGradient>
				{NEBULA_MARK_PETALS.map((d, i) => (
					<path key={ids.petals[i]} id={ids.petals[i]} d={d} />
				))}
				{treated ? (
					<>
						<clipPath id={ids.clip}>{uses}</clipPath>
						<mask id={ids.light} maskUnits="userSpaceOnUse" {...rect}>
							<rect {...rect} fill="#fff" />
							<g
								fill="#000"
								transform={`translate(${tier.bevel} ${tier.bevel})`}
							>
								{uses}
							</g>
						</mask>
						<mask id={ids.shade} maskUnits="userSpaceOnUse" {...rect}>
							<rect {...rect} fill="#fff" />
							<g
								fill="#000"
								transform={`translate(${-tier.bevel} ${-tier.bevel})`}
							>
								{uses}
							</g>
						</mask>
					</>
				) : null}
			</defs>
			{treated ? (
				<g className={cn("forced-colors:hidden", hover && "nc-mark-rest")}>
					<path
						shapeRendering="crispEdges"
						fill={DEEP}
						fillOpacity={0.9}
						transform={`translate(${x} ${y}) scale(${cell})`}
						d={tier.shadow}
					/>
				</g>
			) : null}
			<g fill={`url(#${ids.gradient})`}>
				{hover
					? ids.petals.map((id, i) => (
							<use
								key={id}
								href={`#${id}`}
								className={`nc-mark-petal nc-mark-petal-${i + 1}`}
							/>
						))
					: uses}
			</g>
			{treated ? (
				<g className={cn("forced-colors:hidden", hover && "nc-mark-rest")}>
					<rect
						{...rect}
						fill="#fff"
						fillOpacity={0.45}
						clipPath={`url(#${ids.clip})`}
						mask={`url(#${ids.light})`}
					/>
					<rect
						{...rect}
						fill={DEEP}
						fillOpacity={0.5}
						clipPath={`url(#${ids.clip})`}
						mask={`url(#${ids.shade})`}
					/>
				</g>
			) : null}
			{hover
				? NEBULA_MARK_STARS.map((star, i) => (
						<circle
							// biome-ignore lint/suspicious/noArrayIndexKey: four fixed stars
							key={i}
							className={`nc-mark-star nc-mark-star-${i + 1} forced-colors:hidden`}
							cx="0"
							cy="0"
							r={star.r}
							style={{
								fill: "var(--brand-mark-star, #eef0ff)",
								["--nc-mark-star-to" as string]: `${star.cx}px, ${star.cy}px`,
							}}
						/>
					))
				: null}
		</svg>
	);
}
