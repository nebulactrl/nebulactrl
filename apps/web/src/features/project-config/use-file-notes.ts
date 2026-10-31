import { useMemo } from "react";
import { withFileNotes } from "./model";
import { useServiceConfigSource } from "./queries";

/**
 * Adds `set by nebula.toml` to the staged lines of a service whose field the
 * file owns. `enabled` holds the request back until the lines are on screen.
 */
export function useFileNotes<T extends { field: string; note?: string }>(
	serviceId: string,
	lines: readonly T[],
	enabled: boolean,
): T[] {
	const source = useServiceConfigSource(serviceId, enabled);
	return useMemo(() => withFileNotes(source.data, lines), [source.data, lines]);
}
