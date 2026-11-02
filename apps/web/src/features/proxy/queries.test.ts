import { describe, expect, test } from "bun:test";
import { QueryClient, type QueryKey } from "@tanstack/react-query";
import {
	getServerOptions,
	listProjectsOptions,
	listServicesOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type { ServerProxy } from "#/api/gen/types.gen";
import {
	invalidateProxy,
	invalidateRoutes,
	proxyRoutesQuery,
	serverCertificatesQuery,
	serverPortsQuery,
	serverProxyQuery,
} from "./queries";

function proxy(overrides: {
	deploy?: string;
	tailnet?: string;
}): ServerProxy | undefined {
	return {
		lastDeploy:
			overrides.deploy === undefined ? undefined : { status: overrides.deploy },
		tailnet:
			overrides.tailnet === undefined
				? {}
				: { status: { state: overrides.tailnet } },
	} as unknown as ServerProxy;
}

/** The polling interval the proxy card's query asks for given its cached data. */
function intervalFor(data: ServerProxy | undefined) {
	const { refetchInterval } = serverProxyQuery("srv-1");
	return refetchInterval({ state: { data } });
}

describe("serverProxyQuery polling", () => {
	test("does not poll before the first answer", () => {
		expect(intervalFor(undefined)).toBe(false);
	});

	test.each(["pending", "sent", "acked"])(
		"polls every 2s while the last deploy is %s",
		(status) => {
			expect(intervalFor(proxy({ deploy: status }))).toBe(2_000);
		},
	);

	test.each(["applied", "failed"])(
		"stops polling once the last deploy is %s",
		(status) => {
			expect(intervalFor(proxy({ deploy: status }))).toBe(false);
		},
	);

	test("polls while the tailnet node is provisioning", () => {
		expect(intervalFor(proxy({ tailnet: "provisioning" }))).toBe(2_000);
	});

	test("does not poll for an online tailnet node or a proxy that never deployed", () => {
		expect(intervalFor(proxy({ tailnet: "online" }))).toBe(false);
		expect(intervalFor(proxy({}))).toBe(false);
	});
});

describe("query keys", () => {
	test("address the server they are built for", () => {
		const a = serverPortsQuery("srv-a").queryKey;
		const b = serverPortsQuery("srv-b").queryKey;
		expect(JSON.stringify(a)).toContain("srv-a");
		expect(JSON.stringify(b)).toContain("srv-b");
		expect(JSON.stringify(serverCertificatesQuery("srv-a").queryKey)).toContain(
			"srv-a",
		);
		expect(JSON.stringify(proxyRoutesQuery("srv-a").queryKey)).toContain(
			"srv-a",
		);
	});
});

function isInvalidated(client: QueryClient, key: readonly unknown[]): boolean {
	return client.getQueryState(key)?.isInvalidated ?? false;
}

function seeded() {
	const client = new QueryClient();
	const keys = {
		proxy: serverProxyQuery("srv-1").queryKey,
		certificates: serverCertificatesQuery("srv-1").queryKey,
		routes: proxyRoutesQuery("srv-1").queryKey,
		ports: serverPortsQuery("srv-1").queryKey,
		server: getServerOptions({ path: { serverId: "srv-1" } }).queryKey,
		services: listServicesOptions({ path: { environmentId: "env-1" } })
			.queryKey,
		projects: listProjectsOptions().queryKey,
	};
	for (const key of Object.values(keys)) {
		client.setQueryData(key as QueryKey, {});
	}
	return { client, keys };
}

describe("invalidateProxy", () => {
	test("marks the server-level queries stale and leaves the rest", async () => {
		const { client, keys } = seeded();
		await invalidateProxy(client);
		expect(isInvalidated(client, keys.proxy)).toBe(true);
		expect(isInvalidated(client, keys.certificates)).toBe(true);
		expect(isInvalidated(client, keys.routes)).toBe(true);
		expect(isInvalidated(client, keys.ports)).toBe(true);
		expect(isInvalidated(client, keys.server)).toBe(true);
		expect(isInvalidated(client, keys.services)).toBe(false);
		expect(isInvalidated(client, keys.projects)).toBe(false);
	});
});

describe("invalidateRoutes", () => {
	test("marks services and the server's route and port tables stale", async () => {
		const { client, keys } = seeded();
		await invalidateRoutes(client);
		expect(isInvalidated(client, keys.services)).toBe(true);
		expect(isInvalidated(client, keys.routes)).toBe(true);
		expect(isInvalidated(client, keys.ports)).toBe(true);
		expect(isInvalidated(client, keys.projects)).toBe(false);
	});
});
