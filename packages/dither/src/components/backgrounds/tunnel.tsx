import { DitherShader } from "../../lib/gl";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	PAPER,
	shaderBase,
} from "./common";

// Flight down a tunnel. Screen radius r is the inverse of depth, so a ring at
// fixed depth Z sits at r = 1 / (Z - v t). Line widths are measured in screen
// cells (|dr/dz| = r^2), and anything that would crowd closer than a few cells
// fades out, so the vanishing point never breaks into moire.
const FIELD = /* glsl */ `
float tnSpoke(vec2 p, float r, out float lenWall) {
  float circle = atan(p.y, p.x) / 6.2831853 + 0.5;
  vec2 ap = abs(p);
  float side;
  float w;
  if (ap.x > ap.y) {
    side = p.x > 0.0 ? 0.0 : 2.0;
    w = (p.x > 0.0 ? p.y : -p.y) / max(ap.x, 1e-4);
  } else {
    side = p.y > 0.0 ? 1.0 : 3.0;
    w = (p.y > 0.0 ? -p.x : p.x) / max(ap.y, 1e-4);
  }
  float square = (side + (w + 1.0) * 0.5) / 4.0;
  lenWall = mix(6.2831853 * r, 8.0 * r, tnSquare);
  return mix(circle, square, tnSquare);
}
float field(vec2 uv, float t) {
  float a = u_aspect;
  float cellH = 1.0 / u_res.y;
  float hc = 0.7 * cellH;
  vec2 ctr = vec2(tnCenter.x, 1.0 - tnCenter.y)
    + tnSway * 0.05 * vec2(sin(t * 0.23) + 0.5 * sin(t * 0.51), cos(t * 0.19) + 0.5 * cos(t * 0.43));
  vec2 p = (uv - ctr) * vec2(a, 1.0);
  float r = mix(length(p), max(abs(p.x), abs(p.y)), tnSquare);
  r = max(r, 1e-3);

  float density = tnRings * 0.6;
  float zr = (1.0 / r + t * 0.5) * density;
  float fz = fract(zr);
  float dz = min(fz, 1.0 - fz) / density;
  float dr = dz * r * r;
  float ring = 1.0 - smoothstep(0.5 * hc, 1.6 * hc, dr);
  float ringGap = r * r / density;
  ring *= smoothstep(2.5 * cellH, 6.0 * cellH, ringGap);

  float spoke = 0.0;
  float tile = 0.0;
  if (tnSpokes > 0.5) {
    float wallLen;
    float sp = tnSpoke(p, r, wallLen);
    float fs = fract(sp * tnSpokes);
    float ds = min(fs, 1.0 - fs) / tnSpokes;
    spoke = 1.0 - smoothstep(0.5 * hc, 1.6 * hc, ds * wallLen);
    float spokeGap = wallLen / tnSpokes;
    float crowd = smoothstep(2.5 * cellH, 6.0 * cellH, spokeGap);
    spoke *= crowd;
    float checker = mod(floor(zr) + floor(sp * tnSpokes), 2.0);
    tile = checker * 0.2 * crowd * smoothstep(2.5 * cellH, 6.0 * cellH, ringGap);
  }

  float fogR = 0.07 * tnFog;
  float fog = smoothstep(fogR * 0.6, fogR * 2.4, r);
  float depthLight = 0.55 + 0.45 * smoothstep(0.1, 0.6, r);
  return clamp((max(ring, spoke) + tile) * fog * depthLight, 0.0, 1.0);
}
`;

export interface DitherTunnelProps extends Omit<BackgroundBaseProps, "shape"> {
	/** The cross-section of the tunnel. Default "circle". */
	profile?: "circle" | "square" | undefined;
	/**
	 * dithercn's name for `profile`: "circle" or "square". `"dot"` is still
	 * accepted and means round dither cells (see `cellShape`).
	 */
	shape?: "circle" | "square" | "dot" | undefined;
	/** Cell shape: "square" or "dot". */
	cellShape?: "square" | "dot" | undefined;
	/** How close together the rings are. 2 packs twice as many in. Default 1. */
	rings?: number | undefined;
	/** Lines along the walls, 0 to 32. 0 leaves only the rings and drops the tiles. Default 12. */
	spokes?: number | undefined;
	/** The vanishing point as fractions of the box from the top left. Default [0.5, 0.5]. */
	center?: readonly [number, number] | undefined;
	/** How far the vanishing point wanders. 0 holds it still. Default 1. */
	sway?: number | undefined;
	/** Size of the empty fog in the middle. 2 doubles it to clear room for text. Default 1. */
	fog?: number | undefined;
}

const DEFAULT_PALETTE = [PAPER, INK];
const CENTER: readonly [number, number] = [0.5, 0.5];

/** A slow flight down a tunnel of round or square rings. */
export function DitherTunnel({
	profile = "circle",
	shape,
	cellShape,
	rings = 1,
	spokes = 12,
	center = CENTER,
	sway = 1,
	fog = 1,
	palette = DEFAULT_PALETTE,
	...rest
}: DitherTunnelProps) {
	const section = shape === "circle" || shape === "square" ? shape : profile;
	const cells = cellShape ?? (shape === "dot" ? "dot" : undefined);
	return (
		<DitherShader
			{...shaderBase({ ...rest, shape: cells }, { staticTime: 3 })}
			field={FIELD}
			palette={palette}
			uniforms={{
				tnSquare: section === "square" ? 1 : 0,
				tnRings: Math.max(0.2, rings),
				tnSpokes: clamp(Math.round(spokes), 0, 32),
				tnCenter: [center[0], center[1]],
				tnSway: Math.max(0, sway),
				tnFog: Math.max(0.1, fog),
			}}
		/>
	);
}
