import { useSyncExternalStore } from "react";

// A module-level store, like the Compose import's: the Create menu and the ⌘K
// palette open the same dialog, which is mounted once by the canvas screen.
// The form lives in the dialog content, so closing it starts the next stack
// from a blank file.
interface State {
	open: boolean;
	/** Canvas coordinates the new node is dropped at; unset lets the canvas place it. */
	position: { x: number; y: number } | undefined;
}

let state: State = { open: false, position: undefined };
const listeners = new Set<() => void>();

function set(next: State) {
	state = next;
	for (const listener of listeners) listener();
}

export function useCreateStackState(): State {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => state,
		() => state,
	);
}

export const createStackActions = {
	open(position?: { x: number; y: number }) {
		set({ open: true, position });
	},
	close() {
		set({ open: false, position: state.position });
	},
};
