import { useSyncExternalStore } from "react";

// A module-level store, not context: "Open shell…" and "Browse files…" sit in
// the dock's More menu, the card's context menu, the inspector header's ⋯ and
// ⌘K; the two panels are mounted once by the canvas screen (TerminalHost).
function createStore<T>(initial: T) {
	let value = initial;
	const listeners = new Set<() => void>();
	return {
		get: () => value,
		set(next: T) {
			value = next;
			for (const listener of listeners) listener();
		},
		subscribe(listener: () => void) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	};
}

/** What opens a panel on a replica: the process (a service with several) and the index within it (a stack's component index). */
export interface PanelOptions {
	process?: string;
	replica?: number;
}

/** A service's panel target: the service and, when the opener knows them, the process and replica to start on. */
interface PanelTarget {
	serviceId: string;
	process: string | null;
	replica: number | null;
}

const shell = createStore<PanelTarget | null>(null);
const files = createStore<PanelTarget | null>(null);
const hostShell = createStore<string | null>(null);

/**
 * Opens the terminal panel of a service. `replica` preselects one (for a
 * Compose stack it is the component's index; for a service with processes
 * it counts inside `process`); without it the first running replica is
 * chosen.
 */
export function openShell(serviceId: string, options?: PanelOptions): void {
	shell.set({
		serviceId,
		process: options?.process ?? null,
		replica: options?.replica ?? null,
	});
}

export function closeShell(): void {
	shell.set(null);
}

/** Opens the file browser of a service, on the process and replica given. */
export function openFiles(serviceId: string, options?: PanelOptions): void {
	files.set({
		serviceId,
		process: options?.process ?? null,
		replica: options?.replica ?? null,
	});
}

export function closeFiles(): void {
	files.set(null);
}

export function useShellTarget(): string | null {
	return useSyncExternalStore(
		shell.subscribe,
		() => shell.get()?.serviceId ?? null,
		() => null,
	);
}

/** The replica the terminal was opened on, or null for the default. */
export function useShellReplica(): number | null {
	return useSyncExternalStore(
		shell.subscribe,
		() => shell.get()?.replica ?? null,
		() => null,
	);
}

/** The process the terminal was opened on, or null for the default. */
export function useShellProcess(): string | null {
	return useSyncExternalStore(
		shell.subscribe,
		() => shell.get()?.process ?? null,
		() => null,
	);
}

export function useFilesTarget(): string | null {
	return useSyncExternalStore(
		files.subscribe,
		() => files.get()?.serviceId ?? null,
		() => null,
	);
}

/** The replica the file browser was opened on, or null for the default. */
export function useFilesReplica(): number | null {
	return useSyncExternalStore(
		files.subscribe,
		() => files.get()?.replica ?? null,
		() => null,
	);
}

/** The process the file browser was opened on, or null for the default. */
export function useFilesProcess(): string | null {
	return useSyncExternalStore(
		files.subscribe,
		() => files.get()?.process ?? null,
		() => null,
	);
}

/** Opens the host shell panel of a server (internal/terminal/hostshell.go). */
export function openHostShell(serverId: string): void {
	hostShell.set(serverId);
}

export function closeHostShell(): void {
	hostShell.set(null);
}

export function useHostShellTarget(): string | null {
	return useSyncExternalStore(
		hostShell.subscribe,
		hostShell.get,
		hostShell.get,
	);
}
