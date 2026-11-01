import { DitherShader } from "../../lib/gl";
import { prefersReducedMotion } from "../../lib/motion";
import {
	type BackgroundBaseProps,
	clamp,
	INK,
	PAPER,
	shaderBase,
} from "./common";

// A sphere shaded by one light, with a soft terminator, a thin rim light on
// the shadow side and a dithered glow around it. The outline breathes with
// two slow harmonics plus noise (morph). u_pointer (eased by the engine) moves
// the orb and its light: lean is the share of the way they follow it.
//
// Tone layout: with a highlight, body shading uses 0..0.5 of the ramp and the
// specular spot reaches 1; without one the body uses the whole ramp.
const FIELD = /* glsl */ `
float field(vec2 uv, float t) {
  float a = u_aspect;
  vec2 lean = (u_pointer - 0.5) * obLean;
  vec2 c = vec2(0.5 * a, 0.5) + vec2(lean.x * a, lean.y) * 0.22;
  vec2 p = vec2(uv.x * a, uv.y) - c;
  float d = length(p);
  float ang = atan(p.y, p.x);

  float R = obSize * min(a, 1.0);
  R *= 1.0 + 0.035 * sin(t * 0.9) + 0.02 * sin(t * 1.7 + 1.3);
  R *= 1.0 + obMorph * (
    0.025 * sin(2.0 * ang + t * 0.6)
    + 0.015 * sin(3.0 * ang - t * 0.8)
    + 0.02 * snoise(vec2(cos(ang), sin(ang)) * 1.4 + vec2(t * 0.15, 0.0))
  );

  vec3 L = normalize(vec3(-0.5 + lean.x * 1.4, 0.55 + lean.y * 1.4, 0.75));
  float e = (d - R) / R;

  if (e > 0.0) {
    float toLight = 0.5 + 0.5 * dot(p / max(d, 1e-4), normalize(L.xy));
    float g = obGlow * exp(-e * 3.2) * (0.3 + 0.7 * toLight);
    return g * obTop * 0.7;
  }

  vec2 q = p / R;
  float z = sqrt(max(1.0 - dot(q, q), 0.0));
  vec3 nrm = vec3(q, z);
  float diff = dot(nrm, L);
  float lit = smoothstep(-0.35, 0.85, diff);
  float rim = pow(1.0 - z, 3.0) * (1.0 - smoothstep(-0.2, 0.3, diff)) * 0.3;
  float body = clamp(0.08 + 0.62 * lit + rim, 0.0, 1.0);
  vec3 refl = reflect(-L, nrm);
  float spec = pow(max(refl.z, 0.0), 26.0);
  float tone = body * obTop;
  if (obSpec > 0.5) {
    tone = max(tone, spec * 1.0);
  } else {
    tone = clamp(tone + spec * 0.35, 0.0, 1.0);
  }
  return tone;
}
`;

export interface DitherOrbProps extends BackgroundBaseProps {
	/** Colour of the orb and its glow. Default `--dither-ink`. */
	color?: string | undefined;
	/** A third, brighter tone for the specular highlight. Pick a colour that is light in every theme. */
	highlight?: string | undefined;
	/** Colour behind the orb. "transparent" floats it over the page. Default `--dither-paper`. */
	background?: string | undefined;
	/** Radius as a share of the box's shorter side, 0.1 to 0.5. Default 0.26. */
	size?: number | undefined;
	/** How much the outline wobbles. 0 is a perfect sphere. Default 1. */
	morph?: number | undefined;
	/** How far the orb and its light lean towards the pointer. 0 turns it off. Default 1. */
	lean?: number | undefined;
	/** Strength of the dithered glow around the orb, 0 to 1. Default 0.5. */
	glow?: number | undefined;
}

/** A lit, dithered orb that breathes slowly and leans towards the pointer. */
export function DitherOrb({
	color = INK,
	highlight,
	background = PAPER,
	size = 0.26,
	morph = 1,
	lean = 1,
	glow = 0.5,
	palette,
	animate = true,
	...rest
}: DitherOrbProps) {
	const leaning = lean > 0 && animate && !prefersReducedMotion();
	return (
		<DitherShader
			{...shaderBase({ ...rest, animate }, { staticTime: 2 })}
			field={FIELD}
			palette={
				palette ??
				(highlight ? [background, color, highlight] : [background, color])
			}
			pointer={leaning}
			uniforms={{
				obSize: clamp(size, 0.1, 0.5),
				obMorph: Math.max(0, morph),
				obLean: leaning ? clamp(lean, 0, 2) : 0,
				obGlow: clamp(glow, 0, 1),
				obTop: highlight ? 0.5 : 1,
				obSpec: highlight ? 1 : 0,
			}}
		/>
	);
}
