import { describe, expect, test } from "bun:test";
import {
	compareVersions,
	createBlocker,
	defaultVersion,
	freeName,
	initialEngine,
	isValidDatabaseName,
	nameTaken,
	needsLicenseAck,
	sortEngines,
	specLabel,
	takesDatabaseName,
	upgradeTargets,
	versionsFor,
} from "./engines";
import { ENGINE_CATALOG } from "./engines.fixture";

const engine = (id: string) => {
	const found = ENGINE_CATALOG.find((entry) => entry.id === id);
	if (!found) throw new Error(`no ${id} in the fixture`);
	return found;
};

describe("engine order and default", () => {
	test("Valkey leads, the rest keep the catalog's order", () => {
		expect(sortEngines(ENGINE_CATALOG).map((entry) => entry.id)).toEqual([
			"valkey",
			"postgres",
			"mysql",
			"mariadb",
			"redis",
			"mongodb",
			"clickhouse",
		]);
	});
	test("the dialog opens on the requested engine, else Valkey", () => {
		expect(initialEngine(ENGINE_CATALOG, "postgres")?.id).toBe("postgres");
		expect(initialEngine(ENGINE_CATALOG)?.id).toBe("valkey");
		expect(initialEngine(ENGINE_CATALOG, "nope")?.id).toBe("valkey");
		expect(initialEngine([], "postgres")).toBeUndefined();
	});
});

describe("versions", () => {
	test("the default version is preselected", () => {
		expect(defaultVersion(engine("postgres"))).toBe("18");
		expect(defaultVersion(engine("mysql"))).toBe("8.4");
	});
	test("a flavor narrows the versions and falls back to its newest", () => {
		const postgres = engine("postgres");
		expect(versionsFor(postgres, "postgis").map((v) => v.id)).toEqual([
			"17",
			"16",
		]);
		expect(defaultVersion(postgres, "postgis")).toBe("17");
	});
	test("versions compare numerically, part by part", () => {
		expect(compareVersions("9", "10")).toBeLessThan(0);
		expect(compareVersions("8.4", "9.7")).toBeLessThan(0);
		expect(compareVersions("11.8", "11.10")).toBeLessThan(0);
		expect(compareVersions("17", "17.0")).toBe(0);
		expect(compareVersions("26.8", "25.8")).toBeGreaterThan(0);
	});
});

describe("upgradeTargets", () => {
	test("offers only strictly newer versions, oldest first", () => {
		const postgres = engine("postgres");
		expect(upgradeTargets(postgres, "16").map((v) => v.id)).toEqual([
			"17",
			"18",
		]);
		expect(upgradeTargets(postgres, "17").map((v) => v.id)).toEqual(["18"]);
		expect(upgradeTargets(postgres, "18")).toEqual([]);
	});
	test("a flavor only offers the versions it ships", () => {
		expect(
			upgradeTargets(engine("postgres"), "16", "postgis").map((v) => v.id),
		).toEqual(["17"]);
	});
	test("a version the catalog no longer lists still compares", () => {
		expect(upgradeTargets(engine("mysql"), "5.7").map((v) => v.id)).toEqual([
			"8.4",
			"9.7",
		]);
	});
});

describe("licence gating", () => {
	test("source-available and opt-in engines need a reviewed licence", () => {
		expect(needsLicenseAck(engine("mongodb"))).toBe(true);
		expect(needsLicenseAck(engine("redis"))).toBe(true);
		expect(needsLicenseAck(engine("valkey"))).toBe(false);
		expect(needsLicenseAck(engine("postgres"))).toBe(false);
	});
	test("Create stays blocked until the licence is ticked", () => {
		const draft = {
			engine: engine("mongodb"),
			version: "8.0",
			databaseName: "",
			serverId: "srv",
			name: "mongodb",
			licenseAccepted: false,
		};
		expect(createBlocker(draft)).toMatch(/licence/);
		expect(createBlocker({ ...draft, licenseAccepted: true })).toBeNull();
	});
	test("a permissive engine is not blocked on a licence", () => {
		expect(
			createBlocker({
				engine: engine("postgres"),
				version: "18",
				databaseName: "",
				serverId: "srv",
				name: "postgres",
				licenseAccepted: false,
			}),
		).toBeNull();
	});
});

describe("create form validation", () => {
	test("database names follow the server's pattern", () => {
		expect(isValidDatabaseName("app")).toBe(true);
		expect(isValidDatabaseName("_a1")).toBe(true);
		expect(isValidDatabaseName("1app")).toBe(false);
		expect(isValidDatabaseName("App")).toBe(false);
		expect(isValidDatabaseName("a-b")).toBe(false);
		expect(isValidDatabaseName("a".repeat(63))).toBe(true);
		expect(isValidDatabaseName("a".repeat(64))).toBe(false);
		expect(isValidDatabaseName("")).toBe(false);
	});
	test("key-value engines have no database name", () => {
		expect(takesDatabaseName(engine("valkey"))).toBe(false);
		expect(takesDatabaseName(engine("postgres"))).toBe(true);
		expect(takesDatabaseName(engine("clickhouse"))).toBe(true);
	});
	test("blocks until engine, version, name and server are set", () => {
		const base = {
			engine: engine("valkey"),
			version: "9",
			databaseName: "",
			serverId: "srv",
			name: "valkey",
			licenseAccepted: false,
		};
		expect(createBlocker(base)).toBeNull();
		expect(createBlocker({ ...base, engine: undefined })).not.toBeNull();
		expect(createBlocker({ ...base, version: undefined })).not.toBeNull();
		expect(createBlocker({ ...base, name: "  " })).not.toBeNull();
		expect(createBlocker({ ...base, serverId: "" })).not.toBeNull();
	});
	test("an invalid database name blocks, an empty one defaults on the server", () => {
		const base = {
			engine: engine("postgres"),
			version: "18",
			serverId: "srv",
			name: "postgres",
			licenseAccepted: false,
		};
		expect(createBlocker({ ...base, databaseName: "" })).toBeNull();
		expect(createBlocker({ ...base, databaseName: "My DB" })).not.toBeNull();
	});
});

describe("specLabel", () => {
	test("reads engine and version, with the flavor", () => {
		expect(
			specLabel({
				engine: engine("postgres"),
				spec: { engine: "postgres", version: "17", flavor: "postgis" },
			}),
		).toBe("PostgreSQL 17 · PostGIS");
		expect(
			specLabel({
				engine: engine("mysql"),
				spec: { engine: "mysql", version: "8.4" },
			}),
		).toBe("MySQL 8.4 LTS");
	});
});

describe("name collisions", () => {
	test("a name already used in the environment is taken, whatever its case", () => {
		expect(nameTaken("postgres", ["api", "Postgres"])).toBe(true);
		expect(nameTaken("valkey", ["api", "Postgres"])).toBe(false);
	});

	test("the first free name gets a number", () => {
		expect(freeName("postgres", ["api"])).toBe("postgres");
		expect(freeName("postgres", ["postgres"])).toBe("postgres-2");
		expect(freeName("postgres", ["postgres", "postgres-2"])).toBe("postgres-3");
	});

	test("a taken name blocks Create and says so", () => {
		const draft = {
			engine: engine("postgres"),
			version: "18",
			databaseName: "",
			serverId: "srv",
			name: "postgres",
			licenseAccepted: false,
			taken: ["postgres"],
		};
		expect(createBlocker(draft)).toMatch(/already exists/);
		expect(createBlocker({ ...draft, taken: [] })).toBeNull();
	});
});
