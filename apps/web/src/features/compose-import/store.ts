import { useSyncExternalStore } from "react";

// A module-level store, like the templates gallery's: the Create menu and the
// ⌘K palette open the same dialog, which is mounted once by the canvas screen.
// All of the wizard's own state lives in the dialog content, so closing the
// dialog starts the next import from a blank source.
interface State {
	open: boolean;
}

let state: State = { open: false };
const listeners = new Set<() => void>();

function set(next: State) {
	state = next;
	for (const listener of listeners) listener();
}

export function useComposeImportState(): State {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => state,
		() => state,
	);
}

export const composeImportActions = {
	open() {
		set({ open: true });
	},
	close() {
		set({ open: false });
	},
};
