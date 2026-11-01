import {
	DEFAULT_BOUNDS,
	type LatLon,
	type MapBounds,
} from "@nebulactrl/dither/backgrounds/geo";
import type { TrafficGeo } from "../types";
import { COUNTRY_CENTROIDS } from "./country-centroids";

/** The dot map draws at most this many markers. */
export const MAX_MARKERS = 24;

/** The quietest marker still shows: a weight of 0 would hide it. */
export const MIN_WEIGHT = 0.2;

type GeoData = Pick<TrafficGeo, "level" | "countries" | "cities">;

/** One located place: a city at the city level, else a country centroid. */
export interface GeoPlace {
	key: string;
	/** ISO 3166-1 alpha-2 code of the place's country. */
	country: string;
	at: LatLon;
	label: string;
	requests: number;
	share: number;
	/** 0..1, see markerWeight. */
	weight: number;
}

/**
 * Marker weight (0..1) from a request share, on a log scale of the share of
 * the busiest place: the busiest is 1, a place with a tenth of its traffic
 * still sits half way, and nothing drops below MIN_WEIGHT. A linear scale left
 * every place but the top one as faint as empty land.
 */
export function markerWeight(share: number, maxShare: number): number {
	if (maxShare <= 0 || share <= 0) return MIN_WEIGHT;
	const ratio = Math.min(1, share / maxShare);
	return MIN_WEIGHT + (1 - MIN_WEIGHT) * Math.log10(1 + 9 * ratio);
}

/**
 * Every located place, the busiest first: cities at the city level (with
 * their own coordinates), otherwise country centroids from the static table.
 * A country without a centroid is left out (it stays in the lists).
 */
export function geoPlaces(geo: GeoData): GeoPlace[] {
	const cityList = geo.cities ?? [];
	const useCities = geo.level === "city" && cityList.length > 0;
	const entries = useCities
		? cityList.map((c) => ({
				key: `${c.name}|${c.country}|${c.lat}|${c.lon}`,
				country: c.country,
				at: [c.lat, c.lon] as const,
				label: `${c.name}, ${c.country}`,
				share: c.share,
				requests: c.requests,
			}))
		: (geo.countries ?? []).flatMap((c) => {
				const at = COUNTRY_CENTROIDS[c.iso2];
				return at
					? [
							{
								key: c.iso2,
								country: c.iso2,
								at,
								label: c.name,
								share: c.share,
								requests: c.requests,
							},
						]
					: [];
			});
	const sorted = [...entries].sort((a, b) => b.requests - a.requests);
	const maxShare = sorted.reduce((m, e) => Math.max(m, e.share), 0);
	return sorted.map((e) => ({ ...e, weight: markerWeight(e.share, maxShare) }));
}

/** Whether the window has any located request to show. */
export function hasOrigins(geo: Pick<TrafficGeo, "locatedRequests">): boolean {
	return geo.locatedRequests > 0;
}

/** "42%", "3.4%" or "<0.1%" for a 0..1 share. */
export function formatShare(share: number): string {
	const pct = share * 100;
	if (pct <= 0) return "0%";
	if (pct < 0.1) return "<0.1%";
	return pct >= 10 ? `${pct.toFixed(0)}%` : `${pct.toFixed(1)}%`;
}

// ---- regions, fitting and zoom -------------------------------------------

/** The latitudes a map may show: the dot map's world has no polar caps. */
const WORLD = DEFAULT_BOUNDS;
/** Smallest region the expanded map zooms to, in degrees of latitude. */
export const MIN_LAT_SPAN = 6;
/** Width / height every region keeps, so the map box never changes shape. */
export const REGION_ASPECT = 2;

export const REGIONS = [
	{ id: "world", label: "World", bounds: WORLD },
	{
		id: "europe",
		label: "Europe",
		bounds: { north: 72, south: 34, west: -25, east: 51 },
	},
	{
		id: "north-america",
		label: "North America",
		bounds: { north: 72, south: 10, west: -168, east: -44 },
	},
	{
		id: "south-america",
		label: "South America",
		bounds: { north: 14, south: -56, west: -130, east: 10 },
	},
	{
		id: "africa",
		label: "Africa",
		bounds: { north: 38, south: -36, west: -55, east: 93 },
	},
	{
		id: "asia",
		label: "Asia",
		bounds: { north: 66, south: -10, west: 30, east: 182 },
	},
	{
		id: "oceania",
		label: "Oceania",
		bounds: { north: 2, south: -50, west: 108, east: 212 },
	},
] as const satisfies readonly {
	id: string;
	label: string;
	bounds: MapBounds;
}[];

export type RegionId = (typeof REGIONS)[number]["id"];

/** The world map, or a region of REGION_ASPECT around `center` spanning `latSpan`, kept inside the world. */
function regionAround(
	centerLat: number,
	centerLon: number,
	latSpan: number,
): MapBounds {
	const worldLat = WORLD.north - WORLD.south;
	const span = Math.min(Math.max(latSpan, MIN_LAT_SPAN), worldLat);
	const lonSpan = Math.min(span * REGION_ASPECT, 360);
	if (span >= worldLat || lonSpan >= 360) return WORLD;
	const south = Math.min(
		Math.max(centerLat - span / 2, WORLD.south),
		WORLD.north - span,
	);
	const west = centerLon - lonSpan / 2;
	return { north: south + span, south, west, east: west + lonSpan };
}

/**
 * The region that shows every place with a margin: the busiest places
 * decide it, at REGION_ASPECT, never smaller than `minSpan` degrees of
 * latitude (`MIN_LAT_SPAN` unless a small map wants more context).
 */
export function fitBounds(
	places: readonly Pick<GeoPlace, "at">[],
	margin = 0.25,
	minSpan = MIN_LAT_SPAN,
): MapBounds {
	if (places.length === 0) return WORLD;
	const lats = places.map((p) => p.at[0]);
	const lons = places.map((p) => p.at[1]);
	const latMin = Math.min(...lats);
	const latMax = Math.max(...lats);
	const lonMin = Math.min(...lons);
	const lonMax = Math.max(...lons);
	const latSpan = (latMax - latMin) * (1 + 2 * margin);
	const lonSpan = (lonMax - lonMin) * (1 + 2 * margin);
	const span = Math.max(latSpan, lonSpan / REGION_ASPECT, minSpan);
	return regionAround((latMin + latMax) / 2, (lonMin + lonMax) / 2, span);
}

/** `bounds` zoomed by `factor` (below 1 zooms in) around its centre, within the world. */
export function zoomBounds(bounds: MapBounds, factor: number): MapBounds {
	const latSpan = (bounds.north - bounds.south) * factor;
	return regionAround(
		(bounds.north + bounds.south) / 2,
		(bounds.west + bounds.east) / 2,
		latSpan,
	);
}

/** Whether `bounds` can zoom in further / out further. */
export function canZoom(bounds: MapBounds): { in: boolean; out: boolean } {
	const span = bounds.north - bounds.south;
	return {
		in: span > MIN_LAT_SPAN + 0.01,
		out: span < WORLD.north - WORLD.south - 0.01,
	};
}
