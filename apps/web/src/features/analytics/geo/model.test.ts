import { describe, expect, test } from "bun:test";
import type { MapBounds } from "@nebulactrl/dither/backgrounds/geo";
import type { TrafficGeo } from "../types";
import { COUNTRY_CENTROIDS } from "./country-centroids";
import {
	canZoom,
	fitBounds,
	formatShare,
	type GeoPlace,
	geoPlaces,
	hasOrigins,
	MIN_LAT_SPAN,
	MIN_WEIGHT,
	markerWeight,
	REGION_ASPECT,
	REGIONS,
	zoomBounds,
} from "./model";

function geo(over: Partial<TrafficGeo>): TrafficGeo {
	return {
		level: "country",
		source: "dbip",
		attribution: [],
		locatedRequests: 0,
		unknownRequests: 0,
		countries: [],
		cities: [],
		...over,
	};
}

describe("geoPlaces", () => {
	test("country level uses centroids, busiest first, weight 1 for the top one", () => {
		const places = geoPlaces(
			geo({
				countries: [
					{ iso2: "DE", name: "Germany", requests: 30, share: 0.3 },
					{ iso2: "US", name: "United States", requests: 70, share: 0.7 },
				],
			}),
		);
		expect(places.map((m) => m.label)).toEqual(["United States", "Germany"]);
		expect(places[0]?.at).toEqual(COUNTRY_CENTROIDS.US);
		expect(places[0]?.weight).toBe(1);
		// Log scale: a place with 43% of the top one's traffic is clearly lit.
		expect(places[1]?.weight).toBeGreaterThan(0.3 / 0.7);
		expect(places[1]?.weight).toBeLessThan(1);
	});

	test("city level uses the city coordinates", () => {
		const places = geoPlaces(
			geo({
				level: "city",
				countries: [
					{ iso2: "GB", name: "United Kingdom", requests: 10, share: 1 },
				],
				cities: [
					{
						name: "London",
						country: "GB",
						lat: 51.5,
						lon: -0.1,
						requests: 10,
						share: 1,
					},
				],
			}),
		);
		expect(places).toHaveLength(1);
		expect(places[0]?.at).toEqual([51.5, -0.1]);
		expect(places[0]?.label).toBe("London, GB");
	});

	test("city level without cities falls back to countries", () => {
		const places = geoPlaces(
			geo({
				level: "city",
				countries: [{ iso2: "FR", name: "France", requests: 5, share: 1 }],
			}),
		);
		expect(places[0]?.label).toBe("France");
	});

	test("skips codes without a centroid", () => {
		const codes = Object.keys(COUNTRY_CENTROIDS).slice(0, 40);
		const countries = codes.map((iso2, i) => ({
			iso2,
			name: iso2,
			requests: 100 - i,
			share: (100 - i) / 4000,
		}));
		countries.push({ iso2: "ZZ", name: "Nowhere", requests: 1000, share: 0.5 });
		const places = geoPlaces(geo({ countries }));
		expect(places).toHaveLength(codes.length);
		expect(places.some((m) => m.label === "Nowhere")).toBe(false);
	});

	test("a quiet marker keeps a visible weight, a tenth of the top sits half way", () => {
		expect(markerWeight(0.0001, 0.9)).toBeCloseTo(MIN_WEIGHT, 2);
		expect(markerWeight(0.9, 0.9)).toBe(1);
		expect(markerWeight(0, 0)).toBe(MIN_WEIGHT);
		expect(markerWeight(0.09, 0.9)).toBeCloseTo(
			MIN_WEIGHT + (1 - MIN_WEIGHT) * Math.log10(1.9),
			5,
		);
	});
});

function place(label: string, lat: number, lon: number): GeoPlace {
	return {
		key: label,
		country: "XX",
		at: [lat, lon],
		label,
		requests: 1,
		share: 1,
		weight: 1,
	};
}

describe("regions", () => {
	test("every preset but the world keeps the region aspect", () => {
		for (const r of REGIONS.filter((r) => r.id !== "world")) {
			expect(
				(r.bounds.east - r.bounds.west) / (r.bounds.north - r.bounds.south),
			).toBeCloseTo(REGION_ASPECT, 5);
		}
	});

	test("fitting one city gives the smallest region around it", () => {
		const b = fitBounds([place("Wennigsen", 52.27, 9.57)]);
		expect(b.north - b.south).toBeCloseTo(MIN_LAT_SPAN, 5);
		expect((b.north + b.south) / 2).toBeCloseTo(52.27, 5);
		expect((b.west + b.east) / 2).toBeCloseTo(9.57, 5);
	});

	test("a small map can ask for more context around one place", () => {
		const b = fitBounds([place("Wennigsen", 52.27, 9.57)], 0.25, 40);
		expect(b.north - b.south).toBeCloseTo(40, 5);
		expect((b.west + b.east) / 2).toBeCloseTo(9.57, 5);
	});

	test("fitting places on two continents keeps both inside, with a margin", () => {
		const b = fitBounds([
			place("Wennigsen", 52.27, 9.57),
			place("Los Angeles", 34.05, -118.24),
		]);
		expect(b.west).toBeLessThan(-118.24);
		expect(b.east).toBeGreaterThan(9.57);
		expect(b.north).toBeGreaterThan(52.27);
		expect(b.south).toBeLessThan(34.05);
		expect((b.east - b.west) / (b.north - b.south)).toBeCloseTo(
			REGION_ASPECT,
			5,
		);
	});

	test("zoom stops at the smallest region and at the world", () => {
		let b: MapBounds = REGIONS[1].bounds;
		for (let i = 0; i < 20; i++) b = zoomBounds(b, 0.5);
		expect(b.north - b.south).toBeCloseTo(MIN_LAT_SPAN, 5);
		expect(canZoom(b).in).toBe(false);
		for (let i = 0; i < 20; i++) b = zoomBounds(b, 2);
		expect(b).toEqual(REGIONS[0].bounds);
		expect(canZoom(b).out).toBe(false);
	});

	test("a region never leaves the world's latitudes", () => {
		const b = fitBounds([place("Longyearbyen", 78.2, 15.6)]);
		expect(b.north).toBeLessThanOrEqual(REGIONS[0].bounds.north);
	});
});

describe("helpers", () => {
	test("hasOrigins", () => {
		expect(hasOrigins({ locatedRequests: 0 })).toBe(false);
		expect(hasOrigins({ locatedRequests: 3 })).toBe(true);
	});
	test("formatShare", () => {
		expect(formatShare(0)).toBe("0%");
		expect(formatShare(0.0004)).toBe("<0.1%");
		expect(formatShare(0.034)).toBe("3.4%");
		expect(formatShare(0.42)).toBe("42%");
	});
});
