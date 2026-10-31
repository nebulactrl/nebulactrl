/**
 * The guard over a production Deploy key. `armed` is the lifted cover: the
 * key beneath can be pressed. `sent` is a press whose request has not
 * resolved yet; the cover is closed again by then, and a deploy in flight
 * cannot be undone, so nothing but `resolved` touches it.
 */
export interface GuardState {
	armed: boolean;
	sent: boolean;
}

export type GuardEvent =
	/** The cover was lifted (click, Enter, Space). */
	| "lift"
	/** The key under a lifted cover was pressed. */
	| "press"
	/** The cover was lowered on purpose (the toggle pressed again). */
	| "lower"
	/** A pointer press landed outside the key and its cover, or focus left them. */
	| "outside"
	| "escape"
	/** The lifted cover stayed up for its whole allowance. */
	| "timeout"
	/** The deploy request this guard's press started has resolved. */
	| "resolved";

export const GUARD_CLOSED: GuardState = { armed: false, sent: false };

export function guardReducer(state: GuardState, event: GuardEvent): GuardState {
	switch (event) {
		case "lift":
			// A deploy is on its way; the key is not offered again until it is done.
			return state.sent || state.armed ? state : { ...state, armed: true };
		case "press":
			// Only a lifted cover has a key to press. Pressing closes the cover
			// at once; the in-flight state shows on the closed guard.
			return state.armed && !state.sent ? { armed: false, sent: true } : state;
		case "lower":
		case "outside":
		case "escape":
		case "timeout":
			return state.armed ? { ...state, armed: false } : state;
		case "resolved":
			return state.sent ? { ...state, sent: false } : state;
	}
}

/**
 * Whether a click on the key under the cover may start a deploy. `inert` and
 * `display: none` keep a person away from the key but not a script's
 * `element.click()`, so the handler checks the guard itself: only a lifted
 * cover with nothing in flight has a key to press.
 */
export function canPressKey(state: GuardState, disabled: boolean): boolean {
	return state.armed && !state.sent && !disabled;
}
