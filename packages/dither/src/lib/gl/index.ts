export {
	type ClearBox,
	type ClearBoxOptions,
	clearBoxUniforms,
	MAX_CLEAR_BOXES,
	useClearBox,
} from "./clear-box";
export {
	BUDGET_MARGIN,
	ContextBudget,
	contextBudget,
	MAX_LIVE_CONTEXTS,
} from "./context-budget";
export { DitherShader, type DitherShaderProps } from "./dither-shader";
export {
	computeLayout,
	crispCellSizes,
	type GridLayout,
	parseCellValue,
	type SnappedCell,
	snapCell,
} from "./layout";
export {
	cssColorExpr,
	DEFAULT_PALETTE,
	levelsForPalette,
	limitPalette,
	packPalette,
	paletteToVec3Array,
	parseCssColor,
	type RGBA,
	resolvePaletteColors,
	useDitherColors,
	useResolvedPalette,
} from "./palette";
export type { TextureInput, TextureInputs } from "./renderer";
export { adaptFieldSources, GLSL_NOISE } from "./shader-source";
export {
	describeUniforms,
	type UniformRecord,
	type UniformValue,
	uniformDeclarations,
} from "./uniforms";
