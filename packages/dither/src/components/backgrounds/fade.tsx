import { cn } from "cn";
import type { CSSProperties } from "react";
import type { BayerSize, DitherMatrix } from "../../lib/bayer";
import { DitherShader } from "../../lib/gl";
import { DEFAULT_CELL, ditherRampStyle } from "../../lib/pattern";
import { cssColor, PAPER } from "./common";
import { fadeBandSize, fadeBandTones } from "./fade-bands";
import { pickMatrix } from "./matrix";

export type FadeSide = "top" | "bottom" | "left" | "right";

const EDGE: Record<FadeSide, string> = {
	bottom: "inset-x-0 bottom-0",
	top: "inset-x-0 top-0",
	left: "inset-y-0 left-0",
	right: "inset-y-0 right-0",
};

// Animated edge: depth runs 0 on the content side to 1 at the solid edge, and
// noise lets the boundary wander. fdDir is the unit axis pointing at the edge.
const DRIFT_FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  float depth = dot(uv - 0.5, fdDir) + 0.5;
  float along = dot(uv - 0.5, vec2(fdDir.y, fdDir.x)) * u_aspect;
  float wander = fbm(vec2(along * 3.0 + t * 0.05, depth * 2.0 - t * 0.03)) - 0.5;
  float tone = clamp(depth * 1.15 - 0.1 + wander * 0.4, 0.0, 1.0);
  return max(tone, smoothstep(0.92, 1.0, depth));
}
`;

const AXIS: Record<FadeSide, readonly [number, number]> = {
	bottom: [0, -1],
	top: [0, 1],
	left: [-1, 0],
	right: [1, 0],
};

export interface DitherFadeProps {
	/** The edge of the parent the fade sits on. The colour is solid there. Default "bottom". */
	side?: FadeSide | undefined;
	/** Depth of the fade. A number is CSS pixels; a string is any CSS length such as "30%". Default 160. */
	size?: number | string | undefined;
	/** The colour the content fades into: a `--token` name or any CSS colour. Default `--dither-paper`. */
	color?: string | undefined;
	/** Threshold pattern, with blue noise. Wins over `n`. Default "bayer4". */
	matrix?: DitherMatrix | undefined;
	/** Bayer matrix size. `matrix` is the same choice with blue noise added. Default 4. */
	n?: BayerSize | undefined;
	/** Size of one dither cell in CSS pixels. Default `--dither-cell`. */
	cell?: number | undefined;
	/** Let the edge drift slowly, drawn with WebGL. Keep it for one hero-sized seam. Default false. */
	animate?: boolean | undefined;
	className?: string | undefined;
	style?: CSSProperties | undefined;
}

/**
 * A dithered edge that fades a section's content into a solid colour. Put it
 * inside a `relative` parent. The still fade is one element with one CSS mask
 * (`ditherRamp`): it renders on the server and uses no WebGL.
 */
export function DitherFade({
	side = "bottom",
	size = 160,
	color = PAPER,
	matrix,
	n,
	cell,
	animate = false,
	className,
	style,
}: DitherFadeProps) {
	const pattern = pickMatrix(matrix, n, "bayer4");
	const vertical = side === "top" || side === "bottom";
	const box: CSSProperties = {
		...(vertical ? { height: size } : { width: size }),
		...style,
	};
	const classes = cn(
		"pointer-events-none absolute forced-colors:hidden",
		EDGE[side],
		className,
	);

	if (animate) {
		const [dx, dy] = AXIS[side];
		return (
			<DitherShader
				className={classes}
				style={box}
				field={DRIFT_FIELD}
				palette={["transparent", color]}
				matrix={pattern}
				cell={cell}
				staticTime={2}
				uniforms={{ fdDir: [dx, dy] }}
				aria-hidden
			/>
		);
	}

	const tones = fadeBandTones(fadeBandSize(pattern));
	return (
		<div
			aria-hidden
			className={classes}
			style={{
				...box,
				backgroundColor: cssColor(color),
				...ditherRampStyle({
					edge: side,
					length: size,
					from: 1,
					to: tones[0] ?? 1,
					steps: tones.length,
					matrix: pattern,
					cell: cell ?? DEFAULT_CELL,
				}),
			}}
		/>
	);
}
