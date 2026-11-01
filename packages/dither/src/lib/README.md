# Dither kit

NebulaCtrl's own ordered-dither engine and component library. It exists to give the product a single, coherent texture language that matches the halftone "Stipple" ambient: data fills, bars and a few ambient pieces drawn from cells instead of gradients.

- Engine: `packages/dither/src/lib/**` (this folder), imported as `@nebulactrl/dither/lib` and `@nebulactrl/dither/lib/<file>`
- Components: `packages/dither/src/components/{charts,backgrounds,interface,text,images}/**`, imported as `@nebulactrl/dither/<group>` (the group barrel) or `@nebulactrl/dither/<group>/<file>`
- Every component lives in the package, including the nine that wrap a UI primitive (`charts/stat-card`, `interface/{avatar,button,card,empty-state,not-found}`, `text/dither-number`, `images/{dithered-logo,logo-marquee}`). The interface pieces sit on `components/interface/primitives.tsx` (Base UI plus the design vocabulary `btn-*`, `pressable`, `surface-*`, `rounded-card`, `text-ink-*`, which both apps define), so they draw the same in `apps/web` and `apps/site`. Brand logos for `DitheredLogo`/`LogoMarquee` come from the host through `setBrandCatalog` (`images/logo-source`); `apps/web` registers its sprite in `src/lib/dither-brands.ts`.
- Live catalogue (dev only, in `apps/web`): `/dev/dither`. The galleries and demos (`apps/web/src/components/dither/**`) are the only dither code in the app; they import the package.
- Tokens: `--dither-*` in `packages/tokens/src/dither.css` (`@nebulactrl/tokens`), imported by both apps' stylesheets together with the motion scale in `motion.css`; the package's Tailwind classes need `@source "…/packages/dither/src"` in the app's stylesheet

## What it is

A cell is inked when the tone at that cell is above its threshold in a repeating matrix. Because every matrix is _ordered_, the same tone always gives the same dots and a denser tone is a strict superset of a sparser one (`tile(k) ⊂ tile(k+1)`). Two things in the kit lean on that:

1. **Banded gradients** ("densest at the line"). An area's fill is a stack of strips parallel to its curve, each filled with a pattern at a lower tone than the one above. The union of nested tiles is the densest one, so strips never need to be cut out of each other.
2. **Right-anchored layers** on bars. A progress fill is a few masked layers, all anchored at the leading edge, so the bar thins out behind its solid cap with no phase jumps.

Files:

| File                            | What                                                                                                   |
| ------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `bayer.ts`                      | Bayer 2/4/8 thresholds, `ditherOn`, `quantize`                                                         |
| `pattern.ts`                    | SVG `<pattern>` markup, CSS mask data URIs, a page-wide pattern registry (`useDitherFillUrl`)          |
| `tokens.ts`                     | `readDitherPalette`, `toRGB` (resolves oklch / color-mix through a canvas probe), `useDitherPalette`   |
| `chart-math.ts`                 | Scales, nice ticks, monotone curve sampling, band polygons, stacking, arcs                             |
| `progress-math.ts`              | Bar state, stacked parts, step blocks, gradient and comet layers                                       |
| `recharts-math.ts`              | Adapter for Recharts' area points and baselines                                                        |
| `prefs.ts`                      | `useForcedColors`, `useReducedMotionPref`, `useElementWidth`, `useOnScreen`                            |
| `gl/`, `motion.ts`              | The shared WebGL2 renderer, context budget (`MAX_LIVE_CONTEXTS`) and clock behind animated backgrounds |
| `colors.ts`                     | Named colours: `DITHER_COLOR_NAMES`, `ditherColorVar`                                                  |
| `blue-noise.ts`, `threshold.ts` | Void-and-cluster blue-noise tiles; one threshold lookup for every matrix                               |
| `diffuse.ts`                    | Error diffusion (Floyd–Steinberg, Atkinson, Sierra Lite) on any tone grid                              |
| `canvas.ts`                     | The canvas-2D draw path: `rasterizeField`, `drawDitherCanvas`                                          |
| `export.ts`                     | `toPNG`, `toSVG`, `toCSS`, `download` (and the lower-level `export*` functions)                        |
| `index.ts`                      | The barrel: `import { DitherShader, ditherMaskStyle, ditherImage } from "@nebulactrl/dither/lib"`      |
| `WRITE-YOUR-OWN.md`             | How to add a field                                                                                     |

Everything pure has a `*.test.ts` next to it.

## How to use it

- **Chart on our own SVG:** `DitherAreaChart`, `DitherBarChart`, `DitherLineChart`, … from `@nebulactrl/dither/charts`. Typed `data` / `x` / `series` accessors, a required `label`, a hidden table for screen readers.
- **Inside Recharts:** `<Area shape={<DitherAreaShape color={c} />} />`. Recharts keeps the scales, tooltip, cursor and sync; the shape only replaces the paint.
- **Bars and meters:** `DitherProgressBar`, `DitherMeter`, `DitherCapacityStat`.
- **A block with a texture:** `ditherMaskStyle({ tone, cell })` plus a background colour, with `data-dither-fill` so forced colours turn it solid.
- **A fade or gradient in one box:** `ditherRampStyle({ edge, length, from, to, cell })` (or `toCSS({ kind: "ramp", … })`) is one mask image whose threshold changes along the axis; no stacked bands. A numeric `length` snaps every band boundary to a whole cell row.

Rules the components follow: text, axes and tooltips are never dithered; the line or cap that carries the value is solid; `forced-colors` and cells under 1.5 px fall back to flat colour; motion is transform/opacity only and off under `prefers-reduced-motion`; patterns are registered once per page, so a refetch changes path data only.

## What we adopted from dithercn, and what we did not

[dithercn](https://dithercn.dev) (MIT, `opencoredev/dithercn`, copyright Leo and Shadcn Labs) is the reference for the _catalogue_ and for several ideas. We read its docs and source for technique, installed nothing (no package, no `shadcn add`, no registry item) and wrote our own code.

Adopted (ideas):

- The vocabulary and the example list: area, bar, line, sparkline, heatmap, donut, radar, gauge, progress bar, uptime bar, stat card; the background, interface, text and image examples.
- "Tone → threshold matrix → cell" as the model; Bayer 2/4/8; quantised levels.
- Static fills as SVG `<pattern>` and CSS mask, with a gradient built from tone bands; a solid cap at the end of every bar.
- Colour from the theme through CSS variables; a texture as well as a colour wherever series must be told apart (uptime bar, donut).
- One shared WebGL renderer for animated surfaces, paused off-screen and under reduced motion.

Not adopted, and why:

- **The package, the registry and `lib/dither` source.** The engine is our own. Our API (`bayer.ts`, `pattern.ts`, `tokens.ts`) is different: patterns come from a per-page registry, tokens are our `--dither-*` and `--color-*` variables, and charts take our accessor shape.
- **Their blue-noise table.** We generate our own tile (void-and-cluster, `blue-noise.ts`): 64×64 for shaders and images, a compact 16×16 for SVG and CSS fills. No table is shipped.
- **Their colour variable names.** The named colours (`blue`, `purple`, `green`, `pink`, `orange`, `red`, `grey`) exist as `--dither-blue` … and map onto our own status and accent tokens; `--dither-series-1…8` stay for charts.
- **shadcn/Tailwind theme coupling** (`--primary`, `--background`). Ours map onto the neo-skeuomorphic surface ladder.
- **Entrance animations on by default.** `animate` exists on every chart (a one-off reveal when first seen, never on a refetch) but is off unless asked: our live charts refetch every few seconds and redrawing from zero would read as flicker.

## Attribution

Where a file adapts a non-trivial snippet from dithercn it carries the notice below in its header. Generic maths (Bayer rank bit interleaving, Fritsch–Carlson monotone interpolation, Hermite evaluation, polar arcs) is not copyrightable and is written from the definitions.

```
dithercn
Copyright (c) 2026 Leo
Copyright (c) 2026 Shadcn Labs (startercn, which this project is based on)
Licensed under the MIT License: https://github.com/opencoredev/dithercn/blob/main/LICENSE
Portions adapted from https://github.com/opencoredev/dithercn
```

Two files adapt small pieces of dithercn (the image preset recipes in `components/images/look.ts` and the colour weights in `images/algorithms.ts`) and carry the notice above; the full MIT text is in the repository `NOTICE`. Everything else is written from the definitions.

## Gotchas

- `DitherDefs` is optional: `useDitherFillUrl` mounts a hidden zero-size `<svg>` on `<body>` itself. It is deliberately not `display:none`; browsers skip patterns inside a non-rendered tree.
- Pattern ids hash the _quantised_ tone (inked cell count), colour, cell, matrix and shape. Two fills that look the same share an id.
- `useDitherFillUrls(fills)` keys its memo on the fills' ids, so passing a fresh array each render is fine as long as the values are equal.
