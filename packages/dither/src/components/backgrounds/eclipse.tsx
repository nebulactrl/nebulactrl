import { DitherShader } from "../../lib/gl";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	PAPER,
	shaderBase,
} from "./common";
import { BG2_COMMON } from "./glsl";

// A disc in the sky colour in front of a light in the rim colour. A thin rim
// hugs the edge and is brightest on the `light` side; a corona fades outwards
// with slow variations and breathes on a ten second cycle.
const FIELD = /* glsl */ `${BG2_COMMON}
float field(vec2 uv, float t) {
  float a = u_aspect;
  float cellH = 1.0 / u_res.y;
  vec2 c = vec2(ecCenter.x * a, 1.0 - ecCenter.y);
  vec2 p = vec2(uv.x * a, uv.y) - c;
  float d = length(p);
  float e = d - ecSize;
  if (e < 0.0) return 0.0;

  vec2 dir = p / max(d, 1e-4);
  float side = 0.5 + 0.5 * dot(dir, bg2Dir(ecLight));
  float lit = 0.18 + 0.82 * pow(side, 2.2);
  float ang = atan(p.y, p.x);

  float rim = exp(-e / (1.4 * cellH)) * lit;

  float reach = max(ecCorona, 0.02) * ecSize * 2.4;
  float vary = 0.72 + 0.28 * snoise(vec2(cos(ang), sin(ang)) * 1.6 + vec2(t * 0.05, t * 0.03));
  float breathe = 1.0 + ecPulse * 0.3 * sin(t * 0.62832);
  float corona = exp(-e / reach) * vary * breathe * (0.35 + 0.65 * lit) * 0.7;

  return clamp(max(rim, corona), 0.0, 1.0);
}
`;

export interface DitherEclipseProps extends BackgroundBaseProps {
	/** Centre of the disc as fractions of the box from the top left. Default [0.5, 0.5]. */
	center?: readonly [number, number] | undefined;
	/** Radius of the disc as a share of the box height. Default 0.24. */
	size?: number | undefined;
	/** How far the halo reaches past the rim, 0 to 1. Default 0.5. */
	corona?: number | undefined;
	/** Where the rim is brightest, in degrees as in CSS: 0 is the top. Default 315. */
	light?: number | undefined;
	/** How much the glow breathes, 0 (steady) to 1. Default 0.5. */
	pulse?: number | undefined;
	/** Colour of the rim and halo. Default `--dither-ink`. */
	rimColor?: string | undefined;
	/** Colour of the sky and the disc. Default `--dither-paper`. */
	skyColor?: string | undefined;
}

const CENTER: readonly [number, number] = [0.5, 0.5];

/** A dark disc with a glowing rim and a soft corona that breathes slowly. */
export function DitherEclipse({
	center = CENTER,
	size = 0.24,
	corona = 0.5,
	light = 315,
	pulse = 0.5,
	rimColor = INK,
	skyColor = PAPER,
	palette,
	...rest
}: DitherEclipseProps) {
	return (
		<DitherShader
			{...shaderBase(rest, { staticTime: 2 })}
			field={FIELD}
			palette={palette ?? [skyColor, rimColor]}
			uniforms={{
				ecCenter: [center[0], center[1]],
				ecSize: Math.max(0.02, size),
				ecCorona: clamp(corona, 0, 1),
				ecLight: light,
				ecPulse: clamp(pulse, 0, 1),
			}}
		/>
	);
}
