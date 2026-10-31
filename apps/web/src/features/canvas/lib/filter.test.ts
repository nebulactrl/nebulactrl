import { describe, expect, test } from "bun:test";
import { PLAIN_DOMAIN_RULE } from "#/api/models";
import {
	dimmingIds,
	EMPTY_FILTER,
	isFilterEmpty,
	matchesFilter,
	matchingIds,
	matchSummary,
	NO_SERVER,
} from "./filter";
import { makeDeployment, makeService } from "./fixtures";
import { nodeStatus } from "./status";

const web = makeService({
	id: "web",
	name: "Web",
	slug: "web",
	domains: [
		{
			...PLAIN_DOMAIN_RULE,
			id: "d1",
			hostname: "shop.example.com",
			targetPort: 80,
			component: "",
			process: "",
			generated: false,
			status: "active",
			statusMessage: null,
			dnsOk: true,
			dnsCheckedAt: null,
		},
	],
	currentDeploymentId: "dep-1",
	latestDeployment: makeDeployment("succeeded", {
		finishedAt: new Date().toISOString(),
	}),
});
const db = makeService({
	id: "db",
	name: "Postgres",
	slug: "postgres",
	kind: "database",
	serverId: "server-2",
	server: { id: "server-2", name: "home", status: "online", lastSeenAt: null },
	config: { ...makeService().config, image: "postgres:17" },
});
const broken = makeService({
	id: "broken",
	name: "Worker",
	kind: "worker",
	serverId: null,
	server: null,
	latestDeployment: makeDeployment("failed"),
});
const all = [web, db, broken];

describe("matchesFilter", () => {
	test("empty filter matches everything and matchingIds is null", () => {
		expect(isFilterEmpty(EMPTY_FILTER)).toBe(true);
		expect(matchingIds(all, EMPTY_FILTER)).toBeNull();
		expect(all.every((s) => matchesFilter(s, EMPTY_FILTER))).toBe(true);
	});

	test("kind filter", () => {
		const ids = matchingIds(all, {
			...EMPTY_FILTER,
			kinds: new Set(["database"]),
		});
		expect([...(ids ?? [])]).toEqual(["db"]);
	});

	test("server filter, including unplaced services", () => {
		const ids = matchingIds(all, {
			...EMPTY_FILTER,
			serverIds: new Set(["server-2", NO_SERVER]),
		});
		expect([...(ids ?? [])].sort()).toEqual(["broken", "db"]);
	});

	test("status filter uses the card's status group", () => {
		expect(nodeStatus(web).group).toBe("deployed");
		expect(nodeStatus(broken).group).toBe("failed");
		const ids = matchingIds(all, {
			...EMPTY_FILTER,
			statuses: new Set(["failed"]),
		});
		expect([...(ids ?? [])]).toEqual(["broken"]);
	});

	test("search matches name, domain, image and server name; all terms required", () => {
		const q = (query: string) => [
			...(matchingIds(all, { ...EMPTY_FILTER, query }) ?? []),
		];
		expect(q("shop.example")).toEqual(["web"]);
		expect(q("POSTGRES:17")).toEqual(["db"]);
		expect(q("home")).toEqual(["db"]);
		expect(q("hetzner web")).toEqual(["web"]);
		expect(q("nothing-matches")).toEqual([]);
	});

	test("groups combine with AND across, OR within", () => {
		const ids = matchingIds(all, {
			...EMPTY_FILTER,
			kinds: new Set(["app", "database"]),
			serverIds: new Set(["server-1"]),
		});
		expect([...(ids ?? [])]).toEqual(["web"]);
	});
});

describe("dimmingIds", () => {
	test("passes the matches through, and null (nothing filtered) as it is", () => {
		const ids = new Set(["web"]);
		expect(dimmingIds(ids)).toBe(ids);
		expect(dimmingIds(null)).toBeNull();
	});

	test("a filter nothing passes dims nothing", () => {
		expect(dimmingIds(new Set())).toBeNull();
	});
});

describe("matchSummary", () => {
	test("counts the matches, or says there are none", () => {
		expect(matchSummary(2, 3)).toBe("2 of 3");
		expect(matchSummary(3, 3)).toBe("3 of 3");
		expect(matchSummary(0, 3)).toBe("No matches");
	});
});
