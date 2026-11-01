import { useSyncExternalStore } from "react";

type RouteCreateMode = "external" | "port";

export interface RouteCreateRequest {
	mode: RouteCreateMode;
	/** Canvas coordinates the new node is dropped at. */
	position?: { x: number; y: number };
	/** Preselects the managed service (and its port) a port exposure targets. */
	target?: { serviceId: string; port?: number };
}

// A module-level store, like the Create menu's: the menu entry, the canvas
// context menu and a service's Networking settings all open the same dialog.
let request: RouteCreateRequest | null = null;
const listeners = new Set<() => void>();

function set(next: RouteCreateRequest | null) {
	request = next;
	for (const listener of listeners) listener();
}

export function useRouteCreateRequest(): RouteCreateRequest | null {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => request,
		() => request,
	);
}

export const routeCreate = {
	open(next: RouteCreateRequest) {
		set(next);
	},
	close() {
		set(null);
	},
};
