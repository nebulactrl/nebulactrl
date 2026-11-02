import { describe, expect, test } from "bun:test";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import {
	CLOUDFLARE_OPERATIONS,
	cloudflareIpsQuery,
	cloudflareQuery,
	cloudflareZonesQuery,
	domainDnsQuery,
} from "./queries";

describe("cloudflare queries", () => {
	test.each([
		[
			"the connection",
			() => cloudflareQuery(),
			"GET /integrations/cloudflare",
			make("CloudflareConnectionView"),
			"/integrations/cloudflare",
		],
		[
			"the zones",
			() => cloudflareZonesQuery(),
			"GET /integrations/cloudflare/zones",
			make("ZonesOutputBody"),
			"/integrations/cloudflare/zones",
		],
		[
			"the origin-lock IP ranges",
			() => cloudflareIpsQuery(),
			"GET /cloudflare/ips",
			make("IPListView"),
			"/cloudflare/ips",
		],
		[
			"a domain's live DNS",
			() => domainDnsQuery("dom1"),
			"GET /domains/:id/dns",
			make("DNSStatus"),
			"/domains/dom1/dns",
		],
	] as const)(
		"%s are read from the control plane and invalidated with their operation",
		async (_name, query, route, body, path) => {
			const api = stubApi({ [route]: { body } });
			const client = testQueryClient();
			const options = query();
			await client.fetchQuery(options as never);
			expect(api.unmatched).toEqual([]);
			expect(api.calls.map((c) => c.path)).toEqual([path]);
			// a connection or DNS change invalidates by operation id: the query must carry one it lists
			const head = (options.queryKey[0] ?? {}) as { _id?: string };
			expect(CLOUDFLARE_OPERATIONS).toContain(head._id as never);
		},
	);

	test("the domain query is keyed per domain", () => {
		expect(domainDnsQuery("a").queryKey).not.toEqual(
			domainDnsQuery("b").queryKey,
		);
	});
});
