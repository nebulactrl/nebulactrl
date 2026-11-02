import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import {
	type TrafficParams,
	type TrafficScope,
	useTrafficReport,
} from "./queries";
import { makeReport } from "./test-fixtures";

const base = makeReport();
const report = makeReport({
	range: {
		from: "2026-01-01T00:00:00Z",
		to: "2026-01-02T00:00:00Z",
		stepSeconds: 300,
		truncated: false,
		retention: { minuteHours: 48, hourlyDays: 30, dailyDays: 365 },
	},
	summary: { ...base.summary, requests: 7 },
	series: [],
});

const params: TrafficParams = {
	range: "24h",
	compare: false,
	host: null,
	pagesOnly: true,
};

function run(scope: TrafficScope, p: TrafficParams = params, enabled = true) {
	const client = testQueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
	return renderHook(() => useTrafficReport(scope, p, enabled), { wrapper });
}

const routes = () => ({
	"GET /services/:serviceId/analytics": { body: report },
	"GET /servers/:serverId/analytics": { body: report },
	"GET /analytics/traffic": { body: report },
});

describe("useTrafficReport", () => {
	test("a service scope asks the service endpoint only, with range and pages", async () => {
		const api = stubApi(routes());
		const { result } = run({ serviceId: "svc-1" });
		await waitFor(() => expect(result.current.data?.summary.requests).toBe(7));
		expect(api.calls).toHaveLength(1);
		const [call] = api.calls;
		expect(call?.method).toBe("GET");
		expect(call?.path).toBe("/services/svc-1/analytics");
		expect(call?.query.get("range")).toBe("24h");
		expect(call?.query.get("pages")).toBe("true");
		expect(call?.query.has("compare")).toBe(false);
		expect(call?.query.has("host")).toBe(false);
		expect(api.unmatched).toEqual([]);
	});

	test("a service can be narrowed to a host and asked for the previous period", async () => {
		const api = stubApi(routes());
		const { result } = run(
			{ serviceId: "svc-1" },
			{ range: "7d", compare: true, host: "app.example.com", pagesOnly: false },
		);
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		const query = api.calls[0]?.query;
		expect(query?.get("range")).toBe("7d");
		expect(query?.get("compare")).toBe("prev");
		expect(query?.get("host")).toBe("app.example.com");
		expect(query?.get("pages")).toBe("false");
	});

	test("a server scope uses the server endpoint and never sends a host", async () => {
		const api = stubApi(routes());
		const { result } = run(
			{ serverId: "srv-9" },
			{ ...params, host: "ignored.example.com" },
		);
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(api.calls.map((c) => c.path)).toEqual(["/servers/srv-9/analytics"]);
		expect(api.calls[0]?.query.has("host")).toBe(false);
	});

	test("an environment scope uses the traffic endpoint with environmentId", async () => {
		const api = stubApi(routes());
		const { result } = run({ environmentId: "env-3" });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(api.calls.map((c) => c.path)).toEqual(["/analytics/traffic"]);
		expect(api.calls[0]?.query.get("environmentId")).toBe("env-3");
	});

	test("disabled, it makes no request", async () => {
		const api = stubApi(routes());
		const { result } = run({ serviceId: "svc-1" }, params, false);
		expect(result.current.fetchStatus).toBe("idle");
		expect(result.current.data).toBeUndefined();
		expect(api.calls).toEqual([]);
	});

	test("a failing endpoint surfaces as an error", async () => {
		stubApi({
			"GET /services/:serviceId/analytics": problem(500, { title: "boom" }),
		});
		const { result } = run({ serviceId: "svc-1" });
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(result.current.data).toBeUndefined();
	});
});
