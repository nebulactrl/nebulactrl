import { useSyncExternalStore } from "react";

// A module-level store, not context: "Run command…" sits in the dock's More
// menu, the card's context menu and ⌘K, "Run history" in the Tasks section;
// the dialog and the drawer are mounted once by the canvas screen.
interface Store<T> {
	get(): T;
	set(next: T): void;
	subscribe(listener: () => void): () => void;
}

function createStore<T>(initial: T): Store<T> {
	let value = initial;
	const listeners = new Set<() => void>();
	return {
		get: () => value,
		set(next) {
			value = next;
			for (const listener of listeners) listener();
		},
		subscribe(listener) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	};
}

function useStore<T>(store: Store<T>): T {
	return useSyncExternalStore(store.subscribe, store.get, store.get);
}

export interface RunHistoryTarget {
	serviceId: string;
	/** Only the runs of this task; unset lists every run of the service. */
	taskId?: string;
}

const runCommand = createStore<string | null>(null);
const runHistory = createStore<RunHistoryTarget | null>(null);

/** Opens the "Run command" dialog of a service. */
export function openRunCommand(serviceId: string): void {
	runCommand.set(serviceId);
}

export function closeRunCommand(): void {
	runCommand.set(null);
}

/** The service whose run-command dialog is open, if any. */
export function useRunCommandTarget(): string | null {
	return useStore(runCommand);
}

/** Opens the run-history drawer of a service, optionally for one task. */
export function openRunHistory(serviceId: string, taskId?: string): void {
	runHistory.set(taskId ? { serviceId, taskId } : { serviceId });
}

export function closeRunHistory(): void {
	runHistory.set(null);
}

/** What the run-history drawer shows, if it is open. */
export function useRunHistoryTarget(): RunHistoryTarget | null {
	return useStore(runHistory);
}
