import { useSyncExternalStore } from "react";
import type { SetupDraft } from "./model";

export interface SetupRequest {
	defaults?: Partial<SetupDraft>;
}

// A module-level store, not context: the "Set up for me" buttons live inside
// Settings sections that re-mount as their lists change (the Tailscale
// integration is wired the moment Headscale is ready), while the dialog must
// keep following the service until it is ready. One host
// (`PlatformSetupHost`) owns the dialog.
let request: SetupRequest | null = null;
const listeners = new Set<() => void>();

function set(next: SetupRequest | null) {
	request = next;
	for (const listener of listeners) listener();
}

export function usePlatformSetupRequest(): SetupRequest | null {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => request,
		() => request,
	);
}

export const platformSetup = {
	open(defaults?: Partial<SetupDraft>) {
		set(defaults ? { defaults } : {});
	},
	close() {
		set(null);
	},
};
