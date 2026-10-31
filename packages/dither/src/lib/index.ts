/**
 * The dither engine's public surface. Everything the components are built
 * from is importable from here:
 *
 * - **Draw**: `DitherShader` (WebGL), `DitherPattern` / `ditherMaskStyle`
 *   (SVG and CSS), `drawDitherCanvas` / `rasterizeField` (canvas 2D) and
 *   `ditherImage` / `diffuseTones` (the CPU, once).
 * - **Colour**: named colours, `readDitherPalette`, `useDitherColors`.
 * - **Scale**: `snapCell`, `computeLayout`, `crispCellSizes`.
 * - **Motion**: `useFrameLoop`, `MOTION`, the context budget.
 * - **Export**: `toPNG`, `toSVG`, `toCSS`, `download`.
 *
 * Chart, bar and radial maths stay in their own modules.
 */

export {
	type DitherImageOptions,
	type DitherMethod,
	ditherImage,
} from "../components/images/algorithms";
export * from "./bayer";
export * from "./blue-noise";
export * from "./canvas";
export * from "./colors";
export * from "./diffuse";
export {
	type DitherBitmap,
	download,
	downloadBlob,
	downloadText,
	exportCSS,
	exportPNG,
	exportSVG,
	type IndexedBitmap,
	MAX_PNG_SIDE,
	type PixelBuffer,
	svgDataUrl,
	toCSS,
	toPNG,
	toSVG,
} from "./export";
export * from "./gl";
export {
	DEFAULT_FPS,
	MAX_FPS,
	MOTION,
	prefersReducedMotion,
	useDitherClock,
	useDocumentVisible,
	useFrameLoop,
	useInView,
	usePrefersReducedMotion,
} from "./motion";
export {
	DEFAULT_CELL,
	DitherDefs,
	type DitherFill,
	DitherPattern,
	DitherPatternDefs,
	type DitherPatternProps,
	type DitherRampOptions,
	ditherGradientStops,
	ditherMaskDataUri,
	ditherMaskStyle,
	ditherPatternSvg,
	ditherRamp,
	ditherRampBands,
	ditherRampMask,
	ditherRampStyle,
	type RampBand,
	type RampEdge,
	useDitherFillUrl,
	useDitherFillUrls,
} from "./pattern";
export { inkedAt, thresholdAt, thresholdSampler, toMatrix } from "./threshold";
export {
	type DitherPalette,
	type RGB,
	readDitherPalette,
	toRGB,
	useDitherPalette,
} from "./tokens";
