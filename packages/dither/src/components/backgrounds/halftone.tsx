import { DitherShader } from "../../lib/gl";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	PAPER,
	shaderBase,
} from "./common";

// A print halftone. Each ink has its own screen, rotated 30 degrees from the
// last. Every square of a screen holds one round dot, sized by the source at
// the square's centre. Coverage is anti-aliased over one CSS pixel so the
// dither only breaks up the dot edge. With several inks, field() returns the
// union coverage and colorField() averages the inks that cover each cell, so
// overlaps mix like a print.
const FIELD = /* glsl */ `
float htSource(vec2 cuv, vec2 cpx, float t, float k) {
  float s;
  if (htSrc < 0.5) {
    float n = fbm(cpx / (220.0 * max(htScale, 0.05)) + vec2(t * 0.03 + k * 13.7, -t * 0.02 + k * 5.3));
    s = smoothstep(0.3, 0.78, n);
  } else if (htSrc < 1.5) {
    vec2 d = (cuv - htCenter) * vec2(u_aspect, 1.0);
    float reach = length(vec2(max(htCenter.x, 1.0 - htCenter.x) * u_aspect, max(htCenter.y, 1.0 - htCenter.y)));
    s = 1.0 - clamp(length(d) / max(htRadius * reach, 1e-3), 0.0, 1.0);
    s = s * s * (3.0 - 2.0 * s);
  } else {
    vec2 dir = vec2(sin(htDir), cos(htDir));
    vec2 q = (cuv - 0.5) * vec2(u_aspect, 1.0);
    float reach = 0.5 * (abs(dir.x) * u_aspect + abs(dir.y));
    s = clamp(dot(q, dir) / max(reach, 1e-3) * 0.5 + 0.5, 0.0, 1.0);
  }
  return s;
}
vec3 htCover(vec2 uv, float t) {
  vec2 res = u_res * u_cell;
  vec2 px = uv * res;
  vec3 cov = vec3(0.0);
  for (int k = 0; k < 3; k++) {
    if (float(k) >= htInks) break;
    float ang = radians(htAngle + 30.0 * float(k));
    float c = cos(ang);
    float sn = sin(ang);
    mat2 R = mat2(c, -sn, sn, c);
    vec2 g = R * px;
    float shift = t * htDrift * htSize * 0.25;
    g.x += shift;
    vec2 id = floor(g / htSize);
    vec2 cg = (id + 0.5) * htSize;
    // Back to screen space: rotation matrices invert by transpose.
    vec2 cpx = (cg - vec2(shift, 0.0)) * R;
    float s = htSource(cpx / res, cpx, t, float(k));
    float rad = 0.5 * htSize * htIntensity * sqrt(clamp(s, 0.0, 1.0));
    float dist = length(g - cg);
    float cover = 1.0 - smoothstep(rad - 0.5, rad + 0.5, dist);
    cov[k] = cover * step(0.02, s);
  }
  return cov;
}
float field(vec2 uv, float t) {
  vec3 c = htCover(uv, t);
  return max(c.x, max(c.y, c.z));
}
`;

const COLOR_FIELD = /* glsl */ `
vec3 colorField(vec2 uv, float t, float tone) {
  vec3 c = htCover(uv, t);
  vec3 sum = vec3(0.0);
  float w = 0.0;
  for (int k = 0; k < 3; k++) {
    if (float(k) >= htInks) break;
    float pos = htInks > 1.5 ? float(k) / (htInks - 1.0) : 1.0;
    float wk = step(0.5, c[k]) + c[k] * 0.01;
    sum += paletteColor(pos).rgb * wk;
    w += wk;
  }
  return sum / max(w, 1e-3);
}
`;

export interface DitherHalftoneProps
	extends Omit<BackgroundBaseProps, "palette"> {
	/** Colour of the dots: a `--token` name or any CSS colour. Default `--dither-ink`. */
	color?: string | undefined;
	/** Two or three inks on separate screens, 30° apart. Overlaps mix. Use instead of `color`. */
	channels?: readonly [string, string, string?] | undefined;
	/** Colour behind the dots. "transparent" lays the screen over what is underneath. Default `--dither-paper`. */
	background?: string | undefined;
	/** What sizes the dots: soft drifting clouds, distance from a point, or a ramp. Default "noise". */
	source?: "noise" | "radial" | "linear" | undefined;
	/** Radial only. Where the dots are largest, as fractions of the box from the top left. Default [0.5, 0.5]. */
	center?: readonly [number, number] | undefined;
	/** Radial only. Where the dots run out. 1 reaches the farthest corner. Default 1. */
	radius?: number | undefined;
	/** Linear only. Direction the dots grow towards, in degrees as in CSS. Default 0. */
	direction?: number | undefined;
	/** Distance between dot centres in CSS pixels. Default 10. */
	size?: number | undefined;
	/** Screen angle in degrees. Each extra channel adds 30°. Default 45. */
	angle?: number | undefined;
	/** Size of the largest dot, 0 to 1. At 1 the biggest dots just touch. Default 0.9. */
	intensity?: number | undefined;
	/** Noise only. Size of the clouds. Default 1. */
	scale?: number | undefined;
	/** How fast the screen slides along its angle. 0 holds it still. Default 1. */
	drift?: number | undefined;
}

const CENTER: readonly [number, number] = [0.5, 0.5];
const SOURCES = { noise: 0, radial: 1, linear: 2 } as const;

/** A print halftone: round dots on a rotated screen, in one ink or up to three. */
export function DitherHalftone({
	color = INK,
	channels,
	background = PAPER,
	source = "noise",
	center = CENTER,
	radius = 1,
	direction = 0,
	size = 10,
	angle = 45,
	intensity = 0.9,
	scale = 1,
	drift = 1,
	// One rendered pixel per CSS pixel keeps the dot edges smooth.
	cell = 1,
	...rest
}: DitherHalftoneProps) {
	const inks = channels
		? channels.filter((c): c is string => Boolean(c))
		: [color];
	return (
		<DitherShader
			{...shaderBase({ ...rest, cell }, { staticTime: 5 })}
			field={FIELD}
			colorField={inks.length > 1 ? COLOR_FIELD : undefined}
			palette={[background, ...inks]}
			uniforms={{
				htInks: inks.length,
				htSrc: SOURCES[source],
				htCenter: [center[0], 1 - center[1]],
				htRadius: Math.max(0.05, radius),
				htDir: (direction * Math.PI) / 180,
				htSize: Math.max(2, size),
				htAngle: angle,
				htIntensity: clamp(intensity, 0, 1),
				htScale: Math.max(0.05, scale),
				htDrift: drift,
			}}
		/>
	);
}
