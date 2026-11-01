import { useSyncExternalStore } from "react";

// A module-level store, like the Docker Compose import's: the Create menu and
// the ⌘K palette open the same dialog, which is mounted once by the canvas
// screen. Only the open flag lives here. Everything the reader types, the
// Coolify token above all, lives in the dialog content's own state, so closing
// the dialog (which unmounts it) drops it.
interface State {
	open: boolean;
}

let state: State = { open: false };
const listeners = new Set<() => void>();

function set(next: State) {
	state = next;
	for (const listener of listeners) listener();
}

export function useCoolifyImportState(): State {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => state,
		() => state,
	);
}

export const coolifyImportActions = {
	open() {
		set({ open: true });
	},
	close() {
		set({ open: false });
	},
};
