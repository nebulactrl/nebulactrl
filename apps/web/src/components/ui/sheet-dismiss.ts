export interface PressTarget {
	readonly isConnected: boolean;
	closest(selector: string): unknown;
}

export interface OutsidePress {
	/** Null when the press landed on something that is not an element. */
	target: PressTarget | null;
	/** Mouse button; undefined for touch. Only the primary button dismisses. */
	button?: number | undefined;
	/** Fingers down (including one that just lifted); undefined for mouse. */
	touchCount?: number | undefined;
}

/**
 * Every other layer the page can stack above a sheet: dialogs and their
 * scrims, menus, selects, popovers, tooltips, other sheets and the toast
 * stack. A press inside one belongs to that layer, whoever opened it — a
 * nested dialog being open means an outside press closes only that dialog.
 * A modal sheet's own scrim is deliberately absent: pressing it is how the
 * sheet is dismissed.
 */
export const OTHER_LAYER_SELECTOR = [
	'[role="dialog"]',
	'[role="alertdialog"]',
	'[role="menu"]',
	'[role="listbox"]',
	'[role="tooltip"]',
	"[data-toast]",
	'[data-slot="sheet-content"]',
	'[data-slot="dialog-content"]',
	'[data-slot="dialog-overlay"]',
	'[data-slot="alert-dialog-content"]',
	'[data-slot="alert-dialog-overlay"]',
	'[data-slot="popover-content"]',
	'[data-slot="select-content"]',
	'[data-slot="combobox-content"]',
	'[data-slot="tooltip-content"]',
	'[data-slot="dropdown-menu-content"]',
	'[data-slot="dropdown-menu-sub-content"]',
	'[data-slot="context-menu-content"]',
	'[data-slot="context-menu-sub-content"]',
].join(",");

/**
 * A layer other than a sheet or a toast that is open — or was, a frame ago
 * — right now. Escape belongs to it, not to the sheet behind it. Base UI
 * only knows that for layers nested in the sheet's own React tree, and a
 * dialog the page renders beside the sheet is not; a layer that Escape
 * has just begun closing still counts, because React can flush its close
 * before the sheet's own listener of the same key press runs.
 */
export const OPEN_OTHER_LAYER_SELECTOR = `:is(${OTHER_LAYER_SELECTOR}):not([data-slot="sheet-content"]:not([data-layer="over"]),[data-toast]):is([data-open],[data-ending-style])`;

/**
 * A sheet marked `data-layer="over"` is stacked above another sheet (the
 * deployment debug panel above the inspector). It counts as an open layer for
 * everything below it — Escape belongs to it, and the inspector's tab chords
 * stay quiet — but not for itself: it asks only about layers above it.
 */
export const OPEN_LAYER_ABOVE_OVER_SHEET_SELECTOR = `${OPEN_OTHER_LAYER_SELECTOR}:not([data-layer="over"])`;

/**
 * `retarget` selects the elements that switch the sheet to something else
 * (a cluster row, a template card). Their own click handler already points
 * the sheet at the new subject, so closing it first only makes it flicker.
 */
export function shouldDismissOnOutsidePress(
	press: OutsidePress,
	retarget?: string,
): boolean {
	if (press.button !== undefined && press.button !== 0) return false;
	if (press.touchCount !== undefined && press.touchCount !== 1) return false;

	const { target } = press;
	if (!target) return false;
	// A control that unmounts on the press (a picked select option) no
	// longer says which layer it belonged to; guessing "outside" would
	// close the sheet under a choice the reader was making inside it.
	if (!target.isConnected) return false;
	if (target.closest(OTHER_LAYER_SELECTOR)) return false;
	if (retarget && target.closest(retarget)) return false;
	return true;
}
