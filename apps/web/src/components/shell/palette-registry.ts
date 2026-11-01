import type { LucideIcon } from "lucide-react";
import { useEffect, useSyncExternalStore } from "react";

/** One row of the ⌘K palette. */
export interface PaletteCommand {
	/** Unique across the palette. */
	id: string;
	/** What it does: "Open servers", "Deploy whoami". */
	label: string;
	/** The mono tag drawn at the row's right: "Go to", "Action", … */
	group?: string;
	/** Where or what for: "project", "production". */
	hint?: string;
	/** Words that find the row without being drawn. */
	keywords?: readonly string[];
	icon?: LucideIcon;
	/** A single-key shortcut for the same action, drawn as a key chip. */
	shortcut?: string;
	/**
	 * The row cannot run right now ("nothing staged"). It stays findable by
	 * typing but is left out of the empty prompt, so it never outranks a row
	 * that works.
	 */
	disabled?: boolean;
	/**
	 * A rare or destructive row ("Drain agent1"): offered only once the reader
	 * has typed something, never in the empty prompt.
	 */
	searchOnly?: boolean;
	run: () => void;
}

// A module-level store, not React context: features register commands from
// wherever they mount (the canvas registers "Deploy <service>", the shell
// registers navigation) and the palette, mounted once in the shell, reads
// the union. Each source owns its slice, so re-registering replaces rather
// than appends.
const sources = new Map<string, readonly PaletteCommand[]>();
const listeners = new Set<() => void>();
let snapshot: readonly PaletteCommand[] = [];

function emit() {
	snapshot = [...sources.values()].flat();
	for (const listener of listeners) listener();
}

/**
 * Sets `sourceId`'s commands, replacing what it registered before. Returns
 * the unregister function.
 */
function registerCommands(
	sourceId: string,
	commands: readonly PaletteCommand[],
): () => void {
	sources.set(sourceId, commands);
	emit();
	return () => {
		// A newer registration from the same source stays.
		if (sources.get(sourceId) !== commands) return;
		sources.delete(sourceId);
		emit();
	};
}

/**
 * Registers `commands` for as long as the caller is mounted. Memoise the
 * array (`useMemo`) so the registration only changes when it should.
 */
export function useRegisterCommands(
	sourceId: string,
	commands: readonly PaletteCommand[],
) {
	useEffect(() => registerCommands(sourceId, commands), [sourceId, commands]);
}

function subscribe(listener: () => void): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}

/** Every registered command, in registration order. */
export function useCommandRegistry(): readonly PaletteCommand[] {
	return useSyncExternalStore(
		subscribe,
		() => snapshot,
		() => snapshot,
	);
}
