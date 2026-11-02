import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { stubApi } from "#/test/api";
import { serverMetricsQuery, serviceMetricsQuery } from "./queries";

const body = { points: [], retentionDays: 7, stepSeconds: 10 };

describe("serviceMetricsQuery", () => {
	test("requests the service's metrics with the range", async () => {
		const api = stubApi({ "GET /services/:id/metrics": { body } });
		await new QueryClient().fetchQuery(serviceMetricsQuery("svc_1", "6h"));
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/services/svc_1/metrics");
		expect(api.calls[0]?.query.get("range")).toBe("6h");
		expect(api.calls[0]?.query.has("process")).toBe(false);
	});

	test("narrows to a process when one is given", async () => {
		const api = stubApi({ "GET /services/:id/metrics": { body } });
		await new QueryClient().fetchQuery(
			serviceMetricsQuery("svc_1", "1h", "worker"),
		);
		expect(api.calls[0]?.query.get("process")).toBe("worker");
	});

	test("keys differ by range and process so each view caches on its own", () => {
		const keys = [
			serviceMetricsQuery("svc_1", "1h").queryKey,
			serviceMetricsQuery("svc_1", "6h").queryKey,
			serviceMetricsQuery("svc_1", "1h", "worker").queryKey,
			serviceMetricsQuery("svc_2", "1h").queryKey,
		].map((k) => JSON.stringify(k));
		expect(new Set(keys).size).toBe(4);
	});

	test("refetches every 5 s on live ranges and every 15 s beyond", () => {
		expect(serviceMetricsQuery("s", "5m").refetchInterval).toBe(5_000);
		expect(serviceMetricsQuery("s", "10m").refetchInterval).toBe(5_000);
		expect(serviceMetricsQuery("s", "30m").refetchInterval).toBe(15_000);
		expect(serviceMetricsQuery("s", "30d").refetchInterval).toBe(15_000);
	});

	test("keeps the previous range's data while the next one loads", async () => {
		const client = new QueryClient();
		const hour = { ...body, stepSeconds: 60 };
		stubApi({
			"GET /services/:id/metrics": (request) =>
				Response.json(
					new URL(request.url).searchParams.get("range") === "1h" ? hour : body,
				),
		});
		const options = serviceMetricsQuery("svc_1", "1h");
		await client.fetchQuery(options);
		const placeholder = serviceMetricsQuery("svc_1", "6h").placeholderData;
		expect(typeof placeholder).toBe("function");
		const kept = (placeholder as (prev: unknown) => unknown)(
			client.getQueryData(options.queryKey),
		);
		expect(kept).toEqual(hour);
	});
});

describe("serverMetricsQuery", () => {
	test("requests the server's metrics with the range", async () => {
		const api = stubApi({ "GET /servers/:id/metrics": { body } });
		await new QueryClient().fetchQuery(serverMetricsQuery("srv_1", "24h"));
		expect(api.calls[0]?.path).toBe("/servers/srv_1/metrics");
		expect(api.calls[0]?.query.get("range")).toBe("24h");
	});

	test("shares the live cadence rule with the service query", () => {
		expect(serverMetricsQuery("s", "5m").refetchInterval).toBe(5_000);
		expect(serverMetricsQuery("s", "1h").refetchInterval).toBe(15_000);
	});
});
