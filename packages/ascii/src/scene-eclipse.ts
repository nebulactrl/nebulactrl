/**
 * The `eclipse` scene alone: the dithered planet. A bundle that imports this
 * ships the planet shader and none of the glyph scenes' (`engine.ts` ships all
 * of them).
 */
export type { AsciiEngine, AsciiOptions } from "./core";
export { createPlanet as createEclipse } from "./planet";
