import { useSyncExternalStore } from "react";
import type { ServiceLink } from "#/api/models";

/** Where the connect popover opens: the drop point of a cable, or an element. */
export type ConnectAnchor = { x: number; y: number } | { element: HTMLElement };

// A module-level store, like the create flow's: an edge is drawn deep in the
// canvas, an edge is clicked there too, and the dialogs they lead to live in
// the host the canvas screen mounts once.
interface State {
	/** The link whose references sheet is open. */
	references: ServiceLink | null;
}

let state: State = { references: null };
const listeners = new Set<() => void>();
/** The mounted host's way of reviewing a new link; null while none is mounted. */
let reviewer:
	| ((fromId: string, toId: string, at?: ConnectAnchor) => void)
	| null = null;

function set(next: State) {
	state = next;
	for (const listener of listeners) listener();
}

export function useLinkState(): State {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => state,
		() => state,
	);
}

export const linkActions = {
	request(fromId: string, toId: string, at?: ConnectAnchor) {
		reviewer?.(fromId, toId, at);
	},
	openReferences(link: ServiceLink) {
		set({ references: link });
	},
	closeReferences() {
		set({ references: null });
	},
	/** The host registers itself; returns the unregister function. */
	registerReviewer(
		run: (fromId: string, toId: string, at?: ConnectAnchor) => void,
	): () => void {
		reviewer = run;
		return () => {
			if (reviewer === run) reviewer = null;
		};
	},
};
