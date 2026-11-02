import { describe, expect, test } from "bun:test";
import type { QueryClient } from "@tanstack/react-query";
import {
	getServerProxyQueryKey,
	getServerQueryKey,
	getServerTunnelQueryKey,
	listProjectsQueryKey,
	listServerProxyRoutesQueryKey,
	listServersQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import type { Server, TunnelView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import {
	invalidateTunnel,
	serverTunnelQuery,
	serverViewQuery,
	serverViewsQuery,
} from "./queries";

/** A server with the settings these tests read back, which differ from the defaults. */
const wireServer = (overrides: Overrides<Server> = {}) =>
	make("Server", {
		id: "srv-1",
		name: "hetzner-01",
		canDecommission: true,
		metricsRetentionDays: 30,
		diskAlertThresholdPercent: 85,
		trafficAnalyticsEnabled: true,
		trafficHourlyRetentionDays: 10,
		trafficDailyRetentionDays: 90,
		geoEnabled: true,
		geoSource: "maxmind",
		geoLevel: "city",
		geoRefreshDays: 14,
		...overrides,
	});

describe("serverViewsQuery", () => {
	test("asks for up to 200 servers and returns them as views with their settings", async () => {
		const api = stubApi({
			"GET /servers": {
				body: make("PageServer", {
					items: [wireServer(), wireServer({ id: "srv-2", name: "edge-2" })],
				}),
			},
		});
		const views = await testQueryClient().fetchQuery(serverViewsQuery());
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/servers");
		expect(api.calls[0]?.query.get("limit")).toBe("200");
		expect(views.map((v) => v.name)).toEqual(["hetzner-01", "edge-2"]);
		expect(views[0]?.settings).toEqual({
			metricsRetentionDays: 30,
			diskAlertThresholdPercent: 85,
			trafficAnalyticsEnabled: true,
			trafficHourlyRetentionDays: 10,
			trafficDailyRetentionDays: 90,
			geoEnabled: true,
			geoSource: "maxmind",
			geoLevel: "city",
			geoRefreshDays: 14,
		});
		expect(views[0]?.canDecommission).toBe(true);
	});

	test("a list without items is an empty list", async () => {
		stubApi({ "GET /servers": { body: make("PageServer", { items: null }) } });
		expect(await testQueryClient().fetchQuery(serverViewsQuery())).toEqual([]);
	});

	test("an error answer rejects the query instead of caching an empty list", async () => {
		stubApi({
			"GET /servers": problem(500, {
				title: "x",
				detail: "boom",
				code: "internal",
			}),
		});
		const client = testQueryClient();
		await expect(client.fetchQuery(serverViewsQuery())).rejects.toBeDefined();
		expect(client.getQueryData(serverViewsQuery().queryKey)).toBeUndefined();
	});

	test("caches under its own key, tagged 'view', apart from the plain server list", () => {
		const key = serverViewsQuery().queryKey;
		expect(key.at(-1)).toBe("view");
		expect(key).not.toEqual(listServersQueryKey({ query: { limit: 200 } }));
		// The generated head stays first, so event-driven invalidation of
		// `listServers` still reaches it.
		expect(key[0]).toEqual(
			listServersQueryKey({ query: { limit: 200 } })[0] as never,
		);
	});
});

describe("serverViewQuery", () => {
	test("fetches one server by id and returns its view", async () => {
		const api = stubApi({
			"GET /servers/:serverId": {
				body: wireServer({ id: "srv-7", name: "edge-7" }),
			},
		});
		const view = await testQueryClient().fetchQuery(serverViewQuery("srv-7"));
		expect(api.calls[0]?.path).toBe("/servers/srv-7");
		expect(view.id).toBe("srv-7");
		expect(view.name).toBe("edge-7");
		expect(view.settings.metricsRetentionDays).toBe(30);
	});

	test("an unknown server rejects", async () => {
		stubApi({
			"GET /servers/:serverId": problem(404, {
				title: "Not found",
				detail: "no such server",
				code: "not_found",
			}),
		});
		await expect(
			testQueryClient().fetchQuery(serverViewQuery("nope")),
		).rejects.toBeDefined();
	});

	test("each server has its own cache entry, tagged 'view'", () => {
		expect(serverViewQuery("a").queryKey).not.toEqual(
			serverViewQuery("b").queryKey,
		);
		expect(serverViewQuery("a").queryKey.at(-1)).toBe("view");
	});
});

describe("serverTunnelQuery", () => {
	const poll = (state: TunnelView["state"] | undefined) =>
		serverTunnelQuery("srv-1").refetchInterval({
			state: { data: state ? ({ state } as TunnelView) : undefined },
		});

	test("polls every 2 seconds while the connector is connecting or being removed", () => {
		expect(poll("provisioning")).toBe(2_000);
		expect(poll("removing")).toBe(2_000);
	});

	test("stops polling once the tunnel has settled or when nothing is loaded", () => {
		expect(poll("online")).toBe(false);
		expect(poll("offline")).toBe(false);
		expect(poll("off")).toBe(false);
		expect(poll("needs_attention")).toBe(false);
		expect(poll(undefined)).toBe(false);
	});
});

describe("invalidateTunnel", () => {
	const seed = (client: QueryClient) => {
		const keys = {
			tunnel: getServerTunnelQueryKey({ path: { serverId: "srv-1" } }),
			server: getServerQueryKey({ path: { serverId: "srv-1" } }),
			servers: listServersQueryKey(),
			proxy: getServerProxyQueryKey({ path: { serverId: "srv-1" } }),
			routes: listServerProxyRoutesQueryKey({ path: { serverId: "srv-1" } }),
			projects: listProjectsQueryKey(),
		};
		for (const key of Object.values(keys)) client.setQueryData(key, {});
		return keys;
	};

	test("marks the tunnel, server, list and proxy queries stale and nothing else", async () => {
		const client = testQueryClient();
		const keys = seed(client);
		await invalidateTunnel(client);
		for (const name of [
			"tunnel",
			"server",
			"servers",
			"proxy",
			"routes",
		] as const) {
			expect(client.getQueryState(keys[name])?.isInvalidated).toBe(true);
		}
		expect(client.getQueryState(keys.projects)?.isInvalidated).toBe(false);
	});

	test("also reaches the 'view' caches of the server pages", async () => {
		const client = testQueryClient();
		client.setQueryData(serverViewsQuery().queryKey, []);
		client.setQueryData(serverViewQuery("srv-1").queryKey, {} as never);
		await invalidateTunnel(client);
		expect(
			client.getQueryState(serverViewsQuery().queryKey)?.isInvalidated,
		).toBe(true);
		expect(
			client.getQueryState(serverViewQuery("srv-1").queryKey)?.isInvalidated,
		).toBe(true);
	});
});
