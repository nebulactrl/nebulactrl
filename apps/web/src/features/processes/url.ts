import { useSearch } from "@tanstack/react-router";
import { z } from "zod";

/**
 * `?process=<name>` of the canvas address: the process of the inspected
 * service that the Overview highlights and the Logs, Metrics and terminal
 * views narrow to. Routes that host the inspector spread this into their
 * `validateSearch`; a malformed value is dropped. It belongs to one service,
 * so changing the inspected service clears it. (`useStackNavigation().show`
 * moves it together with the service and tab.)
 */
export const processSearch = {
	process: z.string().optional().catch(undefined),
};

/** The process the address names, if any. */
export function useProcessParam(): string | undefined {
	const search = useSearch({ strict: false }) as { process?: unknown };
	return typeof search.process === "string" && search.process !== ""
		? search.process
		: undefined;
}
