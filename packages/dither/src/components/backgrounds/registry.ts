import type { ReactNode } from "react";
import type { DitherMatrix } from "../../lib/bayer";

/** One control in the gallery's knob panel. */
export type KnobDef =
	| {
			type: "range";
			key: string;
			label: string;
			min: number;
			max: number;
			step: number;
			value: number;
	  }
	| {
			type: "select";
			key: string;
			label: string;
			options: readonly string[];
			value: string;
	  }
	| { type: "toggle"; key: string; label: string; value: boolean };

/** Current knob values, keyed by `KnobDef.key`. */
export type KnobValues = Readonly<Record<string, number | string | boolean>>;

/** One background in the gallery: its knobs and how to render it from their values. */
export interface BackgroundEntry {
	id: string;
	name: string;
	knobs: readonly KnobDef[];
	/** Returns a background that fills its (positioned) parent. */
	render: (values: KnobValues) => ReactNode;
}

/** Typed readers for knob values, with the knob's default as the fallback. */
export function num(values: KnobValues, key: string, fallback: number): number {
	const value = values[key];
	return typeof value === "number" ? value : fallback;
}

export function str<T extends string>(
	values: KnobValues,
	key: string,
	fallback: T,
): T {
	const value = values[key];
	return typeof value === "string" ? (value as T) : fallback;
}

export function bool(
	values: KnobValues,
	key: string,
	fallback: boolean,
): boolean {
	const value = values[key];
	return typeof value === "boolean" ? value : fallback;
}

/** The matrix knob's options: "default" keeps the background's own choice. */
export const MATRIX_OPTIONS = [
	"default",
	"bayer2",
	"bayer4",
	"bayer8",
	"blue-noise",
] as const;

/** The matrix chosen by the gallery's matrix knob, or `undefined` for the background's default. */
export function matrixOf(values: KnobValues): DitherMatrix | undefined {
	const value = values.matrix;
	return value === "bayer2" ||
		value === "bayer4" ||
		value === "bayer8" ||
		value === "blue-noise"
		? value
		: undefined;
}
