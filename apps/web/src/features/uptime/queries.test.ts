import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import {
	environmentUptimeQuery,
	MONITOR_OPERATIONS,
	serviceUptimeQuery,
	uptimeMonitorQuery,
} from "./queries";

const client = () =>
	new QueryClient({ defaultOptions: { queries: { retry: false } } });

describe("uptime queries", () => {
	test("serviceUptimeQuery asks for the service's uptime over the range", async () => {
		const api = stubApi({
			"GET /services/:id/uptime": {
				body: make("ServiceReport", { range: "7d" }),
			},
		});
		await client().fetchQuery(serviceUptimeQuery("svc-1", "7d"));
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/services/svc-1/uptime");
		expect(api.calls[0]?.query.get("range")).toBe("7d");
		expect(api.unmatched).toEqual([]);
	});

	test("the range is part of the cache key, so each range caches apart", () => {
		expect(serviceUptimeQuery("svc-1", "1h").queryKey).not.toEqual(
			serviceUptimeQuery("svc-1", "24h").queryKey,
		);
		expect(serviceUptimeQuery("svc-1", "1h").queryKey).not.toEqual(
			serviceUptimeQuery("svc-2", "1h").queryKey,
		);
	});

	test("live views refetch every 30 s, the pace the agent probes at", () => {
		expect(serviceUptimeQuery("s", "1h").refetchInterval).toBe(30_000);
		expect(environmentUptimeQuery("e").refetchInterval).toBe(30_000);
	});

	test("switching range keeps the previous data while the new one loads", async () => {
		stubApi({
			"GET /services/:id/uptime": (request) =>
				Response.json({
					range: new URL(request.url).searchParams.get("range"),
				}),
		});
		const queryClient = client();
		await queryClient.fetchQuery(serviceUptimeQuery("s", "1h"));
		const placeholder = serviceUptimeQuery("s", "24h").placeholderData;
		expect(typeof placeholder).toBe("function");
		const previous = { range: "1h" };
		expect((placeholder as (prev: unknown) => unknown)(previous)).toBe(
			previous,
		);
	});

	test("environmentUptimeQuery asks for the environment's services", async () => {
		const api = stubApi({
			"GET /environments/:id/uptime": { body: { items: [] } },
		});
		const data = await client().fetchQuery(environmentUptimeQuery("env-9"));
		expect(api.calls[0]?.path).toBe("/environments/env-9/uptime");
		expect(data).toEqual({ items: [] });
	});

	test("uptimeMonitorQuery loads the saved monitor of the service", async () => {
		const monitor = {
			enabled: true,
			alerts: true,
			failureThreshold: 3,
			recoveryThreshold: 2,
			reminderMinutes: 30,
		};
		const api = stubApi({
			"GET /services/:id/uptime-monitor": { body: monitor },
		});
		const data = await client().fetchQuery(uptimeMonitorQuery("svc-4"));
		expect(api.calls[0]?.path).toBe("/services/svc-4/uptime-monitor");
		expect(data).toEqual(monitor);
	});

	test("a monitor change touches the settings, the card and the environment list", () => {
		expect([...MONITOR_OPERATIONS].sort()).toEqual([
			"getEnvironmentUptime",
			"getServiceUptime",
			"getUptimeMonitor",
		]);
	});
});
