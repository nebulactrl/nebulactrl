import { useSyncExternalStore } from "react";

// A module-level store, not context: the "Maintenance mode…" entry sits in the
// dock's More menu, the card's context menu and the inspector header, and the
// dialog is mounted once by the canvas screen.
let target: string | null = null;
const listeners = new Set<() => void>();

function set(next: string | null) {
	target = next;
	for (const listener of listeners) listener();
}

/** Opens the maintenance dialog of a service. */
export function openMaintenance(serviceId: string): void {
	set(serviceId);
}

export function closeMaintenance(): void {
	set(null);
}

/** The service whose maintenance dialog is open, if any. */
export function useMaintenanceTarget(): string | null {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => target,
		() => target,
	);
}
