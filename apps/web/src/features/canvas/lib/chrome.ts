/**
 * How the canvas's floating chrome (toolbar, search field, selection dock)
 * fits the part of the canvas the inspector leaves free. The
 * chrome lives in the free area only, so a wide inspector must not push any of
 * it under the sheet. Pure, so the thresholds are tested as plain values.
 */

/**
 * Under this free width the toolbar shows icons only, and the selection dock
 * does too: the labelled toolbar (Search, Arrange, Group) plus Create needs
 * about 440px, and the dock about 290px next to the zoom stack.
 */
export const COMPACT_BELOW = 520;

/**
 * From this free width on the search field floats at the top centre, between
 * the toolbar and Create. Below it, the field would cover one of them, so it
 * takes its own row under the toolbar.
 */
export const SEARCH_FLOAT_FROM = 880;

export interface ChromeMode {
	/** Toolbar and dock show icons only. */
	compact: boolean;
	/** The search field sits in the chrome column instead of floating. */
	searchInline: boolean;
}

/** The part of the canvas the inspector leaves free, in px. */
export function freeWidth(canvasWidth: number, inspectorPx: number): number {
	return Math.max(0, canvasWidth - inspectorPx);
}

/** The chrome layout for a free width. */
export function chromeMode(free: number): ChromeMode {
	return {
		compact: free < COMPACT_BELOW,
		searchInline: free < SEARCH_FLOAT_FROM,
	};
}

/**
 * The chrome layout for a canvas `canvasWidth` wide with an inspector
 * `inspectorPx` wide over its right edge. A canvas not measured yet (width 0)
 * gets the roomy layout, so the first frame does not flash icons.
 */
export function chromeModeFor(
	canvasWidth: number,
	inspectorPx: number,
): ChromeMode {
	if (canvasWidth <= 0) return chromeMode(Number.POSITIVE_INFINITY);
	return chromeMode(freeWidth(canvasWidth, inspectorPx));
}
