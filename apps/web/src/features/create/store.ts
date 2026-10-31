import { useSyncExternalStore } from "react";

export type CreateKind = "git" | "image" | "database";

export interface CreateOptions {
	/** Canvas coordinates the new node is dropped at; unset lets the canvas place it. */
	position?: { x: number; y: number };
	/** Preselects a database engine (a catalog id such as `postgres`). */
	engine?: string;
}

export interface CreateRequest extends CreateOptions {
	kind: CreateKind;
}

// A module-level store, not context: the Create button sits in the canvas
// toolbar, the host (dialog) is mounted elsewhere on the same screen, and
// the palette and context menu call `open` from neither.
interface State {
	/** The dialog to show, if any. */
	request: CreateRequest | null;
	/** The menu is open; `options` are carried into the dialog it leads to. */
	menu: { options: CreateOptions } | null;
	/** How many Create buttons are mounted: only they can show the menu. */
	buttons: number;
}

let state: State = { request: null, menu: null, buttons: 0 };
const listeners = new Set<() => void>();

function set(next: Partial<State>) {
	state = { ...state, ...next };
	for (const listener of listeners) listener();
}

export function useCreateState(): State {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => state,
		() => state,
	);
}

export const createActions = {
	/**
	 * With a kind: the dialog. Without: the menu, or, when no Create button is
	 * on screen to show it, the Docker image dialog (the most common start).
	 */
	open(kind?: CreateKind, options: CreateOptions = {}) {
		if (kind) {
			set({ request: { kind, ...options }, menu: null });
		} else if (state.buttons > 0) {
			set({ menu: { options } });
		} else {
			set({ request: { kind: "image", ...options } });
		}
	},
	closeDialog() {
		set({ request: null });
	},
	setMenuOpen(open: boolean) {
		if (open) set({ menu: state.menu ?? { options: {} } });
		else set({ menu: null });
	},
	/** The menu picked an entry: carry its options into the dialog. */
	choose(kind: CreateKind, engine?: string) {
		const options = state.menu?.options ?? {};
		set({
			request: {
				kind,
				...options,
				...(engine ? { engine } : {}),
			},
			menu: null,
		});
	},
	registerButton(): () => void {
		set({ buttons: state.buttons + 1 });
		return () => set({ buttons: state.buttons - 1 });
	},
};
