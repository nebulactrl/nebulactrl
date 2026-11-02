import { beforeEach, describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { stubChartSize } from "./chart-size";
import { MetricsPanel } from "./metrics-panel";
import {
	serverBody,
	serverMetrics,
	serverPoint,
	serviceMetrics,
} from "./test-fixtures";

beforeEach(() => {
	window.localStorage.clear();
	stubChartSize();
	return () => window.localStorage.clear();
});

describe("MetricsPanel", () => {
	test("a service scope loads that service's metrics, not a server's", async () => {
		const api = stubApi({
			"GET /services/:id/metrics": { body: serviceMetrics([]) },
			"GET /services/:id": problem(404, { detail: "gone" }),
			"GET /services/:id/deployments": { body: { items: [] } },
		});
		await renderWithProviders(<MetricsPanel scope={{ serviceId: "svc-9" }} />);
		expect(await screen.findByText("No samples yet")).toBeInTheDocument();
		expect(screen.getByText(/container must be running/)).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toContain("/services/svc-9/metrics");
		expect(api.calls.some((c) => c.path.startsWith("/servers"))).toBe(false);
	});

	test("a server scope loads that server's metrics and shows its three charts", async () => {
		const api = stubApi({
			"GET /servers/:id/metrics": { body: serverMetrics([serverPoint()]) },
			"GET /servers/:id": { body: serverBody() },
		});
		await renderWithProviders(<MetricsPanel scope={{ serverId: "srv_9" }} />);
		expect(
			await screen.findByRole("heading", { name: "Disk" }),
		).toBeInTheDocument();
		expect(screen.getByRole("heading", { name: "CPU" })).toBeInTheDocument();
		expect(screen.getByRole("heading", { name: "Memory" })).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toContain("/servers/srv_9/metrics");
		expect(api.calls.some((c) => c.path.startsWith("/services"))).toBe(false);
	});
});
