import { DitherShader } from "../../lib/gl";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	inkMix,
	PAPER,
	shaderBase,
} from "./common";

// A perspective floor under a striped sun. d is the screen distance below the
// horizon and s = horizon / d is the depth scale (1 at the bottom edge). Grid
// lines are measured in screen cells and fade where they would crowd closer
// than a few cells, so the horizon never breaks into moire. Squares keep
// their size against the box height: columns counts squares across the front
// of the floor in a 16:10 box, and narrower boxes show fewer.
const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  float a = u_aspect;
  float cellH = 1.0 / u_res.y;
  float h = rgHorizon;
  float d = h - uv.y;
  float tone = 0.0;

  if (d > 0.0) {
    float s = h / max(d, 1e-3);
    float cols = max(rgColumns, 2.0);
    float gx = (uv.x - 0.5) * a * cols / 1.6 * s;
    float fx = abs(fract(gx + 0.5) - 0.5);
    float pitchX = 1.6 / (cols * s) * u_res.y;
    float dxCells = fx * pitchX;

    float zc = h * cols / 0.72;
    float gz = s * zc + t * 0.6;
    float fzr = abs(fract(gz + 0.5) - 0.5);
    float pitchZ = h / (s * s * zc) * u_res.y;
    float dzCells = fzr * pitchZ;

    float lx = (1.0 - smoothstep(0.35, 1.0, dxCells)) * smoothstep(1.8, 4.5, pitchX);
    float lz = (1.0 - smoothstep(0.35, 1.0, dzCells)) * smoothstep(1.8, 4.5, pitchZ);
    float bright = clamp(1.5 / sqrt(s), 0.25, 1.0);
    float glow = exp(-d * 11.0) * 0.35;
    tone = clamp(max(lx, lz) * bright + glow, 0.0, 1.0);
  } else {
    float e = uv.y - h;
    tone = exp(-e * 7.0) * 0.5;
    if (rgSun > 0.001) {
      float r = rgSun * 0.5;
      vec2 q = vec2((uv.x - 0.5) * a, e);
      float dist = length(q);
      float edge = 1.2 * cellH;
      float disc = 1.0 - smoothstep(r - edge, r + edge, dist);
      float rel = clamp(e / r, 0.0, 1.0);
      // Slits cut the lower half, thicker towards the ground, drifting down.
      float hole = clamp((0.6 - rel) * 1.1, 0.0, 0.55);
      float phase = fract(rel * 7.0 + t * 0.15);
      float slit = hole > 0.001 ? smoothstep(hole, hole + 0.06, phase) : 1.0;
      float sun = disc * slit * (0.72 + 0.28 * rel);
      float halo = exp(-max(dist - r, 0.0) * 9.0) * 0.3;
      tone = max(tone, max(sun, halo));
    }
  }
  return tone;
}
`;

export interface DitherRetroGridProps extends BackgroundBaseProps {
	/** Height of the horizon, 0 (bottom) to 1 (top). Default 0.42. */
	horizon?: number | undefined;
	/** Size of the sun as a share of the box height. 0 hides it. Default 0.4. */
	sun?: number | undefined;
	/** Squares across the front of the floor in a 16:10 box. Narrower boxes show fewer. Default 12. */
	columns?: number | undefined;
}

const DEFAULT_PALETTE = [PAPER, inkMix(28), inkMix(62), INK];

/** A perspective grid floor scrolling towards a horizon under a striped, glowing sun. */
export function DitherRetroGrid({
	horizon = 0.42,
	sun = 0.4,
	columns = 12,
	palette = DEFAULT_PALETTE,
	...rest
}: DitherRetroGridProps) {
	return (
		<DitherShader
			{...shaderBase(rest, { staticTime: 3 })}
			field={FIELD}
			palette={palette}
			uniforms={{
				rgHorizon: clamp(horizon, 0.05, 0.95),
				rgSun: clamp(sun, 0, 1.5),
				rgColumns: Math.max(2, columns),
			}}
		/>
	);
}
