/**
 * Overrides for a fixture builder. A field set to `undefined` is left out of the
 * result, which is how a test removes an optional field the builder fills in
 * (the API omits it rather than sending null).
 */
export type Overrides<T> = { [K in keyof T]?: T[K] | undefined };

/** `base` with `over` applied; fields `over` sets to undefined are removed. */
export function withOverrides<T extends object>(
	base: T,
	over: Overrides<T>,
): T {
	const out: Record<string, unknown> = Object.fromEntries(Object.entries(base));
	for (const [key, value] of Object.entries(over)) {
		if (value === undefined) delete out[key];
		else out[key] = value;
	}
	// Removing an optional field keeps the shape T; the builders only remove optional ones.
	return out as T;
}
