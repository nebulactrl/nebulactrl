import {
	afterEach,
	beforeEach,
	describe,
	expect,
	setSystemTime,
	test,
} from "bun:test";
import { type GeoDatabase, geoStatusView } from "./geo-status";

beforeEach(() => setSystemTime(new Date("2026-10-30T12:00:00Z")));
afterEach(() => setSystemTime());

const downloaded: GeoDatabase = {
	downloaded: true,
	source: "dbip",
	level: "country",
	label: "2026-10",
	sizeBytes: 8 * 1024 * 1024,
	checkedAt: "2026-10-30T09:00:00Z",
};

const view = (
	status: GeoDatabase | undefined,
	state: { enabled?: boolean; online?: boolean } = {},
) =>
	geoStatusView({
		enabled: state.enabled ?? true,
		online: state.online ?? true,
		status,
	});

describe("geoStatusView", () => {
	test("off: no database is kept", () => {
		const v = view(undefined, { enabled: false });
		expect(v.tone).toBe("muted");
		expect(v.rows).toEqual([]);
	});

	test("offline agent: unknown, not 'not downloaded'", () => {
		const v = view(downloaded, { online: false });
		expect(v.tone).toBe("wait");
		expect(v.headline).toContain("offline");
		expect(v.rows).toEqual([]);
	});

	test("online but nothing reported yet", () => {
		const v = view(undefined);
		expect(v.headline).toBe("Not reported yet.");
		expect(v.tone).toBe("wait");
	});

	test("enabled and still downloading", () => {
		const v = view({ downloaded: false });
		expect(v.headline).toBe("Downloading.");
		expect(v.tone).toBe("wait");
	});

	test("a failed download shows the reason", () => {
		const v = view({
			downloaded: false,
			error: "MaxMind rejected the licence key",
		});
		expect(v.tone).toBe("bad");
		expect(v.detail).toContain("MaxMind rejected the licence key");
	});

	test("downloaded DB-IP: source, release, size and last refresh", () => {
		const v = view(downloaded);
		expect(v.tone).toBe("ok");
		expect(v.headline).toBe("Downloaded.");
		expect(v.rows).toEqual([
			{ label: "Database", value: "DB-IP Lite, country level" },
			{ label: "Release", value: "2026-10 release" },
			{ label: "Size", value: "8 MiB" },
			{
				label: "Last refresh",
				value: "3h ago",
				title: "2026-10-30 09:00:00 UTC",
			},
		]);
	});

	test("downloaded MaxMind: the build date", () => {
		const v = view({
			...downloaded,
			source: "maxmind",
			level: "city",
			label: "20261014",
			buildTime: "2026-10-14T08:30:00Z",
		});
		expect(v.rows[0]?.value).toBe("MaxMind GeoLite2, city level");
		expect(v.rows[1]?.value).toBe("Built 2026-10-14");
	});

	test("a failed refresh keeps the database facts and says so", () => {
		const v = view({ ...downloaded, error: "MaxMind returned HTTP 500" });
		expect(v.tone).toBe("bad");
		expect(v.rows.length).toBeGreaterThan(0);
		expect(v.detail).toContain("HTTP 500");
	});
});
