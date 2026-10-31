/**
 * When an InfoTip is open. Hover and keyboard focus show it for as long as
 * they last; a click or tap pins it open until the next click, Escape or a
 * press elsewhere, so a touch screen (which has no hover) can read it and a
 * mouse user can move on to select the text.
 */
export interface InfoTipState {
	open: boolean;
	/** Opened by a click or tap: pointer and focus leaving do not close it. */
	pinned: boolean;
}

export const INFO_TIP_CLOSED: InfoTipState = { open: false, pinned: false };

export type InfoTipEvent =
	| { type: "hover"; open: boolean }
	| { type: "focus"; open: boolean }
	/** A click or tap on the icon, from a pointer or Enter/Space. */
	| { type: "press" }
	/** Escape, or a press outside the icon and the tip. */
	| { type: "dismiss" };

export function reduceInfoTip(
	state: InfoTipState,
	event: InfoTipEvent,
): InfoTipState {
	switch (event.type) {
		case "hover":
			if (event.open) return { ...state, open: true };
			// The pointer leaving must not take away a tip somebody pinned.
			return state.pinned ? state : INFO_TIP_CLOSED;
		case "focus":
			return event.open ? { ...state, open: true } : INFO_TIP_CLOSED;
		case "press":
			return state.pinned ? INFO_TIP_CLOSED : { open: true, pinned: true };
		case "dismiss":
			return INFO_TIP_CLOSED;
	}
}

/**
 * The event for a change Base UI reports, or null for one the tip handles
 * itself (the press, through the button's own click handler, which also fires
 * for touch and for Enter and Space).
 */
export function infoTipEventFor(
	reason: string,
	open: boolean,
): InfoTipEvent | null {
	switch (reason) {
		case "trigger-hover":
			return { type: "hover", open };
		case "trigger-focus":
			return { type: "focus", open };
		case "escape-key":
		case "outside-press":
			return { type: "dismiss" };
		default:
			return null;
	}
}
