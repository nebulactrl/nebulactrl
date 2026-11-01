import { describe, expect, test } from "bun:test";
import type { DashboardAccessView } from "#/api/gen/types.gen";
import {
	allowSummary,
	certificateChoices,
	checkDraft,
	draftFor,
	draftValid,
	isDirty,
	listAdmits,
	lockoutOf,
	TAILNET_ONLY,
	toBody,
	withCurrentIp,
} from "./model";

function view(over: Partial<DashboardAccessView> = {}): DashboardAccessView {
	return {
		enabled: true,
		host: "nebula.example.com",
		allowCidrs: [],
		dnsResolver: "",
		resolverAvailable: [],
		certificate: { method: "http-01", status: "unknown", message: "" },
		clientIp: "203.0.113.9",
		clientIpAllowed: true,
		viaDashboardRoute: true,
		proxyDeployRequired: false,
		proxyServerId: null,
		source: "default",
		updatedAt: null,
		...over,
	} as DashboardAccessView;
}

describe("listAdmits", () => {
	test("an empty list lets everybody in", () => {
		expect(listAdmits([], "203.0.113.9")).toBe(true);
		expect(listAdmits(["", "  "], "203.0.113.9")).toBe(true);
	});
	test("the tailnet preset", () => {
		expect(listAdmits(TAILNET_ONLY, "100.101.102.103")).toBe(true);
		expect(listAdmits(TAILNET_ONLY, "100.127.255.255")).toBe(true);
		expect(listAdmits(TAILNET_ONLY, "100.128.0.0")).toBe(false);
		expect(listAdmits(TAILNET_ONLY, "100.63.255.255")).toBe(false);
		expect(listAdmits(TAILNET_ONLY, "fd7a:115c:a1e0:ab12::1")).toBe(true);
		expect(listAdmits(TAILNET_ONLY, "fd7a:115c:a1e1::1")).toBe(false);
		expect(listAdmits(TAILNET_ONLY, "203.0.113.9")).toBe(false);
	});
	test("single addresses and unmasked CIDRs", () => {
		expect(listAdmits(["203.0.113.9"], "203.0.113.9")).toBe(true);
		expect(listAdmits(["203.0.113.9"], "203.0.113.10")).toBe(false);
		expect(listAdmits(["10.1.2.3/8"], "10.200.0.1")).toBe(true);
		expect(listAdmits(["2001:db8::1"], "2001:db8::1")).toBe(true);
	});
	test("an IPv4-mapped IPv6 caller is the IPv4 address", () => {
		expect(listAdmits(["192.0.2.0/24"], "::ffff:192.0.2.77")).toBe(true);
	});
	test("families never mix, and bad entries match nothing", () => {
		expect(listAdmits(["::/0"], "203.0.113.9")).toBe(false);
		expect(listAdmits(["0.0.0.0/0"], "2001:db8::1")).toBe(false);
		expect(listAdmits(["nonsense"], "203.0.113.9")).toBe(false);
		expect(listAdmits(["203.0.113.9"], "not-an-ip")).toBe(false);
	});
});

describe("lockoutOf", () => {
	test("a list that leaves the caller out locks them out", () => {
		const v = view();
		expect(lockoutOf({ allow: [...TAILNET_ONLY], resolver: "" }, v)).toEqual({
			locksOut: true,
			ip: "203.0.113.9",
		});
	});
	test("a list that includes the caller does not", () => {
		const v = view({ clientIp: "100.101.102.103" });
		expect(
			lockoutOf({ allow: [...TAILNET_ONLY], resolver: "" }, v).locksOut,
		).toBe(false);
		expect(
			lockoutOf({ allow: ["203.0.113.0/24"], resolver: "" }, view()).locksOut,
		).toBe(false);
	});
	test("an empty list never does", () => {
		expect(lockoutOf({ allow: [], resolver: "" }, view()).locksOut).toBe(false);
		expect(lockoutOf({ allow: [" "], resolver: "" }, view()).locksOut).toBe(
			false,
		);
	});
	test("a caller who does not come through the route is out of its reach", () => {
		const v = view({ viaDashboardRoute: false });
		expect(
			lockoutOf({ allow: [...TAILNET_ONLY], resolver: "" }, v).locksOut,
		).toBe(false);
	});
});

describe("withCurrentIp", () => {
	test("adds the address to a list that lacks it", () => {
		expect(withCurrentIp(["100.64.0.0/10"], "203.0.113.9")).toEqual([
			"100.64.0.0/10",
			"203.0.113.9",
		]);
		expect(withCurrentIp([], "2001:db8::7")).toEqual(["2001:db8::7"]);
	});
	test("keeps a list that already lets the address in", () => {
		expect(withCurrentIp(["203.0.113.0/24"], "203.0.113.9")).toEqual([
			"203.0.113.0/24",
		]);
	});
	test("ignores an unknown address", () => {
		expect(withCurrentIp(["10.0.0.0/8"], " ")).toEqual(["10.0.0.0/8"]);
	});
});

describe("draft", () => {
	test("starts from the view and is clean", () => {
		const v = view({
			allowCidrs: ["100.64.0.0/10"],
			dnsResolver: "cloudflare",
		});
		const d = draftFor(v);
		expect(d).toEqual({ allow: ["100.64.0.0/10"], resolver: "cloudflare" });
		expect(isDirty(d, v)).toBe(false);
	});
	test("a spelling change alone is not a change", () => {
		const v = view({ allowCidrs: ["10.0.0.0/8"] });
		expect(isDirty({ allow: ["10.1.2.3/8"], resolver: "" }, v)).toBe(false);
		expect(
			isDirty({ allow: ["10.0.0.0/8", "10.0.0.0/8"], resolver: "" }, v),
		).toBe(false);
	});
	test("a changed list or provider is dirty", () => {
		const v = view();
		expect(isDirty({ allow: ["10.0.0.0/8"], resolver: "" }, v)).toBe(true);
		expect(isDirty({ allow: [], resolver: "cloudflare" }, v)).toBe(true);
	});
	test("validation names the bad rows", () => {
		const d = { allow: ["100.64.0.0/10", "nope", "10.0.0.0/33"], resolver: "" };
		const check = checkDraft(d);
		expect(Object.keys(check.entries).sort()).toEqual([
			"allowCidrs.1",
			"allowCidrs.2",
		]);
		expect(draftValid(d)).toBe(false);
		expect(draftValid({ allow: [...TAILNET_ONLY], resolver: "" })).toBe(true);
		expect(draftValid({ allow: [], resolver: "" })).toBe(true);
	});
	test("more than 200 networks is refused", () => {
		const allow = Array.from({ length: 201 }, (_, i) => `10.${i}.0.0/16`);
		expect(draftValid({ allow, resolver: "" })).toBe(false);
	});
});

describe("toBody", () => {
	test("cleans the list and sends the confirmation only when given", () => {
		const d = {
			allow: [" 100.64.0.0/10 ", "", "100.64.0.0/10"],
			resolver: "cloudflare",
		};
		expect(toBody(d, false)).toEqual({
			allowCidrs: ["100.64.0.0/10"],
			dnsResolver: "cloudflare",
		});
		expect(toBody(d, true)).toEqual({
			allowCidrs: ["100.64.0.0/10"],
			dnsResolver: "cloudflare",
			confirmLockout: true,
		});
	});
});

describe("certificateChoices", () => {
	test("HTTP-01 plus every provider with a resolver", () => {
		const v = view({
			resolverAvailable: [
				{ provider: "cloudflare", providerName: "Cloudflare", applied: true },
			],
		});
		expect(certificateChoices(v).map((c) => c.value)).toEqual([
			"",
			"cloudflare",
		]);
		expect(certificateChoices(v)[1]?.label).toBe("DNS-01 Cloudflare");
	});
	test("a provider that lost its resolver stays visible but cannot be picked", () => {
		const c = certificateChoices(view({ dnsResolver: "route53" }));
		expect(c.at(-1)).toMatchObject({ value: "route53", disabled: true });
	});
});

describe("allowSummary", () => {
	test("says who gets in", () => {
		expect(allowSummary([])).toContain("everybody");
		expect(allowSummary(["10.0.0.0/8"])).toContain("this network");
		expect(allowSummary([...TAILNET_ONLY])).toContain("these 2 networks");
	});
});
