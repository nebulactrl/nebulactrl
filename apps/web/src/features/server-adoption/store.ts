import { useSyncExternalStore } from "react";

// A module-level store, like the Coolify import's: the servers list, a server's
// page and the enrol dialog open the same review, which is mounted once by each
// page that can open it. Only the open flag and the server live here; every
// choice the reader makes lives in the dialog content's own state, so closing
// the dialog (which unmounts it) drops them.
interface State {
	open: boolean;
	serverId?: string;
}

let state: State = { open: false };
const listeners = new Set<() => void>();

function set(next: State) {
	state = next;
	for (const listener of listeners) listener();
}

export function useAdoptionState(): State {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => state,
		() => state,
	);
}

/** Opens the review of the workloads found on a server. */
export function openAdoption(serverId: string) {
	set({ open: true, serverId });
}

export function closeAdoption() {
	// The server stays, so the content does not change while the dialog fades out.
	set(
		state.serverId === undefined
			? { open: false }
			: { open: false, serverId: state.serverId },
	);
}
