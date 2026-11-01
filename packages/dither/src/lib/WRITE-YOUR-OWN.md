# Write your own dither

A new background is one GLSL function, the **field**, handed to `DitherShader`. The engine does the rest: sizing the grid to whole device pixels, quantising the tone, the threshold matrix, the palette, motion, pausing off-screen, reduced motion, the WebGL context budget and the fallback.

## 1. Return a tone

The field runs once per cell and returns a tone from 0 (paper) to 1 (ink).

```tsx
import { DitherShader } from "@nebulactrl/dither/lib/gl";

const FIELD = /* glsl */ `
float field(vec2 uv, vec2 res, float t) {
  return uv.y;               // bottom paper, top ink
}
`;

export const Rise = () => (
	<DitherShader
		field={FIELD}
		palette={["paper", "blue"]}
		className="absolute inset-0"
	/>
);
```

- `uv` is the cell centre, 0..1 on both axes, **y up**.
- `res` is the grid size in cells (`res.x / res.y` is the aspect).
- `t` is seconds. It stops when the shader is off-screen and holds `stillTime` under reduced motion.

Both signatures work: `float field(vec2 uv, float t)` (with the `u_aspect` uniform for the aspect) and the three-argument form above. A `colorField` may be `vec3 colorField(vec2 uv, float t, float tone)` or `vec3|vec4 colorField(vec2 uv, vec2 res, float t)`.

## 2. Draw a shape

`uv` stretches with the box, so correct for the aspect before measuring distances:

```glsl
float field(vec2 uv, vec2 res, float t) {
  vec2 p = vec2(uv.x * res.x / res.y, uv.y);
  vec2 drop = vec2(0.5 * res.x / res.y, 0.45);
  float d = length(p - drop);
  return (0.5 + 0.5 * cos(d * 48.0)) * exp(-d * 3.0);   // rings that fade
}
```

Return smooth values and let the matrix dither them. Every tone between 0 and 1 is a different dot density.

## 3. Add motion and props

Each key of `uniforms` is declared for you: a number is a `float`, a tuple of 2, 3 or 4 numbers a `vec2`, `vec3` or `vec4`. Changing a value updates the uniform; changing the GLSL recompiles. Prefix names (`ripple…`) so fields never clash, and never start one with `u_` (reserved).

```tsx
import {
	DitherShader,
	type DitherShaderProps,
} from "@nebulactrl/dither/lib/gl";

const FIELD = /* glsl */ `
float field(vec2 uv, vec2 res, float t) {
  vec2 p = vec2(uv.x * res.x / res.y, uv.y);
  vec2 drop = vec2(rippleDrop.x * res.x / res.y, 1.0 - rippleDrop.y);
  float wobble = fbm(p * 2.2 + vec2(t * 0.03, 0.0)) - 0.5;
  float d = length(p - drop) + wobble * 0.08;
  float rings = 0.5 + 0.5 * cos(d * rippleRings - t * 0.9);
  return rippleStrength * rings * exp(-d * 2.6);
}
`;

export type RipplesProps = Omit<DitherShaderProps, "field" | "uniforms"> & {
	drop?: readonly [number, number];
	rings?: number;
	strength?: number;
};

export function Ripples({
	drop = [0.5, 0.42],
	rings = 46,
	strength = 0.85,
	...rest
}: RipplesProps) {
	return (
		<DitherShader
			{...rest}
			field={FIELD}
			stillTime={4}
			uniforms={{
				rippleDrop: drop,
				rippleRings: rings,
				rippleStrength: strength,
			}}
		/>
	);
}
```

`stillTime={4}` is the moment drawn for reduced motion (`staticTime` is the same prop). Pick one that looks finished. The rest of `DitherShaderProps` passes through: `palette`, `matrix` (`bayer2`, `bayer4`, `bayer8`, `blue-noise`), `levels`, `cell`, `shape`, `speed`, `fps`, `animate`, `pointer`, `pointerRest`, `fallback`.

### Levels

`levels` is how many tone steps the field is snapped to before the threshold is applied. A number is used as given (default 6). `"full"` uses every tone the matrix can draw (5 for `bayer2`, 17 for `bayer4`, 65 for `bayer8`, 256 for `blue-noise`): the smoothest ramps, for soft fields. `"stops"` picks the fewest steps, at or above 6, where `levels - 1` is a multiple of `colours - 1`, so every palette stop is hit exactly and no stray neighbour-colour dots appear between stops. A 2-colour palette gives 6, 3 colours 7, 5 colours 9; `bayer2` draws only 5 tones, so it is capped there. The rule is `levelsForPalette(colours, matrix)` in `gl/palette.ts`; use it directly when you pass a number.

### Pointer

`pointer` exposes `u_pointer` (0..1, y up), eased towards the pointer. When the pointer leaves the page, the window loses focus or a touch ends, the target returns to `pointerRest` (default `[0.5, 0.5]`), and the easing stops once it has arrived. Set `pointerRest` to where the field looks finished with nobody there.

## Helpers every field gets

From `GLSL_NOISE` (`gl/shader-source.ts`), prepended automatically:

| Helper                                   |                                                                    |
| ---------------------------------------- | ------------------------------------------------------------------ |
| `hash11`, `hash21`, `hash22`             | stable per-cell randomness in [0, 1)                               |
| `vnoise`, `snoise`, `fbm`, `fbmN`        | value noise, simplex noise in [-1, 1], fractal noise in [0, 1]     |
| `rot2`                                   | 2×2 rotation                                                       |
| `sdCircle`, `sdBox`, `sdSegment`, `smin` | signed distances and a smooth minimum                              |
| `spectrum(h)`                            | a saturated rainbow, wraps at 1                                    |
| `paletteColor(x)`                        | the palette's ink colours (every entry after paper) as a ramp 0..1 |

Engine uniforms you may read: `u_res` (grid in cells), `u_aspect`, `u_time`, `u_pointer` (with `pointer`), `u_cell`, `u_palN`, `u_pal[]`.

## Many hues

The field says _where_ the dots go; an optional `colorField` says _which colour_ they are, independent of density:

```glsl
vec3 colorField(vec2 uv, vec2 res, float t) {
  return mix(spectrum(uv.x * 0.75 - t * 0.03), paletteColor(0.0).rgb, 0.2);
}
```

Mixing in a fifth of the theme ink keeps pale hues readable.

## Colour and theme

Palette entries are token names (`"--chart-1"`), named colours (`"blue"`, `"purple"`, `"green"`, `"pink"`, `"orange"`, `"red"`, `"grey"`, plus `"ink"` and `"paper"`) or any CSS colour (`oklch(...)`, `transparent`). They are resolved in the context of the shader's own box, so a wrapper that overrides `--dither-ink` retints it, and they re-read when `<html>`'s class or style changes.

## The same field without WebGL

For exports, thumbnails and workers, write the field's JavaScript twin and draw it with the canvas path. It uses the same thresholds, level snapping and device-pixel cells, so a still frame matches:

```ts
import { drawDitherCanvas } from "@nebulactrl/dither/lib/canvas";

const tone = (u: number, v: number) =>
	Math.max(0, 1 - Math.hypot(u - 0.5, v - 0.5) * 2);
drawDitherCanvas(canvas.getContext("2d")!, tone, {
	cell: 3,
	method: "bayer4",
	palette: ["paper", "blue"],
});
```

Then `toPNG(canvas)`, `toSVG(canvasToBitmap(canvas))` or `toCSS(...)` from `@nebulactrl/dither/lib/export`.

## Ship it

1. Put the component in `packages/dither/src/components/<group>/<name>.tsx` with its GLSL beside it.
2. Export it from the group's `index.ts`.
3. Add an entry to the group's gallery registry (`backgrounds/registry-*.tsx` for backgrounds) with a few knobs; it appears in `/dev/dither`.
4. Pure maths (anything you can compute without WebGL) gets a `*.test.ts` next to it.
5. Check it with reduced motion on, in a hidden tab, scrolled off-screen, and with WebGL2 unavailable (the `fallback` shows).
