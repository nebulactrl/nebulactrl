import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback } from "react";
import { z } from "zod";

/**
 * `?component=<name>` of the canvas address: the component of the inspected
 * stack that the Overview highlights and the Logs tab filters to. Routes that
 * host the inspector spread this into their `validateSearch`; a malformed
 * value is dropped. It belongs to one service, so changing the inspected
 * service clears it.
 */
export const stackSearch = {
	component: z.string().optional().catch(undefined),
};

/** The component the address names, if any. */
export function useComponentParam(): string | undefined {
	const search = useSearch({ strict: false }) as { component?: unknown };
	return typeof search.component === "string" && search.component !== ""
		? search.component
		: undefined;
}

/** What `useStackNavigation().show` may change in one step. */
export interface ShowOptions {
	service?: string;
	tab?: string;
	/** The component to name in the address; `null` clears it. */
	component?: string | null;
	/** The process to name in the address; `null` clears it. */
	process?: string | null;
	/** The Settings section to open; given with `tab: "settings"`. */
	section?: string;
}

/**
 * Moves the address in a single navigation: service, tab and component
 * together, so two writes never race over a stale `previous`.
 */
export function useStackNavigation(): {
	show(options: ShowOptions): void;
} {
	const navigate = useNavigate();
	const show = useCallback(
		(options: ShowOptions) => {
			void navigate({
				to: ".",
				search: (previous: Record<string, unknown>) => ({
					...previous,
					...(options.service === undefined
						? {}
						: { service: options.service }),
					...(options.tab === undefined ? {} : { tab: options.tab }),
					// A section belongs to Settings of one service.
					...(options.service === undefined && options.tab === undefined
						? {}
						: { section: options.section }),
					...(options.component === undefined
						? {}
						: { component: options.component ?? undefined }),
					...(options.process === undefined
						? {}
						: { process: options.process ?? undefined }),
				}),
				replace: options.service === undefined,
			} as never);
		},
		[navigate],
	);
	return { show };
}
