// The server's facet counts (`LogFacetsPage`) in the shape the search box, its
// suggestions and the facet panel already read: the maps a `FacetStats` holds
// for lines in the browser. The counts cover the whole queried range, not the
// lines loaded.

import type { FacetField, LogFacetsPage } from "#/api/gen/types.gen";
import type { AttrStat, FacetStats } from "#/features/logs/search/facets";
import { FACET_KEYS, type FacetKey } from "#/features/logs/search/query";

type SearchStats = Pick<FacetStats, "facets" | "attrs">;

function valueCounts(field: FacetField): Map<string, number> {
	return new Map((field.values ?? []).map((v) => [v.value, v.count]));
}

/**
 * Server facets as search stats. A built-in facet the server did not send is
 * an empty map; an attribute the server flags numeric counts all its lines as
 * numeric (the suggestions offer range operators for it).
 */
export function toSearchStats(page: LogFacetsPage | undefined): SearchStats {
	const facets = Object.fromEntries(
		FACET_KEYS.map((key) => [key, new Map<string, number>()]),
	) as Record<FacetKey, Map<string, number>>;
	const attrs = new Map<string, AttrStat>();
	if (!page) return { facets, attrs };
	for (const field of page.facets ?? []) {
		if ((FACET_KEYS as readonly string[]).includes(field.key)) {
			facets[field.key as FacetKey] = valueCounts(field);
		}
	}
	for (const field of page.attrs ?? []) {
		// The server names an attribute `@method`; the query language and `FacetStats` use the path `method`.
		const path = field.key.startsWith("@") ? field.key.slice(1) : field.key;
		attrs.set(path, {
			count: field.count,
			numeric: field.numeric ? field.count : 0,
			values: valueCounts(field),
		});
	}
	return { facets, attrs };
}

export interface VolumePoint {
	/** Start of the bucket, epoch milliseconds. */
	start: number;
	count: number;
	/** Of `count`, the error and fatal lines and the 5xx responses. */
	errors: number;
}

/** The volume histogram of a page, oldest bucket first; buckets with an unreadable start are left out. */
export function histogramSeries(
	page: LogFacetsPage | undefined,
): VolumePoint[] {
	const series: VolumePoint[] = [];
	for (const bucket of page?.histogram ?? []) {
		const start = Date.parse(bucket.start);
		if (Number.isNaN(start)) continue;
		series.push({ start, count: bucket.count, errors: bucket.errors });
	}
	return series;
}
