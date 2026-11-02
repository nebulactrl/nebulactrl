// Test-only: a complete traffic report to override field by field.
import type { TrafficReport } from "./types";

const T0 = Date.UTC(2026, 0, 1, 12);

export function makeReport(
	overrides: Partial<TrafficReport> = {},
): TrafficReport {
	return {
		range: {
			from: new Date(T0 - 3_600_000).toISOString(),
			to: new Date(T0).toISOString(),
			stepSeconds: 60,
			truncated: false,
			retention: { minuteHours: 48, hourlyDays: 30, dailyDays: 365 },
		},
		summary: {
			requests: 12_400,
			visitors: 900,
			bytesOut: 5 * 1024 * 1024,
			bytesIn: 1024,
			botRequests: 1_240,
			documents: 3_000,
			p50Ms: 21,
			p95Ms: 180,
			p99Ms: 950,
			errorRate: 0.0042,
			clientErrorRate: 0.02,
			peakRps: 4.25,
		},
		series: [0, 1, 2].map((i) => ({
			ts: new Date(T0 - (2 - i) * 60_000).toISOString(),
			requests: 100 + i,
			s2xx: 90,
			s3xx: 5,
			s4xx: 4,
			s5xx: 1 + i,
			p50Ms: 20,
			p95Ms: 100,
			p99Ms: 400,
			bytesOut: 1_000,
			visitors: 10,
		})),
		sparklines: {
			requests: [1, 2, 3],
			visitors: [1, 2, 3],
			bytesOut: [1, 2, 3],
			p95Ms: [1, 2, 3],
		},
		deployments: [],
		statusCodes: [
			{ code: 200, n: 12_000 },
			{ code: 404, n: 300 },
			{ code: 503, n: 100 },
		],
		topPaths: [
			{ k: "/", n: 5_000, s5xx: 0, avgMs: 40 },
			{ k: "/api/orders", n: 3_000, s5xx: 50, avgMs: 220 },
			{ k: "/api/users", n: 1_000, s5xx: 8 },
		],
		topPathsKind: "pages",
		topReferrers: [
			{ k: "", n: 800 },
			{ k: "news.example.org", n: 200 },
		],
		topBrowsers: [{ k: "Firefox", n: 70 }],
		topOS: [{ k: "Linux", n: 60 }],
		topDevices: [{ k: "desktop", n: 90 }],
		topHosts: [
			{ k: "app.example.com", n: 700 },
			{ k: "www.example.com", n: 300 },
		],
		notFound: [{ k: "/wp-login.php", n: 44 }],
		activeHours: null,
		...overrides,
	};
}

/** A located-origins block: two countries, optionally with cities. */
export function makeGeo(level: "country" | "city" = "country") {
	return {
		level,
		source: "dbip" as const,
		locatedRequests: 1_000,
		unknownRequests: 50,
		attribution: [
			{
				source: "dbip" as const,
				text: "IP geolocation by DB-IP",
				url: "https://db-ip.com",
			},
		],
		countries: [
			{ iso2: "DE", name: "Germany", requests: 600, share: 0.6 },
			{ iso2: "US", name: "United States", requests: 400, share: 0.4 },
		],
		cities:
			level === "city"
				? [
						{
							name: "Berlin",
							country: "DE",
							lat: 52.5,
							lon: 13.4,
							requests: 500,
							share: 0.5,
						},
						{
							name: "Austin",
							country: "US",
							lat: 30.3,
							lon: -97.7,
							requests: 300,
							share: 0.3,
						},
					]
				: null,
	};
}
