import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import {
	getServiceGitOptions,
	getSystemOptions,
	listGitConnectionsOptions,
	listGitDeliveriesOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { invalidateGit } from "./queries";

describe("invalidateGit", () => {
	test("marks connection, delivery and service-link queries stale and leaves others alone", async () => {
		stubApi({
			"GET /git/connections": { body: { items: [] } },
			"GET /git/connections/:id/deliveries": { body: { items: [] } },
			"GET /services/:id/git": { body: make("GitLinkView", { linked: false }) },
			"GET /system": { body: make("SystemInfo") },
		});
		const qc = new QueryClient();
		const connections = listGitConnectionsOptions();
		const deliveries = listGitDeliveriesOptions({
			path: { connectionId: "c1" },
		});
		const serviceGit = getServiceGitOptions({ path: { serviceId: "s1" } });
		const system = getSystemOptions();
		await qc.fetchQuery(connections);
		await qc.fetchQuery(deliveries);
		await qc.fetchQuery(serviceGit);
		await qc.fetchQuery(system);
		const invalidated = (key: readonly unknown[]) =>
			qc.getQueryState(key)?.isInvalidated;
		expect(invalidated(connections.queryKey)).toBe(false);

		await invalidateGit(qc);

		expect(invalidated(connections.queryKey)).toBe(true);
		expect(invalidated(deliveries.queryKey)).toBe(true);
		expect(invalidated(serviceGit.queryKey)).toBe(true);
		expect(invalidated(system.queryKey)).toBe(false);
	});
});
