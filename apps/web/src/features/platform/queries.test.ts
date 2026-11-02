import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { listPlatformServicesOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { Server } from "#/api/models";
import {
	invalidatePlatform,
	platformServiceQuery,
	platformServicesQuery,
	usableServers,
} from "./queries";
import { platformListing, platformService } from "./test-fixtures";

function interval(
	options: { refetchInterval?: unknown },
	data: unknown,
): unknown {
	const fn = options.refetchInterval as (q: {
		state: { data: unknown };
	}) => number;
	return fn({ state: { data } });
}

describe("platform queries", () => {
	test("the listing polls fast while something provisions or is removed, slowly otherwise", () => {
		const q = platformServicesQuery();
		expect(interval(q, undefined)).toBe(15_000);
		expect(
			interval(q, platformListing(platformService({ status: "ready" }))),
		).toBe(15_000);
		expect(
			interval(q, platformListing(platformService({ status: "provisioning" }))),
		).toBe(3_000);
		expect(
			interval(q, platformListing(platformService({ status: "removing" }))),
		).toBe(3_000);
		expect(
			interval(q, platformListing(platformService({ status: "failed" }))),
		).toBe(15_000);
	});

	test("one service follows the same rhythm", () => {
		const q = platformServiceQuery("ps-1");
		expect(interval(q, platformService({ status: "provisioning" }))).toBe(
			3_000,
		);
		expect(interval(q, platformService({ status: "ready" }))).toBe(15_000);
		expect(interval(q, undefined)).toBe(15_000);
	});

	test("invalidatePlatform refreshes the platform views and the Tailscale integration only", async () => {
		const client = new QueryClient();
		const listKey = listPlatformServicesOptions().queryKey;
		client.setQueryData(listKey, platformListing());
		client.setQueryData(
			platformServiceQuery("ps-1").queryKey,
			platformService(),
		);
		client.setQueryData([{ _id: "getTailscaleIntegration" }], {});
		client.setQueryData([{ _id: "listServers" }], {});
		await invalidatePlatform(client);
		const stale = (key: readonly unknown[]) =>
			client.getQueryState(key)?.isInvalidated;
		expect(stale(listKey)).toBe(true);
		expect(stale(platformServiceQuery("ps-1").queryKey)).toBe(true);
		expect(stale([{ _id: "getTailscaleIntegration" }])).toBe(true);
		expect(stale([{ _id: "listServers" }])).toBe(false);
	});

	test("usableServers keeps online servers that take new services", () => {
		const server = (over: Partial<Server>) =>
			({
				status: "online",
				drainState: "none",
				acceptWorkloads: true,
				...over,
			}) as Server;
		const online = server({ id: "a" });
		const result = usableServers([
			online,
			server({ id: "b", status: "offline" }),
			server({ id: "c", status: "pending" }),
			server({ id: "d", drainState: "draining" }),
			server({ id: "e", acceptWorkloads: false }),
		]);
		expect(result.map((s) => s.id)).toEqual(["a"]);
	});
});
