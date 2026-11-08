import { useSearch } from "@tanstack/react-router";
import { useCallback, useMemo } from "react";
import { z } from "zod";
import { useSearchWriter } from "#/features/logs/history/url";

/** What a log line comes from: a service's own output, or the requests its server proxied. */
export type LogSource = "runtime" | "requests";

/** Every source, in the order the address and the API spell them. */
export const ALL_SOURCES: readonly LogSource[] = ["runtime", "requests"];

/**
 * The address state of the source filter: `?sources=runtime` (a comma list;
 * absent means both). Routes that host the explorer spread this into their
 * `validateSearch`; the value stays the text the address holds and
 * `useSourcesParam` reads it, so a malformed value costs nothing.
 */
export const logSourcesSearch = {
	sources: z.string().optional().catch(undefined),
};

/**
 * The sources a `?sources=` value names. Unknown names are dropped, repeats
 * are merged and the order is always runtime first; nothing left (or nothing
 * at all) is every source.
 */
export function parseSources(value: unknown): readonly LogSource[] {
	if (typeof value !== "string") return ALL_SOURCES;
	const named = new Set(value.split(",").map((part) => part.trim()));
	const known = ALL_SOURCES.filter((source) => named.has(source));
	return known.length === 0 ? ALL_SOURCES : known;
}

/** The comma list the API and the address spell a source set as: "runtime,requests". */
export function sourcesParam(sources: readonly LogSource[]): string {
	return ALL_SOURCES.filter((source) => sources.includes(source)).join(",");
}

/**
 * Whether `sources` is exactly the runtime log: what an endpoint serves when
 * `sources` is left out.
 */
export function isRuntimeOnly(sources: readonly LogSource[]): boolean {
	return sourcesParam(sources) === "runtime";
}

/** The sources shown, from `?sources=` (both by default). Choosing both removes the parameter. */
export function useSourcesParam(): {
	sources: readonly LogSource[];
	setSources(next: readonly LogSource[]): void;
} {
	const search = useSearch({ strict: false }) as { sources?: unknown };
	const write = useSearchWriter();
	const raw = typeof search.sources === "string" ? search.sources : undefined;
	const sources = useMemo(() => parseSources(raw), [raw]);
	const setSources = useCallback(
		(next: readonly LogSource[]) => {
			const chosen = parseSources(sourcesParam(next));
			write({
				sources:
					chosen.length === ALL_SOURCES.length
						? undefined
						: sourcesParam(chosen),
			});
		},
		[write],
	);
	return { sources, setSources };
}
