import { describe, expect, test } from "bun:test";
import { stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import { fleetQuery, serverDrainQuery } from "./queries";

const totals = {
	servers: 0,
	services: 0,
	memTotalBytes: 0,
	memUsedBytes: 0,
	committedMemBytes: 0,
};

describe("fleetQuery", () => {
	test("keeps the totals and turns a null server list into an empty one", async () => {
		stubApi({ "GET /fleet": { body: { servers: null, totals } } });
		const data = await testQueryClient().fetchQuery(fleetQuery());
		// fetchQuery returns the raw reply; select runs through the observer.
		expect(data.totals).toEqual(totals);
		const select = fleetQuery().select;
		expect(select?.(data)).toEqual({ servers: [], totals });
	});

	test("polls every 15 seconds as the fallback to the event feed", () => {
		expect(fleetQuery().refetchInterval).toBe(15_000);
	});
});

describe("serverDrainQuery", () => {
	test("requests the server's drain and exposes null when it was never drained", async () => {
		const api = stubApi({
			"GET /servers/:id/drain": { body: { drain: null } },
		});
		const query = serverDrainQuery("s1", false);
		const raw = await testQueryClient().fetchQuery(query);
		expect(api.calls.map((c) => c.path)).toEqual(["/servers/s1/drain"]);
		expect(query.select?.(raw)).toBeNull();
	});

	test("polls faster while a drain runs", () => {
		expect(serverDrainQuery("s1", true).refetchInterval).toBe(3_000);
		expect(serverDrainQuery("s1", false).refetchInterval).toBe(15_000);
	});
});
