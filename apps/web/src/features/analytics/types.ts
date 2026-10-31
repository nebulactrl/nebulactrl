// The traffic-analytics payload, straight from the generated SDK. Every
// analytics file imports from here so the vocabulary is one name per thing.

import type {
	TopItem as GenTopItem,
	Geo,
	Report,
	SeriesPoint,
	Summary,
} from "#/api/gen/types.gen";

export type TrafficReport = Report;
export type TrafficSummary = Summary;
export type TrafficPoint = SeriesPoint;
export type TopItem = GenTopItem;
/** Where the window's requests came from (null on a report when geolocation is off). */
export type TrafficGeo = Geo;
